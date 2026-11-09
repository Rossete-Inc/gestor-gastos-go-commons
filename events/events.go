// Package events publica e consome os eventos assíncronos do Gestor de Gastos.
//
// Na AWS o publisher faz PutEvents no barramento do EventBridge, e cada
// serviço consome por uma regra que aponta para a própria Lambda. Localmente
// o publisher faz POST do mesmo envelope a cada consumidor listado em
// LOCAL_EVENT_TARGETS, sem retentativa nem DLQ.
//
// O consumidor recebe Event no formato que o EventBridge entrega à Lambda, e
// o Router atende os dois caminhos com o mesmo handler. Como na AWS a entrega
// é "pelo menos uma vez", todo handler precisa ser idempotente.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/config"
)

// Tipos de evento (detail-type) do MVP.
const (
	// UserConfirmed é publicado pelo trigger Post Confirmation do Cognito e
	// consumido pelo users-service, que cria o perfil.
	UserConfirmed = "UserConfirmed"
	// UserDeleted é publicado pelo users-service e consumido pelos serviços
	// que guardam dados do usuário.
	UserDeleted = "UserDeleted"
)

// UserConfirmedDetail é o detail de UserConfirmed. Email e Nome são dados
// pessoais: nunca vão para o log.
type UserConfirmedDetail struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
	Nome   string `json:"nome"`
}

// UserDeletedDetail é o detail de UserDeleted.
type UserDeletedDetail struct {
	UserID string `json:"userId"`
}

// MaxDetailBytes é o limite de uma entrada do PutEvents. O publisher local
// aplica o mesmo limite para o local se comportar igual.
const MaxDetailBytes = 256 << 10

// Event é o envelope que o consumidor recebe, com os campos que o
// EventBridge entrega à Lambda.
type Event struct {
	Version    string          `json:"version,omitempty"`
	ID         string          `json:"id"`
	DetailType string          `json:"detail-type"`
	Source     string          `json:"source"`
	Account    string          `json:"account,omitempty"`
	Time       time.Time       `json:"time"`
	Region     string          `json:"region,omitempty"`
	Resources  []string        `json:"resources,omitempty"`
	Detail     json.RawMessage `json:"detail"`
}

// Publisher publica um evento. detail é serializado em JSON.
type Publisher interface {
	Publish(ctx context.Context, detailType string, detail any) error
}

// Options configura NewPublisher.
type Options struct {
	Runtime config.Runtime
	Source  string // quem publica, por exemplo "gestor-gastos.users-service"

	// RUNTIME=aws
	BusName string // nome do barramento, lido do SSM
	API     EventBridgeAPI

	// RUNTIME=local
	LocalTargets string       // valor de LOCAL_EVENT_TARGETS
	HTTPClient   *http.Client // nil usa timeout de 30 s
	Log          *slog.Logger // nil usa slog.Default()
}

// NewPublisher escolhe o publisher pelo runtime.
func NewPublisher(o Options) (Publisher, error) {
	if o.Source == "" {
		return nil, errors.New("events: source vazio")
	}
	switch o.Runtime {
	case config.AWS:
		if o.BusName == "" || o.API == nil {
			return nil, errors.New("events: barramento e client EventBridge são obrigatórios com RUNTIME=aws")
		}
		return NewEventBridge(o.API, o.BusName, o.Source), nil
	case config.Local:
		targets, err := ParseTargets(o.LocalTargets)
		if err != nil {
			return nil, err
		}
		return NewLocal(targets, o.Source, o.HTTPClient, o.Log), nil
	default:
		return nil, fmt.Errorf("events: runtime %q inválido", o.Runtime)
	}
}

// ParseTargets lê LOCAL_EVENT_TARGETS: URLs separadas por vírgula, cada uma
// apontando para o endpoint de eventos do cmd/local de um consumidor, por
// exemplo "http://localhost:8082/events,http://localhost:8083/events".
// Valor vazio é válido e significa nenhum consumidor.
func ParseTargets(s string) ([]string, error) {
	var targets []string
	for _, raw := range strings.Split(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("events: LOCAL_EVENT_TARGETS tem URL inválida %q", raw)
		}
		targets = append(targets, raw)
	}
	return targets, nil
}

func marshalDetail(detailType string, detail any) ([]byte, error) {
	if detailType == "" {
		return nil, errors.New("detail-type vazio")
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("serializar detail de %s: %w", detailType, err)
	}
	if len(b) > MaxDetailBytes {
		return nil, fmt.Errorf("detail de %s com %d bytes excede o limite de %d", detailType, len(b), MaxDetailBytes)
	}
	return b, nil
}
