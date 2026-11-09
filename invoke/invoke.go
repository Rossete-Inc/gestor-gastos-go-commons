// Package invoke é o transporte síncrono entre o BFF e os serviços (ADR 0004).
//
// O BFF monta um envelope Request (operação + payload + userId) e o envia por
// um Transport. Na AWS o transporte é Lambda Invoke; localmente é um POST
// HTTP ao cmd/local do serviço. O serviço recebe o mesmo envelope pelo Router,
// que chama o mesmo handler nos dois casos.
//
// Erros de domínio atravessam o transporte como errs.Problem e são
// reconstruídos do outro lado com o mesmo Kind, Code e Fields. Falhas do
// próprio transporte viram errs.Internal, com a causa só no log.
package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/config"
	"github.com/Rossete-Inc/gestor-gastos-go-commons/errs"
)

// MaxPayloadBytes é o limite de payload de uma invocação síncrona de Lambda.
// O transporte HTTP aplica o mesmo limite para o local se comportar igual.
const MaxPayloadBytes = 6 << 20

// Request é o envelope enviado do BFF para um serviço.
type Request struct {
	Operation string          `json:"operation"`
	UserID    string          `json:"userId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Response é o envelope devolvido pelo serviço. Quando Error vem preenchido,
// Payload é ignorado.
type Response struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *errs.Problem   `json:"error,omitempty"`
}

// Transport entrega um Request a um serviço e devolve a Response dele. O erro
// de retorno indica falha do transporte (rede, permissão, função que caiu),
// nunca um erro de domínio, que vem em Response.Error.
type Transport interface {
	Send(ctx context.Context, req Request) (Response, error)
}

// NewTransport escolhe o transporte pelo runtime. Com RUNTIME=aws, target é o
// ARN (ou nome) da função, com alias, e api é o client Lambda. Com
// RUNTIME=local, target é a URL base do cmd/local do serviço e httpClient pode
// ser nil.
func NewTransport(rt config.Runtime, target string, api LambdaAPI, httpClient *http.Client) (Transport, error) {
	if target == "" {
		return nil, errors.New("invoke: destino vazio")
	}
	switch rt {
	case config.AWS:
		if api == nil {
			return nil, errors.New("invoke: client Lambda obrigatório com RUNTIME=aws")
		}
		return NewLambda(api, target), nil
	case config.Local:
		return NewHTTP(target, httpClient), nil
	default:
		return nil, fmt.Errorf("invoke: runtime %q inválido", rt)
	}
}

// Client é usado pelo BFF para chamar operações de um serviço.
type Client struct {
	t Transport
}

// NewClient cria um Client sobre o transporte t.
func NewClient(t Transport) *Client { return &Client{t: t} }

// Call executa a operação op em nome de userID. in é serializado como
// payload (nil envia sem payload) e a resposta é decodificada em out (nil
// descarta). Erros de domínio do serviço voltam como *errs.Error.
func (c *Client) Call(ctx context.Context, op, userID string, in, out any) error {
	req := Request{Operation: op, UserID: userID}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return errs.Internal(fmt.Errorf("invoke %s: serializar payload: %w", op, err))
		}
		req.Payload = b
	}

	resp, err := c.t.Send(ctx, req)
	if err != nil {
		return errs.Internal(fmt.Errorf("invoke %s: %w", op, err))
	}
	if resp.Error != nil {
		return errs.FromProblem(*resp.Error)
	}
	if out != nil && len(resp.Payload) > 0 {
		if err := json.Unmarshal(resp.Payload, out); err != nil {
			return errs.Internal(fmt.Errorf("invoke %s: decodificar resposta: %w", op, err))
		}
	}
	return nil
}
