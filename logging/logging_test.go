package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func linha(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log não é JSON: %v\n%s", err, buf.String())
	}
	return m
}

func TestNewJSONComServiceEEnv(t *testing.T) {
	var buf bytes.Buffer
	New(Options{Service: "expenses", Env: "dev", Output: &buf}).Info("gasto criado", "userId", "abc-123", "valorCentavos", 1500)

	m := linha(t, &buf)
	if m["service"] != "expenses" || m["env"] != "dev" || m["msg"] != "gasto criado" {
		t.Errorf("linha = %v", m)
	}
	if m["userId"] != "abc-123" || m["valorCentavos"] != float64(1500) {
		t.Errorf("atributos não sensíveis foram alterados: %v", m)
	}
}

func TestRedigeChavesSensiveis(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf})
	l.Info("login", "email", "joao@x.com", "Senha", "Abc12345", "access_token", "eyJ...", "Descricao", "farmácia do João")

	m := linha(t, &buf)
	for _, k := range []string{"email", "Senha", "access_token", "Descricao"} {
		if m[k] != Redacted {
			t.Errorf("%s = %v, esperava %s", k, m[k], Redacted)
		}
	}
	if strings.Contains(buf.String(), "joao") || strings.Contains(buf.String(), "Abc12345") {
		t.Errorf("PII vazou: %s", buf.String())
	}
}

func TestMascaraEmailEmTextoLivreMensagemEErro(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf})
	l.Error("falha ao notificar maria@empresa.com.br",
		"detalhe", "usuário ana.souza+teste@gmail.com não confirmado",
		"err", errors.New("cognito: user pedro@x.io not found"))

	out := buf.String()
	for _, pii := range []string{"maria@", "ana.souza", "pedro@"} {
		if strings.Contains(out, pii) {
			t.Errorf("%q vazou: %s", pii, out)
		}
	}
	if strings.Count(out, Redacted) != 3 {
		t.Errorf("esperava 3 máscaras: %s", out)
	}
}

func TestRedigeDentroDeGrupoEWith(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Output: &buf}).With("nome", "Fulano").WithGroup("req")
	l.Info("ok", slog.Group("user", "email", "a@b.com", "id", "u1"))

	out := buf.String()
	if strings.Contains(out, "Fulano") || strings.Contains(out, "a@b.com") {
		t.Errorf("PII vazou: %s", out)
	}
	if !strings.Contains(out, `"id":"u1"`) {
		t.Errorf("id deveria permanecer: %s", out)
	}
}

func TestNivel(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Level: "warn", Output: &buf})
	l.Info("não aparece")
	if buf.Len() != 0 {
		t.Fatalf("info com nível warn: %s", buf.String())
	}
	l.Warn("aparece")
	if buf.Len() == 0 {
		t.Fatal("warn deveria aparecer")
	}

	casos := map[string]slog.Level{"debug": slog.LevelDebug, "WARNING": slog.LevelWarn, " error ": slog.LevelError, "": slog.LevelInfo, "xyz": slog.LevelInfo}
	for in, want := range casos {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsSensitive(t *testing.T) {
	for _, k := range []string{"email", "E-mail", "refresh_token", "Authorization", "CPF"} {
		if !IsSensitive(k) {
			t.Errorf("%q deveria ser sensível", k)
		}
	}
	for _, k := range []string{"userId", "categoriaId", "status", "grupo"} {
		if IsSensitive(k) {
			t.Errorf("%q não deveria ser sensível", k)
		}
	}
}

func TestContexto(t *testing.T) {
	if FromContext(context.Background()) != slog.Default() {
		t.Error("sem logger deveria devolver o default")
	}
	l := New(Options{})
	if FromContext(WithLogger(context.Background(), l)) != l {
		t.Error("deveria devolver o logger guardado")
	}
}
