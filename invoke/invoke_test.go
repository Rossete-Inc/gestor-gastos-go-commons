package invoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/config"
	"github.com/Rossete-Inc/gestor-gastos-go-commons/errs"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
)

type somaIn struct{ A, B int64 }
type somaOut struct{ Total int64 }

// novoRouter monta o serviço fake usado pelos dois transportes. logs recebe
// o que o Router registra.
func novoRouter(logs io.Writer) *Router {
	r := NewRouter(slog.New(slog.NewJSONHandler(logs, nil)))
	r.Handle("Somar", Typed(func(_ context.Context, _ string, in somaIn) (somaOut, error) {
		return somaOut{Total: in.A + in.B}, nil
	}))
	r.Handle("QuemSou", Typed(func(_ context.Context, userID string, _ struct{}) (string, error) {
		return userID, nil
	}))
	r.Handle("Excluir", func(context.Context, Request) (any, error) { return nil, nil })
	r.Handle("Validar", func(context.Context, Request) (any, error) {
		return nil, errs.Validation("Dados inválidos", errs.FieldError{Field: "valorCentavos", Message: "deve ser maior que zero"})
	})
	r.Handle("CategoriaEmUso", func(context.Context, Request) (any, error) {
		return nil, errs.Conflict("categoria_em_uso", "Categoria em uso.")
	})
	r.Handle("Quebrar", func(context.Context, Request) (any, error) {
		return nil, errors.New("dynamodb: segredo-da-tabela")
	})
	r.Handle("Panico", func(context.Context, Request) (any, error) { panic("estourou") })
	r.Handle("NaoSerializa", func(context.Context, Request) (any, error) { return make(chan int), nil })
	return r
}

// fakeLambda imita o runtime da Lambda: decodifica o payload no tipo de
// entrada do handler, chama o handler e serializa a saída.
type fakeLambda struct {
	handler  func(context.Context, Request) (Response, error)
	err      error  // erro da API (permissão, throttling)
	funcErr  string // simula FunctionError
	raw      []byte // sobrescreve o payload de resposta
	function string // última função chamada
}

func (f *fakeLambda) Invoke(ctx context.Context, in *lambda.InvokeInput, _ ...func(*lambda.Options)) (*lambda.InvokeOutput, error) {
	f.function = aws.ToString(in.FunctionName)
	if f.err != nil {
		return nil, f.err
	}
	if f.funcErr != "" {
		return &lambda.InvokeOutput{FunctionError: aws.String(f.funcErr), Payload: []byte(`{"errorMessage":"Task timed out"}`)}, nil
	}
	if f.raw != nil {
		return &lambda.InvokeOutput{Payload: f.raw}, nil
	}
	var req Request
	if err := json.Unmarshal(in.Payload, &req); err != nil {
		return nil, err
	}
	resp, err := f.handler(ctx, req)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(resp)
	return &lambda.InvokeOutput{Payload: b}, err
}

// transportes devolve um Client por transporte, todos sobre o mesmo Router.
func transportes(t *testing.T, logs io.Writer) map[string]*Client {
	t.Helper()
	r := novoRouter(logs)
	srv := httptest.NewServer(r.HTTPHandler())
	t.Cleanup(srv.Close)
	return map[string]*Client{
		"http":   NewClient(NewHTTP(srv.URL+"/", nil)),
		"lambda": NewClient(NewLambda(&fakeLambda{handler: r.LambdaHandler()}, "arn:aws:lambda:us-east-1:1:function:svc:live")),
	}
}

