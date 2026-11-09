package invoke

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/errs"
)

// HandlerFunc trata uma operação. O valor devolvido vira o payload da resposta
// (nil devolve sem payload); o erro vira errs.Problem.
type HandlerFunc func(ctx context.Context, req Request) (any, error)

// Typed adapta uma função com entrada e saída tipadas. Um payload que não
// decodifica em In vira 400 "payload_invalido"; payload ausente deixa In com
// valor zero.
func Typed[In, Out any](fn func(ctx context.Context, userID string, in In) (Out, error)) HandlerFunc {
	return func(ctx context.Context, req Request) (any, error) {
		var in In
		if len(req.Payload) > 0 {
			if err := json.Unmarshal(req.Payload, &in); err != nil {
				return nil, errs.BadRequest("payload_invalido", "Payload inválido para a operação.").WithCause(err)
			}
		}
		return fn(ctx, req.UserID, in)
	}
}

// Router despacha o envelope para o handler da operação. O mesmo Router serve
// a Lambda (LambdaHandler) e o cmd/local (HTTPHandler).
type Router struct {
	handlers map[string]HandlerFunc
	log      *slog.Logger
}

// NewRouter cria um Router. log recebe as falhas internas com a causa, que
// nunca vai na resposta; nil usa slog.Default().
func NewRouter(log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{handlers: map[string]HandlerFunc{}, log: log}
}

// Handle registra o handler da operação. Nome vazio ou repetido é erro de
// programação e causa panic na inicialização.
func (r *Router) Handle(op string, h HandlerFunc) {
	if op == "" || h == nil {
		panic("invoke: operação e handler são obrigatórios")
	}
	if _, ok := r.handlers[op]; ok {
		panic(fmt.Sprintf("invoke: operação %q registrada duas vezes", op))
	}
	r.handlers[op] = h
}

// Dispatch executa a operação e sempre devolve um envelope: erros e panics
// viram Response.Error.
func (r *Router) Dispatch(ctx context.Context, req Request) (resp Response) {
	h, ok := r.handlers[req.Operation]
	if !ok {
		return errorResponse(errs.BadRequest("operacao_desconhecida", fmt.Sprintf("Operação %q não existe.", req.Operation)))
	}

	defer func() {
		if p := recover(); p != nil {
			err := errs.Internal(fmt.Errorf("panic: %v", p))
			r.logInternal(ctx, req.Operation, err)
			resp = errorResponse(err)
		}
	}()

	out, err := h(ctx, req)
	if err != nil {
		if errs.KindOf(err) == errs.KindInternal {
			r.logInternal(ctx, req.Operation, err)
		}
		return errorResponse(err)
	}
	if out == nil {
		return Response{}
	}
	b, err := json.Marshal(out)
	if err != nil {
		err = errs.Internal(fmt.Errorf("serializar resposta: %w", err))
		r.logInternal(ctx, req.Operation, err)
		return errorResponse(err)
	}
	return Response{Payload: b}
}

// LambdaHandler devolve a função para lambda.Start. Erros de domínio voltam
// dentro do envelope, então a função nunca falha por eles.
func (r *Router) LambdaHandler() func(context.Context, Request) (Response, error) {
	return func(ctx context.Context, req Request) (Response, error) {
		return r.Dispatch(ctx, req), nil
	}
}

// HTTPHandler devolve o handler do cmd/local, que atende POST em Path.
func (r *Router) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, func(w http.ResponseWriter, hr *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(hr.Body, MaxPayloadBytes+1))
		if err != nil || len(raw) > MaxPayloadBytes {
			http.Error(w, "envelope ilegível ou grande demais", http.StatusRequestEntityTooLarge)
			return
		}
		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			http.Error(w, "envelope inválido", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r.Dispatch(hr.Context(), req))
	})
	return mux
}

func (r *Router) logInternal(ctx context.Context, op string, err error) {
	r.log.ErrorContext(ctx, "falha interna na operação", "operation", op, "error", err)
}

func errorResponse(err error) Response {
	p := errs.ToProblem(err)
	return Response{Error: &p}
}
