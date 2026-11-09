package events

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// LocalPublisher entrega o evento por POST a todos os consumidores, em
// paralelo. A falha de um consumidor não impede a entrega aos outros; o erro
// devolvido junta as falhas. Não há retentativa nem DLQ.
type LocalPublisher struct {
	targets []string
	source  string
	client  *http.Client
	log     *slog.Logger
	now     func() time.Time
}

// NewLocal cria o publisher local. client nil usa timeout de 30 s; log nil
// usa slog.Default().
func NewLocal(targets []string, source string, client *http.Client, log *slog.Logger) *LocalPublisher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if log == nil {
		log = slog.Default()
	}
	return &LocalPublisher{targets: targets, source: source, client: client, log: log, now: time.Now}
}

// Publish implementa Publisher.
func (p *LocalPublisher) Publish(ctx context.Context, detailType string, detail any) error {
	b, err := marshalDetail(detailType, detail)
	if err != nil {
		return err
	}
	ev := Event{
		Version:    "0",
		ID:         newID(),
		DetailType: detailType,
		Source:     p.source,
		Time:       p.now().UTC(),
		Region:     "local",
		Detail:     b,
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("serializar evento %s: %w", detailType, err)
	}

	if len(p.targets) == 0 {
		p.log.WarnContext(ctx, "evento sem consumidor local; confira LOCAL_EVENT_TARGETS", "detailType", detailType, "eventId", ev.ID)
		return nil
	}

	errs := make([]error, len(p.targets))
	var wg sync.WaitGroup
	for i, target := range p.targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.post(ctx, target, body); err != nil {
				errs[i] = fmt.Errorf("consumidor %s: %w", target, err)
			}
		}()
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("publicar %s (evento %s): %w", detailType, ev.ID, err)
	}
	return nil
}

func (p *LocalPublisher) post(ctx context.Context, target string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}

// newID gera um id aleatório no formato UUID v4, como o do EventBridge.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
