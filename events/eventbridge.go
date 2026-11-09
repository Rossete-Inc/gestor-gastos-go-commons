package events

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
)

// EventBridgeAPI é o subconjunto do client EventBridge usado aqui;
// *eventbridge.Client o satisfaz, e os testes usam um fake.
type EventBridgeAPI interface {
	PutEvents(ctx context.Context, params *eventbridge.PutEventsInput, optFns ...func(*eventbridge.Options)) (*eventbridge.PutEventsOutput, error)
}

// EventBridgePublisher publica no barramento com PutEvents.
type EventBridgePublisher struct {
	api    EventBridgeAPI
	bus    string
	source string
}

// NewEventBridge cria o publisher para o barramento bus.
func NewEventBridge(api EventBridgeAPI, bus, source string) *EventBridgePublisher {
	return &EventBridgePublisher{api: api, bus: bus, source: source}
}

// Publish implementa Publisher. PutEvents pode aceitar a chamada e rejeitar
// a entrada; isso também é erro.
func (p *EventBridgePublisher) Publish(ctx context.Context, detailType string, detail any) error {
	b, err := marshalDetail(detailType, detail)
	if err != nil {
		return err
	}
	out, err := p.api.PutEvents(ctx, &eventbridge.PutEventsInput{
		Entries: []types.PutEventsRequestEntry{{
			EventBusName: aws.String(p.bus),
			Source:       aws.String(p.source),
			DetailType:   aws.String(detailType),
			Detail:       aws.String(string(b)),
		}},
	})
	if err != nil {
		return fmt.Errorf("publicar %s em %s: %w", detailType, p.bus, err)
	}
	if out.FailedEntryCount > 0 {
		code, msg := "desconhecido", ""
		if len(out.Entries) > 0 {
			code, msg = aws.ToString(out.Entries[0].ErrorCode), aws.ToString(out.Entries[0].ErrorMessage)
		}
		return fmt.Errorf("publicar %s em %s: entrada rejeitada (%s): %s", detailType, p.bus, code, msg)
	}
	return nil
}