func TestContratoNosDoisTransportes(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	for nome, c := range transportes(t, &logs) {
		t.Run(nome, func(t *testing.T) {
			t.Run("sucesso com payload tipado", func(t *testing.T) {
				var out somaOut
				if err := c.Call(ctx, "Somar", "u1", somaIn{A: 1999, B: 1}, &out); err != nil {
					t.Fatal(err)
				}
				if out.Total != 2000 {
					t.Errorf("total = %d", out.Total)
				}
			})

			t.Run("userId chega ao handler", func(t *testing.T) {
				var out string
				if err := c.Call(ctx, "QuemSou", "user-123", nil, &out); err != nil {
					t.Fatal(err)
				}
				if out != "user-123" {
					t.Errorf("userId = %q", out)
				}
			})

			t.Run("sem payload de resposta", func(t *testing.T) {
				var out somaOut
				if err := c.Call(ctx, "Excluir", "u1", nil, &out); err != nil {
					t.Fatal(err)
				}
				if err := c.Call(ctx, "Somar", "u1", somaIn{A: 1}, nil); err != nil {
					t.Fatal(err)
				}
			})

			t.Run("erro de validação preserva tipo e campos", func(t *testing.T) {
				err := c.Call(ctx, "Validar", "u1", nil, nil)
				e := errs.As(err)
				if e.Kind != errs.KindValidation || e.Code != "validacao" {
					t.Fatalf("erro = %+v", e)
				}
				if len(e.Fields) != 1 || e.Fields[0].Field != "valorCentavos" {
					t.Errorf("fields = %+v", e.Fields)
				}
			})

			t.Run("erro de domínio comparável com errors.Is", func(t *testing.T) {
				err := c.Call(ctx, "CategoriaEmUso", "u1", nil, nil)
				if !errors.Is(err, errs.Conflict("categoria_em_uso", "")) {
					t.Errorf("erro = %v", err)
				}
			})

			t.Run("operação desconhecida", func(t *testing.T) {
				e := errs.As(c.Call(ctx, "NaoExiste", "u1", nil, nil))
				if e.Kind != errs.KindBadRequest || e.Code != "operacao_desconhecida" {
					t.Errorf("erro = %+v", e)
				}
			})

			t.Run("payload que não decodifica", func(t *testing.T) {
				e := errs.As(c.Call(ctx, "Somar", "u1", map[string]string{"A": "dez"}, nil))
				if e.Kind != errs.KindBadRequest || e.Code != "payload_invalido" {
					t.Errorf("erro = %+v", e)
				}
			})

			for _, op := range []string{"Quebrar", "Panico", "NaoSerializa"} {
				t.Run("falha interna em "+op+" não vaza a causa", func(t *testing.T) {
					logs.Reset()
					err := c.Call(ctx, op, "u1", nil, nil)
					e := errs.As(err)
					if e.Kind != errs.KindInternal || e.Code != "erro_interno" {
						t.Fatalf("erro = %+v", e)
					}
					if strings.Contains(err.Error(), "segredo") || strings.Contains(err.Error(), "estourou") {
						t.Errorf("causa vazou para o cliente: %v", err)
					}
					if !strings.Contains(logs.String(), op) {
						t.Errorf("falha não foi registrada no log do serviço: %s", logs.String())
					}
				})
			}

			t.Run("resposta que não cabe em out", func(t *testing.T) {
				var out int
				if errs.KindOf(c.Call(ctx, "Somar", "u1", somaIn{}, &out)) != errs.KindInternal {
					t.Error("esperava erro interno")
				}
			})

			t.Run("payload que não serializa", func(t *testing.T) {
				if errs.KindOf(c.Call(ctx, "Somar", "u1", make(chan int), nil)) != errs.KindInternal {
					t.Error("esperava erro interno")
				}
			})
		})
	}
}

func TestLambdaTransport(t *testing.T) {
	ctx := context.Background()
	r := novoRouter(io.Discard)

	t.Run("chama a função configurada", func(t *testing.T) {
		f := &fakeLambda{handler: r.LambdaHandler()}
		if err := NewClient(NewLambda(f, "svc:live")).Call(ctx, "Somar", "u1", somaIn{}, nil); err != nil {
			t.Fatal(err)
		}
		if f.function != "svc:live" {
			t.Errorf("função = %q", f.function)
		}
	})

	casos := map[string]*fakeLambda{
		"erro da API":               {err: errors.New("AccessDeniedException")},
		"FunctionError":             {funcErr: "Unhandled"},
		"resposta fora do envelope": {raw: []byte("não é json")},
	}
	for nome, f := range casos {
		t.Run(nome, func(t *testing.T) {
			err := NewClient(NewLambda(f, "svc")).Call(ctx, "Somar", "u1", nil, nil)
			if errs.KindOf(err) != errs.KindInternal {
				t.Errorf("erro = %v", err)
			}
		})
	}

	t.Run("envelope acima do limite não é enviado", func(t *testing.T) {
		f := &fakeLambda{handler: r.LambdaHandler()}
		grande := strings.Repeat("x", MaxPayloadBytes)
		if errs.KindOf(NewClient(NewLambda(f, "svc")).Call(ctx, "Somar", "u1", grande, nil)) != errs.KindInternal {
			t.Error("esperava erro interno")
		}
		if f.function != "" {
			t.Error("a função não deveria ter sido chamada")
		}
	})
}

