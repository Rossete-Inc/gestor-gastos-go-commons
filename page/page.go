// Package page cuida da paginação por cursor opaco.
//
// O cursor carrega a chave de onde a próxima Query continua (o
// LastEvaluatedKey do DynamoDB, com atributos string), codificada em
// base64url e assinada com HMAC-SHA256. A assinatura cobre também um escopo
// (por exemplo o usuário e os filtros da listagem), então o cliente não
// consegue forjar uma chave nem reaproveitar o cursor de outra listagem.
package page

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/errs"
)

// Tamanhos de página.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// MinSecretBytes é o tamanho mínimo do segredo do HMAC.
const MinSecretBytes = 32

// maxCursorLen limita o trabalho feito com cursores recebidos do cliente.
const maxCursorLen = 2048

const version = "v1"

// b64 rejeita bits de preenchimento diferentes de zero, para que dois textos
// distintos nunca decodifiquem nos mesmos bytes.
var b64 = base64.RawURLEncoding.Strict()

// Key é a chave de continuação: nome do atributo → valor string, como
// PK, SK e as chaves de um GSI.
type Key map[string]string

// Page é uma página de resultados. NextCursor vazio indica a última página.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// New monta a página garantindo Items não nulo, para o JSON trazer [] e
// não null.
func New[T any](items []T, nextCursor string) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, NextCursor: nextCursor}
}

// Limit normaliza o tamanho de página pedido: zero ou negativo usa
// DefaultLimit, e acima de MaxLimit é limitado a MaxLimit.
func Limit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	default:
		return n
	}
}

// Codec codifica e valida cursores com um segredo.
type Codec struct {
	secret []byte
}

// NewCodec cria o Codec. O segredo vem do SSM (SecureString) na AWS e do
// ambiente local, e precisa ter pelo menos MinSecretBytes bytes.
func NewCodec(secret []byte) (*Codec, error) {
	if len(secret) < MinSecretBytes {
		return nil, fmt.Errorf("page: segredo do cursor precisa de pelo menos %d bytes, tem %d", MinSecretBytes, len(secret))
	}
	return &Codec{secret: append([]byte(nil), secret...)}, nil
}

// Encode gera o cursor da chave dentro do escopo. Chave vazia gera cursor
// vazio, que significa "não há próxima página".
func (c *Codec) Encode(scope string, key Key) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return "", fmt.Errorf("page: serializar chave: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return version + "." + payload + "." + c.sign(scope, payload), nil
}

// Decode valida o cursor no escopo e devolve a chave. Cursor vazio devolve
// nil (primeira página). Qualquer cursor adulterado, truncado ou de outro
// escopo vira 400 "cursor_invalido", com a causa só no log.
func (c *Codec) Decode(scope, cursor string) (Key, error) {
	if cursor == "" {
		return nil, nil
	}
	key, err := c.decode(scope, cursor)
	if err != nil {
		return nil, errs.BadRequest("cursor_invalido", "Cursor de paginação inválido. Volte para a primeira página.").WithCause(err)
	}
	return key, nil
}

func (c *Codec) decode(scope, cursor string) (Key, error) {
	if len(cursor) > maxCursorLen {
		return nil, fmt.Errorf("cursor com %d caracteres", len(cursor))
	}
	parts := strings.Split(cursor, ".")
	if len(parts) != 3 || parts[0] != version {
		return nil, errors.New("formato desconhecido")
	}
	payload, sig := parts[1], parts[2]

	got, err := b64.DecodeString(sig)
	if err != nil {
		return nil, fmt.Errorf("assinatura: %w", err)
	}
	want, _ := b64.DecodeString(c.sign(scope, payload))
	if !hmac.Equal(got, want) {
		return nil, errors.New("assinatura não confere")
	}

	raw, err := b64.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	var key Key
	if err := json.Unmarshal(raw, &key); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if len(key) == 0 {
		return nil, errors.New("chave vazia")
	}
	return key, nil
}

// sign cobre versão, escopo e payload. O separador \x00 impede que escopo e
// payload diferentes produzam a mesma mensagem.
func (c *Codec) sign(scope, payload string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte(version + "\x00" + scope + "\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
