package events

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// Path é a rota em que o cmd/local de cada consumidor recebe eventos.
const Path = "/events"

// HandlerFunc trata um evento. Erro faz a AWS tentar de novo e, esgotadas as
// tentativas, mandar o evento para a DLQ; por isso o handler precisa ser
// idempotente.
type HandlerFunc func(ctx context.Context, e Event) error

// Typed adapta um handler que recebe o detail já decodificado em T.
func Typed[T any](fn func(ctx context.Context, e Event, detail T) error) HandlerFunc {
	return func(ctx context.Context, e Event) error {
		var d T
		if err := json.Unmarshal(e.Detail, &d); err != nil {
			return fmt.Errorf("detail de %s inválido: %w", e.DetailType, err)
		}
		return fn(ctx, e, d)
	}
}

// Router despacha o evento pelo detail-type. O mesmo Router serve a Lambda
// (LambdaHandler) e o cmd/local (HTTPHandler).
type Router struct {
	handlers map[string]HandlerFunc
	log      *slog.Logger
}

// NewRouter cria um Router. log nil usa slog.Default().
func NewRouter(log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{handlers: map[string]HandlerFunc{}, log: log}
}

// Handle registra o handler do tipo de evento. Tipo vazio ou repetido é erro
// de programação e causa panic na inicialização.
func (r *Router) Handle(detailType string, h HandlerFunc) {
	if detailType == "" || h == nil {
		panic("events: tipo de evento e handler são obrigatórios")
	}
	if _, ok := r.handlers[detailType]; ok {
		panic(fmt.Sprintf("events: tipo %q registrado duas vezes", detailType))
	}
	r.handlers[detailType] = h
}

// Dispatch executa o handler do evento. Um tipo sem handler é ignorado, como
// um evento que não casa com nenhuma regra do EventBridge. Panic vira erro.
func (r *Router) Dispatch(ctx context.Context, e Event) (err error) {
	h, ok := r.handlers[e.DetailType]
	if !ok {
		r.log.DebugContext(ctx, "evento ignorado", "detailType", e.DetailType, "eventId", e.ID)
		return nil
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
		if err != nil {
			r.log.ErrorContext(ctx, "falha ao processar evento", "detailType", e.DetailType, "eventId", e.ID, "error", err)
		}
	}()
	return h(ctx, e)
}

// LambdaHandler devolve a função para lambda.Start.
func (r *Router) LambdaHandler() func(context.Context, Event) error {
	return r.Dispatch
}

// HTTPHandler devolve o handler do cmd/local, que atende POST em Path:
// 204 quando processou ou ignorou, 400 para envelope inválido e 500 quando o
// handler falhou.
func (r *Router) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, func(w http.ResponseWriter, hr *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(hr.Body, MaxDetailBytes*2))
		if err != nil {
			http.Error(w, "evento ilegível", http.StatusBadRequest)
			return
		}
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil || e.DetailType == "" {
			http.Error(w, "evento inválido", http.StatusBadRequest)
			return
		}
		if err := r.Dispatch(hr.Context(), e); err != nil {
			http.Error(w, "falha ao processar o evento "+e.ID, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
