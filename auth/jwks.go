package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// jwksCache baixa e guarda as chaves públicas RSA de um endpoint JWKS.
//
// As chaves são renovadas a cada ttl. Um kid desconhecido força nova busca,
// no máximo uma vez a cada minRefresh, para que tokens com kid inventado
// não virem uma enxurrada de requisições ao Cognito.
type jwksCache struct {
	url        string
	client     *http.Client
	ttl        time.Duration
	minRefresh time.Duration
	now        func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

var errKidDesconhecido = errors.New("kid desconhecido")

func (c *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	age := c.now().Sub(c.fetchedAt)
	if c.keys == nil || age >= c.ttl {
		if err := c.refresh(ctx); err != nil {
			return nil, err
		}
	} else if _, ok := c.keys[kid]; !ok && age >= c.minRefresh {
		if err := c.refresh(ctx); err != nil {
			return nil, err
		}
	}

	k, ok := c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errKidDesconhecido, kid)
	}
	return k, nil
}

func (c *jwksCache) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return fmt.Errorf("jwks: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("jwks: buscar %s: %w", c.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: %s respondeu %d", c.url, resp.StatusCode)
	}

	var body struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("jwks: resposta inválida: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(body.Keys))
	for _, k := range body.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		pub, err := parseRSA(k)
		if err != nil {
			return fmt.Errorf("jwks: chave %q: %w", k.Kid, err)
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("jwks: nenhuma chave RSA de assinatura")
	}
	c.keys, c.fetchedAt = keys, c.now()
	return nil
}

func parseRSA(k jwk) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("n: %w", err)
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("e: %w", err)
	}
	exp := new(big.Int).SetBytes(e)
	if len(n) == 0 || !exp.IsInt64() || exp.Int64() < 3 {
		return nil, errors.New("módulo ou expoente inválido")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exp.Int64())}, nil
}
