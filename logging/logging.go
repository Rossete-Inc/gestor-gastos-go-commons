// Package logging cria o logger JSON (slog) padrão dos serviços, sem dados
// pessoais.
//
// Dois mecanismos impedem PII no log:
//   - atributos com nome sensível (email, nome, senha, token, descricao...)
//     têm o valor trocado por "[REDACTED]";
//   - e-mails em qualquer texto, inclusive na mensagem, são mascarados.
//
// Identifique o usuário só pelo userId (sub do Cognito), nunca pelo e-mail.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// Redacted substitui valores sensíveis.
const Redacted = "[REDACTED]"

// sensitiveKeys são comparadas sem diferenciar maiúsculas, ignorando "_" e "-".
var sensitiveKeys = map[string]bool{
	"email": true, "nome": true, "name": true, "username": true,
	"password": true, "senha": true, "token": true, "accesstoken": true,
	"idtoken": true, "refreshtoken": true, "authorization": true, "cookie": true,
	"descricao": true, "description": true, "codigo": true, "code": true,
	"secret": true, "telefone": true, "phone": true, "cpf": true,
}

var emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// Options configura o logger.
type Options struct {
	Service string
	Env     string
	Level   string    // debug, info, warn ou error; padrão info
	Output  io.Writer // padrão os.Stdout
}

// New cria um logger JSON com service e env em toda linha.
func New(o Options) *slog.Logger {
	out := o.Output
	if out == nil {
		out = os.Stdout
	}
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:       ParseLevel(o.Level),
		ReplaceAttr: redact,
	})
	return slog.New(&maskHandler{Handler: h}).With("service", o.Service, "env", o.Env)
}

// ParseLevel converte o texto do LOG_LEVEL; valores desconhecidos viram info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// IsSensitive informa se um atributo com esse nome é tratado como PII.
func IsSensitive(key string) bool {
	k := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	return sensitiveKeys[k]
}

// MaskEmails troca qualquer e-mail no texto por [REDACTED].
func MaskEmails(s string) string { return emailRe.ReplaceAllString(s, Redacted) }

func redact(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.MessageKey {
		return a
	}
	if IsSensitive(a.Key) && a.Value.Kind() != slog.KindGroup {
		return slog.String(a.Key, Redacted)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, MaskEmails(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, MaskEmails(err.Error()))
		}
	}
	return a
}

// maskHandler mascara e-mails na mensagem, que ReplaceAttr não alcança.
type maskHandler struct{ slog.Handler }

func (h *maskHandler) Handle(ctx context.Context, r slog.Record) error {
	if emailRe.MatchString(r.Message) {
		nr := slog.NewRecord(r.Time, r.Level, MaskEmails(r.Message), r.PC)
		r.Attrs(func(a slog.Attr) bool { nr.AddAttrs(a); return true })
		r = nr
	}
	return h.Handler.Handle(ctx, r)
}

func (h *maskHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &maskHandler{Handler: h.Handler.WithAttrs(as)}
}

func (h *maskHandler) WithGroup(name string) slog.Handler {
	return &maskHandler{Handler: h.Handler.WithGroup(name)}
}

type ctxKey struct{}

// WithLogger guarda o logger no contexto (por exemplo, já com requestId).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext devolve o logger do contexto, ou slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
