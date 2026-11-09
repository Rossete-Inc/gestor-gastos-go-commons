// Package errs define os erros de domínio do Gestor de Gastos e a conversão
// deles para application/problem+json (RFC 9457).
//
// Os serviços devolvem *Error. O BFF converte para Problem na borda HTTP, e
// o transporte entre BFF e serviços (pacote invoke) usa o mesmo Problem para
// reconstruir o erro do outro lado sem perder o tipo.
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

// Kind classifica o erro e determina o status HTTP.
type Kind int

const (
	KindInternal Kind = iota
	KindBadRequest
	KindUnauthorized
	KindForbidden
	KindNotFound
	KindConflict
	KindValidation
)

var kindStatus = map[Kind]int{
	KindInternal:     http.StatusInternalServerError,
	KindBadRequest:   http.StatusBadRequest,
	KindUnauthorized: http.StatusUnauthorized,
	KindForbidden:    http.StatusForbidden,
	KindNotFound:     http.StatusNotFound,
	KindConflict:     http.StatusConflict,
	KindValidation:   http.StatusUnprocessableEntity,
}

// Status devolve o status HTTP do Kind.
func (k Kind) Status() int { return kindStatus[k] }

func kindFromStatus(status int) Kind {
	for k, s := range kindStatus {
		if s == status {
			return k
		}
	}
	if status >= 400 && status < 500 {
		return KindBadRequest
	}
	return KindInternal
}

// FieldError aponta um campo inválido num erro de validação.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error é um erro de domínio com tipo, código estável e mensagem para o usuário.
type Error struct {
	Kind    Kind
	Code    string // estável e em snake_case, por exemplo "categoria_em_uso"
	Message string // texto em pt-BR que pode ser mostrado ao usuário
	Fields  []FieldError
	Err     error // causa interna; nunca é exposta ao cliente
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Is permite comparar por Kind e Code com errors.Is(err, errs.NotFound("x", "")).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind && t.Code == e.Code
}

// WithCause anexa a causa interna e devolve o próprio erro.
func (e *Error) WithCause(err error) *Error { e.Err = err; return e }

func newErr(k Kind, code, msg string) *Error { return &Error{Kind: k, Code: code, Message: msg} }

func BadRequest(code, msg string) *Error   { return newErr(KindBadRequest, code, msg) }
func Unauthorized(code, msg string) *Error { return newErr(KindUnauthorized, code, msg) }
func Forbidden(code, msg string) *Error    { return newErr(KindForbidden, code, msg) }
func NotFound(code, msg string) *Error     { return newErr(KindNotFound, code, msg) }
func Conflict(code, msg string) *Error     { return newErr(KindConflict, code, msg) }

// Validation cria um erro 422 com os campos inválidos.
func Validation(msg string, fields ...FieldError) *Error {
	e := newErr(KindValidation, "validacao", msg)
	e.Fields = fields
	return e
}

// Internal embrulha uma falha inesperada. A causa fica só no log.
func Internal(err error) *Error {
	return &Error{Kind: KindInternal, Code: "erro_interno", Message: "Erro interno. Tente novamente.", Err: err}
}

// As extrai o *Error de err. Qualquer outro erro vira Internal.
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}

// KindOf devolve o Kind de err (KindInternal para erros desconhecidos).
func KindOf(err error) Kind { return As(err).Kind }
