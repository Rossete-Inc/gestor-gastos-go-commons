package invoke

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Path é a rota em que o cmd/local de cada serviço recebe o envelope.
const Path = "/invoke"

// HTTPTransport envia o envelope por POST ao cmd/local do serviço. Só existe
// para o ambiente local; na AWS os serviços não têm endpoint HTTP.
type HTTPTransport struct {
	url    string
	client *http.Client
}

// NewHTTP cria o transporte para a URL base do serviço, por exemplo
// http://localhost:8081. client nil usa um client com timeout de 30 s, o mesmo
// timeout das Lambdas.
func NewHTTP(baseURL string, client *http.Client) *HTTPTransport {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPTransport{url: strings.TrimRight(baseURL, "/") + Path, client: client}
}

// Send implementa Transport.
func (t *HTTPTransport) Send(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("serializar envelope: %w", err)
	}
	if len(body) > MaxPayloadBytes {
		return Response{}, fmt.Errorf("envelope de %d bytes excede o limite de %d", len(body), MaxPayloadBytes)
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("montar requisição: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")

	hresp, err := t.client.Do(hreq)
	if err != nil {
		return Response{}, fmt.Errorf("POST %s: %w", t.url, err)
	}
	defer func() { _ = hresp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(hresp.Body, MaxPayloadBytes+1))
	if err != nil {
		return Response{}, fmt.Errorf("POST %s: ler resposta: %w", t.url, err)
	}
	// O servidor responde 200 sempre que entregou um envelope, inclusive com
	// erro de domínio. Outro status equivale ao FunctionError da Lambda.
	if hresp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("POST %s: status %d: %s", t.url, hresp.StatusCode, truncate(raw, 512))
	}
	if len(raw) > MaxPayloadBytes {
		return Response{}, fmt.Errorf("POST %s: resposta excede %d bytes", t.url, MaxPayloadBytes)
	}

	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Response{}, fmt.Errorf("POST %s: resposta fora do envelope: %w", t.url, err)
	}
	return resp, nil
}
