package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

// consumidor sobe um cmd/local fake que registra os UserDeleted recebidos.
// falhar faz o handler devolver erro.
type consumidor struct {
	mu       sync.Mutex
	recebido []Event
	userIDs  []string
	srv      *httptest.Server
}

func novoConsumidor(t *testing.T, falhar bool) *consumidor {
	t.Helper()
	c := &consumidor{}
	r := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	r.Handle(UserDeleted, Typed(func(_ context.Context, e Event, d UserDeletedDetail) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.recebido = append(c.recebido, e)
		c.userIDs = append(c.userIDs, d.UserID)
		if falhar {
			return errors.New("dynamodb indisponível")
		}
		return nil
	}))
	c.srv = httptest.NewServer(r.HTTPHandler())
	t.Cleanup(c.srv.Close)
	return c
}

func (c *consumidor) url() string { return c.srv.URL + Path }

func TestLocalFanOut(t *testing.T) {
	ctx := context.Background()
	a, b, d := novoConsumidor(t, false), novoConsumidor(t, false), novoConsumidor(t, false)
	p := NewLocal([]string{a.url(), b.url(), d.url()}, "gestor-gastos.users-service", nil, nil)
	fixo := time.Date(2026, 11, 9, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	p.now = func() time.Time { return fixo }

	if err := p.Publish(ctx, UserDeleted, UserDeletedDetail{UserID: "u-42"}); err != nil {
		t.Fatal(err)
	}

	var id string
	for i, c := range []*consumidor{a, b, d} {
		if len(c.recebido) != 1 || c.userIDs[0] != "u-42" {
			t.Fatalf("consumidor %d recebeu %d eventos, userIds %v", i, len(c.recebido), c.userIDs)
		}
		e := c.recebido[0]
		if e.DetailType != UserDeleted || e.Source != "gestor-gastos.users-service" || e.Region != "local" {
			t.Errorf("evento = %+v", e)
		}
		if !e.Time.Equal(fixo) || e.Time.Location() != time.UTC {
			t.Errorf("time = %v, quer %v em UTC", e.Time, fixo)
		}
		if i == 0 {
			id = e.ID
		} else if e.ID != id {
			t.Errorf("todos os consumidores devem receber o mesmo id: %q != %q", e.ID, id)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("id fora do formato UUID v4: %q", id)
	}
}

func TestLocalFalhaDeUmConsumidor(t *testing.T) {
	ctx := context.Background()
	ok1, ruim, ok2 := novoConsumidor(t, false), novoConsumidor(t, true), novoConsumidor(t, false)
	fora := "http://127.0.0.1:1" + Path
	p := NewLocal([]string{ok1.url(), ruim.url(), fora, ok2.url()}, "svc", nil, nil)

	err := p.Publish(ctx, UserDeleted, UserDeletedDetail{UserID: "u-1"})
	if err == nil {
		t.Fatal("esperava erro")
	}
	// Os consumidores saudáveis recebem mesmo com a falha dos outros.
	for _, c := range []*consumidor{ok1, ruim, ok2} {
		if len(c.recebido) != 1 {
			t.Errorf("consumidor %s recebeu %d eventos", c.url(), len(c.recebido))
		}
	}
	msg := err.Error()
	if !strings.Contains(msg, ruim.url()) || !strings.Contains(msg, "status 500") || !strings.Contains(msg, fora) {
		t.Errorf("o erro deve citar os dois consumidores que falharam: %v", err)
	}
	if strings.Contains(msg, ok1.url()) || strings.Contains(msg, ok2.url()) {
		t.Errorf("o erro cita consumidor que não falhou: %v", err)
	}
}

func TestLocalSemConsumidor(t *testing.T) {
	var logs bytes.Buffer
	p := NewLocal(nil, "svc", nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err := p.Publish(context.Background(), UserDeleted, UserDeletedDetail{UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "LOCAL_EVENT_TARGETS") {
		t.Errorf("esperava aviso no log: %s", logs.String())
	}
}

func TestLocalContextoCancelado(t *testing.T) {
	c := novoConsumidor(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewLocal([]string{c.url()}, "svc", nil, nil).Publish(ctx, UserDeleted, UserDeletedDetail{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("erro = %v", err)
	}
}

func TestLocalURLInvalida(t *testing.T) {
	if err := NewLocal([]string{"http://[::1"}, "svc", nil, nil).Publish(context.Background(), UserDeleted, UserDeletedDetail{}); err == nil {
		t.Error("esperava erro")
	}
}

func TestDetailInvalido(t *testing.T) {
	publishers := map[string]Publisher{
		"local":       NewLocal(nil, "svc", nil, nil),
		"eventbridge": NewEventBridge(&fakeEventBridge{}, "bus", "svc"),
	}
	casos := map[string]struct {
		tipo   string
		detail any
	}{
		"detail-type vazio": {"", UserDeletedDetail{}},
		"não serializa":     {UserDeleted, make(chan int)},
		"acima de 256 KB":   {UserDeleted, strings.Repeat("x", MaxDetailBytes)},
	}
	for pn, p := range publishers {
		for cn, c := range casos {
			t.Run(pn+"/"+cn, func(t *testing.T) {
				if err := p.Publish(context.Background(), c.tipo, c.detail); err == nil {
					t.Error("esperava erro")
				}
			})
		}
	}
}

type fakeEventBridge struct {
	in          *eventbridge.PutEventsInput
	err         error
	falhou      bool
	semEntradas bool
}

func (f *fakeEventBridge) PutEvents(_ context.Context, in *eventbridge.PutEventsInput, _ ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error) {
	f.in = in
	if f.err != nil {
		return nil, f.err
	}
	if f.falhou {
		out := &eventbridge.PutEventsOutput{FailedEntryCount: 1}
		if !f.semEntradas {
			out.Entries = []types.PutEventsResultEntry{{ErrorCode: aws.String("InternalFailure"), ErrorMessage: aws.String("tente de novo")}}
		}
		return out, nil
	}
	return &eventbridge.PutEventsOutput{Entries: []types.PutEventsResultEntry{{EventId: aws.String("1")}}}, nil
}

func TestEventBridge(t *testing.T) {
	ctx := context.Background()

	t.Run("monta a entrada", func(t *testing.T) {
		f := &fakeEventBridge{}
		p := NewEventBridge(f, "gestor-gastos-bus", "gestor-gastos.users-service")
		if err := p.Publish(ctx, UserConfirmed, UserConfirmedDetail{UserID: "u-1", Email: "a@b.c", Nome: "Ana"}); err != nil {
			t.Fatal(err)
		}
		if len(f.in.Entries) != 1 {
			t.Fatalf("entradas = %d", len(f.in.Entries))
		}
		e := f.in.Entries[0]
		if aws.ToString(e.EventBusName) != "gestor-gastos-bus" || aws.ToString(e.Source) != "gestor-gastos.users-service" || aws.ToString(e.DetailType) != UserConfirmed {
			t.Errorf("entrada = %+v", e)
		}
		var d UserConfirmedDetail
		if err := json.Unmarshal([]byte(aws.ToString(e.Detail)), &d); err != nil || d.UserID != "u-1" || d.Nome != "Ana" {
			t.Errorf("detail = %s (%v)", aws.ToString(e.Detail), err)
		}
	})

	casos := map[string]*fakeEventBridge{
		"erro da API":                {err: errors.New("AccessDeniedException")},
		"entrada rejeitada":          {falhou: true},
		"entrada rejeitada sem item": {falhou: true, semEntradas: true},
	}
	for nome, f := range casos {
		t.Run(nome, func(t *testing.T) {
			if err := NewEventBridge(f, "bus", "svc").Publish(ctx, UserDeleted, UserDeletedDetail{UserID: "u"}); err == nil {
				t.Error("esperava erro")
			}
		})
	}
}

func TestRouter(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	r := NewRouter(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	var recebido UserConfirmedDetail
	r.Handle(UserConfirmed, Typed(func(_ context.Context, _ Event, d UserConfirmedDetail) error {
		recebido = d
		return nil
	}))
	r.Handle(UserDeleted, func(context.Context, Event) error { panic("estourou") })
	r.Handle("Falha", func(context.Context, Event) error { return errors.New("boom") })

	t.Run("evento real do EventBridge", func(t *testing.T) {
		// Formato entregue pelo EventBridge à Lambda.
		raw := `{"version":"0","id":"6a7e8feb-b491-4cf7-a9f1-bf3703467718","detail-type":"UserConfirmed",
			"source":"gestor-gastos.auth","account":"111122223333","time":"2026-11-09T15:04:05Z",
			"region":"us-east-1","resources":[],"detail":{"userId":"u-9","email":"x@y.z","nome":"Bia"}}`
		var e Event
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		if err := r.LambdaHandler()(ctx, e); err != nil {
			t.Fatal(err)
		}
		if recebido.UserID != "u-9" || recebido.Nome != "Bia" {
			t.Errorf("detail = %+v", recebido)
		}
	})

	t.Run("tipo sem handler é ignorado", func(t *testing.T) {
		if err := r.Dispatch(ctx, Event{DetailType: "Outro", ID: "e1"}); err != nil {
			t.Error(err)
		}
		if !strings.Contains(logs.String(), "evento ignorado") {
			t.Error("esperava log de evento ignorado")
		}
	})

	t.Run("detail inválido", func(t *testing.T) {
		if err := r.Dispatch(ctx, Event{DetailType: UserConfirmed, Detail: json.RawMessage(`"texto"`)}); err == nil {
			t.Error("esperava erro")
		}
	})

	t.Run("panic vira erro", func(t *testing.T) {
		err := r.Dispatch(ctx, Event{DetailType: UserDeleted, ID: "e2"})
		if err == nil || !strings.Contains(err.Error(), "estourou") {
			t.Errorf("erro = %v", err)
		}
	})

	t.Run("erro é registrado com o id do evento", func(t *testing.T) {
		logs.Reset()
		if err := r.Dispatch(ctx, Event{DetailType: "Falha", ID: "e3"}); err == nil {
			t.Fatal("esperava erro")
		}
		if !strings.Contains(logs.String(), `"eventId":"e3"`) {
			t.Errorf("log = %s", logs.String())
		}
	})

	t.Run("registro inválido", func(t *testing.T) {
		for nome, fn := range map[string]func(){
			"repetido":    func() { r.Handle(UserConfirmed, func(context.Context, Event) error { return nil }) },
			"tipo vazio":  func() { r.Handle("", func(context.Context, Event) error { return nil }) },
			"handler nil": func() { r.Handle("X", nil) },
		} {
			t.Run(nome, func(t *testing.T) {
				defer func() {
					if recover() == nil {
						t.Error("esperava panic")
					}
				}()
				fn()
			})
		}
	})
}

func TestHTTPHandler(t *testing.T) {
	r := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	r.Handle(UserDeleted, func(context.Context, Event) error { return nil })
	r.Handle("Falha", func(context.Context, Event) error { return errors.New("boom") })
	h := r.HTTPHandler()

	casos := []struct {
		nome   string
		method string
		body   string
		status int
	}{
		{"processado", http.MethodPost, `{"id":"1","detail-type":"UserDeleted","detail":{}}`, http.StatusNoContent},
		{"ignorado", http.MethodPost, `{"id":"1","detail-type":"Outro","detail":{}}`, http.StatusNoContent},
		{"handler falhou", http.MethodPost, `{"id":"1","detail-type":"Falha","detail":{}}`, http.StatusInternalServerError},
		{"JSON inválido", http.MethodPost, `não é json`, http.StatusBadRequest},
		{"sem detail-type", http.MethodPost, `{"id":"1"}`, http.StatusBadRequest},
		{"método errado", http.MethodGet, ``, http.StatusMethodNotAllowed},
	}
	for _, tt := range casos {
		t.Run(tt.nome, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, Path, strings.NewReader(tt.body)))
			if rec.Code != tt.status {
				t.Errorf("status = %d, quer %d", rec.Code, tt.status)
			}
		})
	}

	t.Run("corpo ilegível", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, Path, erroLeitura{}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

type erroLeitura struct{}

func (erroLeitura) Read([]byte) (int, error) { return 0, errors.New("conexão caiu") }

func TestParseTargets(t *testing.T) {
	casos := []struct {
		in   string
		quer []string
		erro bool
	}{
		{"", nil, false},
		{"  ", nil, false},
		{"http://localhost:8082/events", []string{"http://localhost:8082/events"}, false},
		{" http://a:1/events , ,https://b/events ", []string{"http://a:1/events", "https://b/events"}, false},
		{"localhost:8082/events", nil, true},
		{"ftp://a/events", nil, true},
		{"http:///events", nil, true},
		{"http://a/events,%zz", nil, true},
	}
	for _, tt := range casos {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTargets(tt.in)
			if (err != nil) != tt.erro || strings.Join(got, "|") != strings.Join(tt.quer, "|") {
				t.Errorf("ParseTargets(%q) = %v, %v", tt.in, got, err)
			}
		})
	}
}

func TestNewPublisher(t *testing.T) {
	casos := []struct {
		nome string
		o    Options
		quer string // tipo esperado; vazio = erro
	}{
		{"aws", Options{Runtime: config.AWS, Source: "s", BusName: "bus", API: &fakeEventBridge{}}, "eventbridge"},
		{"local", Options{Runtime: config.Local, Source: "s", LocalTargets: "http://localhost:8082/events"}, "local"},
		{"local sem consumidores", Options{Runtime: config.Local, Source: "s"}, "local"},
		{"aws sem barramento", Options{Runtime: config.AWS, Source: "s", API: &fakeEventBridge{}}, ""},
		{"aws sem client", Options{Runtime: config.AWS, Source: "s", BusName: "bus"}, ""},
		{"local com URL inválida", Options{Runtime: config.Local, Source: "s", LocalTargets: "nada"}, ""},
		{"source vazio", Options{Runtime: config.Local}, ""},
		{"runtime inválido", Options{Runtime: "gcp", Source: "s"}, ""},
	}
	for _, tt := range casos {
		t.Run(tt.nome, func(t *testing.T) {
			p, err := NewPublisher(tt.o)
			var got string
			switch p.(type) {
			case *EventBridgePublisher:
				got = "eventbridge"
			case *LocalPublisher:
				got = "local"
			}
			if got != tt.quer || (tt.quer == "") != (err != nil) {
				t.Errorf("publisher = %q, erro = %v", got, err)
			}
		})
	}
}