func TestHTTPTransport(t *testing.T) {
	ctx := context.Background()

	resposta := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	casos := map[string]string{
		"status diferente de 200":   resposta(http.StatusBadGateway, "bad gateway").URL,
		"resposta fora do envelope": resposta(http.StatusOK, "<html>").URL,
		"serviço fora do ar":        "http://127.0.0.1:1",
	}
	for nome, url := range casos {
		t.Run(nome, func(t *testing.T) {
			if errs.KindOf(NewClient(NewHTTP(url, nil)).Call(ctx, "Somar", "u1", nil, nil)) != errs.KindInternal {
				t.Error("esperava erro interno")
			}
		})
	}

	t.Run("contexto cancelado", func(t *testing.T) {
		srv := httptest.NewServer(novoRouter(io.Discard).HTTPHandler())
		t.Cleanup(srv.Close)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		err := NewClient(NewHTTP(srv.URL, nil)).Call(cctx, "Somar", "u1", nil, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("erro = %v", err)
		}
	})

	t.Run("resposta acima do limite", func(t *testing.T) {
		srv := resposta(http.StatusOK, `{"payload":"`+strings.Repeat("x", MaxPayloadBytes)+`"}`)
		if errs.KindOf(NewClient(NewHTTP(srv.URL, nil)).Call(ctx, "Somar", "u1", nil, nil)) != errs.KindInternal {
			t.Error("esperava erro interno")
		}
	})

	t.Run("envelope acima do limite não é enviado", func(t *testing.T) {
		chamou := false
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { chamou = true }))
		t.Cleanup(srv.Close)
		grande := strings.Repeat("x", MaxPayloadBytes)
		if errs.KindOf(NewClient(NewHTTP(srv.URL, nil)).Call(ctx, "Somar", "u1", grande, nil)) != errs.KindInternal {
			t.Error("esperava erro interno")
		}
		if chamou {
			t.Error("o servidor não deveria ter sido chamado")
		}
	})

	t.Run("URL inválida", func(t *testing.T) {
		if errs.KindOf(NewClient(NewHTTP("://sem-esquema", nil)).Call(ctx, "Somar", "u1", nil, nil)) != errs.KindInternal {
			t.Error("esperava erro interno")
		}
	})
}

func TestHTTPHandler(t *testing.T) {
	h := novoRouter(io.Discard).HTTPHandler()
	casos := []struct {
		nome   string
		method string
		path   string
		body   string
		status int
	}{
		{"envelope válido", http.MethodPost, Path, `{"operation":"Somar","payload":{"A":1,"B":2}}`, http.StatusOK},
		{"erro de domínio também é 200", http.MethodPost, Path, `{"operation":"Validar"}`, http.StatusOK},
		{"método errado", http.MethodGet, Path, "", http.StatusMethodNotAllowed},
		{"rota errada", http.MethodPost, "/outra", "{}", http.StatusNotFound},
		{"envelope inválido", http.MethodPost, Path, "não é json", http.StatusBadRequest},
		{"envelope grande demais", http.MethodPost, Path, strings.Repeat("x", MaxPayloadBytes+1), http.StatusRequestEntityTooLarge},
	}
	for _, tt := range casos {
		t.Run(tt.nome, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body)))
			if rec.Code != tt.status {
				t.Errorf("status = %d, quer %d", rec.Code, tt.status)
			}
		})
	}
}

func TestRouterHandle(t *testing.T) {
	deveEntrarEmPanico := func(t *testing.T, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Error("esperava panic")
			}
		}()
		fn()
	}
	r := NewRouter(nil)
	r.Handle("Op", func(context.Context, Request) (any, error) { return nil, nil })
	t.Run("operação repetida", func(t *testing.T) {
		deveEntrarEmPanico(t, func() { r.Handle("Op", func(context.Context, Request) (any, error) { return nil, nil }) })
	})
	t.Run("operação vazia", func(t *testing.T) {
		deveEntrarEmPanico(t, func() { r.Handle("", func(context.Context, Request) (any, error) { return nil, nil }) })
	})
	t.Run("handler nil", func(t *testing.T) {
		deveEntrarEmPanico(t, func() { r.Handle("Outra", nil) })
	})
}

func TestNewTransport(t *testing.T) {
	api := &fakeLambda{}
	casos := []struct {
		nome   string
		rt     config.Runtime
		target string
		api    LambdaAPI
		quer   string // tipo esperado; vazio = erro
	}{
		{"aws usa Lambda", config.AWS, "arn:svc:live", api, "lambda"},
		{"local usa HTTP", config.Local, "http://localhost:8081", nil, "http"},
		{"aws sem client", config.AWS, "arn:svc", nil, ""},
		{"destino vazio", config.Local, "", nil, ""},
		{"runtime inválido", config.Runtime("gcp"), "x", api, ""},
	}
	for _, tt := range casos {
		t.Run(tt.nome, func(t *testing.T) {
			tr, err := NewTransport(tt.rt, tt.target, tt.api, nil)
			var got string
			switch tr.(type) {
			case *LambdaTransport:
				got = "lambda"
			case *HTTPTransport:
				got = "http"
			}
			if got != tt.quer || (tt.quer == "") != (err != nil) {
				t.Errorf("transporte = %q, erro = %v", got, err)
			}
		})
	}
}
