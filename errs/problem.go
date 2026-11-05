package errs

import (
	"encoding/json"
	"net/http"
)

// ContentType é o media type dos erros HTTP.
const ContentType = "application/problem+json"

// TypePrefix forma o campo "type" do problem: TypePrefix + Code.
const TypePrefix = "urn:gestor-gastos:problem:"

var titles = map[Kind]string{
	KindInternal:     "Erro interno",
	KindBadRequest:   "Requisição inválida",
	KindUnauthorized: "Não autenticado",
	KindForbidden:    "Acesso negado",
	KindNotFound:     "Não encontrado",
	KindConflict:     "Conflito",
	KindValidation:   "Dados inválidos",
}

// Problem é o corpo application/problem+json (RFC 9457), com as extensões
// "code" e "errors".
type Problem struct {
	Type     string       `json:"type"`
	Title    string       `json:"title"`
	Status   int          `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Instance string       `json:"instance,omitempty"`
	Code     string       `json:"code"`
	Errors   []FieldError `json:"errors,omitempty"`
}

// ToProblem converte qualquer erro em Problem. A causa interna nunca entra
// no corpo; erros desconhecidos viram 500 genérico.
func ToProblem(err error) Problem {
	e := As(err)
	if e == nil {
		e = Internal(nil)
	}
	return Problem{
		Type:   TypePrefix + e.Code,
		Title:  titles[e.Kind],
		Status: e.Kind.Status(),
		Detail: e.Message,
		Code:   e.Code,
		Errors: e.Fields,
	}
}

// FromProblem reconstrói o *Error a partir de um Problem recebido de outro serviço.
func FromProblem(p Problem) *Error {
	return &Error{Kind: kindFromStatus(p.Status), Code: p.Code, Message: p.Detail, Fields: p.Errors}
}

// Write grava err como problem+json. instance costuma ser o path da requisição.
func Write(w http.ResponseWriter, instance string, err error) {
	p := ToProblem(err)
	p.Instance = instance
	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
