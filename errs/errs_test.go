package errs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConstrutoresEStatus(t *testing.T) {
	tests := []struct {
		err    *Error
		status int
		title  string
	}{
		{BadRequest("cursor_invalido", "Cursor inválido"), 400, "Requisição inválida"},
		{Unauthorized("token_invalido", "Faça login"), 401, "Não autenticado"},
		{Forbidden("somente_admin", "Só admin"), 403, "Acesso negado"},
		{NotFound("gasto_nao_encontrado", "Gasto não encontrado"), 404, "Não encontrado"},
		{Conflict("categoria_em_uso", "Categoria em uso"), 409, "Conflito"},
		{Validation("Dados inválidos", FieldError{"valor", "deve ser maior que zero"}), 422, "Dados inválidos"},
		{Internal(errors.New("boom")), 500, "Erro interno"},
	}
	for _, tt := range tests {
		t.Run(tt.err.Code, func(t *testing.T) {
			p := ToProblem(tt.err)
			if p.Status != tt.status || p.Title != tt.title || p.Code != tt.err.Code {
				t.Errorf("problem = %+v", p)
			}
			if p.Type != TypePrefix+tt.err.Code {
				t.Errorf("type = %q", p.Type)
			}
		})
	}
}

func TestErroDesconhecidoNaoVaza(t *testing.T) {
	p := ToProblem(fmt.Errorf("dynamodb: senha=hunter2 conexão recusada"))
	if p.Status != 500 || strings.Contains(p.Detail, "hunter2") || p.Code != "erro_interno" {
		t.Fatalf("problem vazou ou status errado: %+v", p)
	}
}

func TestToProblemNil(t *testing.T) {
	if p := ToProblem(nil); p.Status != 500 {
		t.Fatalf("status = %d", p.Status)
	}
	if As(nil) != nil {
		t.Fatal("As(nil) deveria ser nil")
	}
}

func TestCausaInternaFicaSóNoError(t *testing.T) {
	causa := errors.New("ConditionalCheckFailed")
	e := Conflict("nome_duplicado", "Já existe").WithCause(causa)
	if !errors.Is(e, causa) {
		t.Error("Unwrap deveria expor a causa")
	}
	if !strings.Contains(e.Error(), "ConditionalCheckFailed") {
		t.Errorf("Error() = %q", e.Error())
	}
	if strings.Contains(ToProblem(e).Detail, "ConditionalCheckFailed") {
		t.Error("a causa não pode ir para o problem")
	}
	if Forbidden("x", "y").Error() != "x: y" {
		t.Error("Error() sem causa")
	}
}

func TestIsEAsAtravésDeWrap(t *testing.T) {
	err := fmt.Errorf("store: %w", NotFound("gasto_nao_encontrado", "não achei"))
	if !errors.Is(err, NotFound("gasto_nao_encontrado", "")) {
		t.Error("errors.Is por Kind+Code falhou")
	}
	if errors.Is(err, NotFound("outro", "")) {
		t.Error("Code diferente não deveria casar")
	}
	if KindOf(err) != KindNotFound {
		t.Errorf("KindOf = %v", KindOf(err))
	}
	if KindOf(errors.New("x")) != KindInternal {
		t.Error("erro comum deveria ser Internal")
	}
}

func TestIdaEVoltaPeloProblem(t *testing.T) {
	orig := Validation("Dados inválidos", FieldError{"data", "formato AAAA-MM-DD"})
	b, _ := json.Marshal(ToProblem(orig))
	var p Problem
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	got := FromProblem(p)
	if got.Kind != KindValidation || got.Code != "validacao" || len(got.Fields) != 1 || got.Fields[0].Field != "data" {
		t.Fatalf("reconstruído = %+v", got)
	}
	if FromProblem(Problem{Status: 418}).Kind != KindBadRequest {
		t.Error("4xx desconhecido deveria virar BadRequest")
	}
	if FromProblem(Problem{Status: 503}).Kind != KindInternal {
		t.Error("5xx desconhecido deveria virar Internal")
	}
}

func TestWrite(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, "/gastos/123", NotFound("gasto_nao_encontrado", "Gasto não encontrado"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != ContentType {
		t.Errorf("content-type = %q", ct)
	}
	var p Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Instance != "/gastos/123" || p.Detail != "Gasto não encontrado" {
		t.Errorf("corpo = %+v", p)
	}
}

func TestKindOfNil(t *testing.T) {
	// Antes da v0.1.1, KindOf(nil) entrava em panic.
	if k := KindOf(nil); k != KindNone {
		t.Errorf("KindOf(nil) = %v, quer KindNone", k)
	}
	if KindOf(nil) == KindInternal {
		t.Error("nil não pode ser classificado como erro interno")
	}
	if KindNone.Status() != 0 {
		t.Errorf("KindNone.Status() = %d, quer 0", KindNone.Status())
	}
}
