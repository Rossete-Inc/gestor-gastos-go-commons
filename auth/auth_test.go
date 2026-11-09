package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/errs"
)

const (
	issuer   = "http://cognito.test/local_pool"
	clientID = "client-123"
)

var agora = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

type chave struct {
	kid  string
	priv *rsa.PrivateKey
}

func novaChave(t *testing.T, kid string) chave {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return chave{kid: kid, priv: k}
}

func (c chave) jwk() map[string]string {
	return map[string]string{
		"kty": "RSA", "kid": c.kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(c.priv.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(c.priv.E)).Bytes()),
	}
}

// jwksServer serve as chaves de *atual e conta as requisições.
type jwksServer struct {
	*httptest.Server
	hits  atomic.Int32
	atual atomic.Pointer[[]chave]
}

func novoJWKS(t *testing.T, chaves ...chave) *jwksServer {
	s := &jwksServer{}
	s.atual.Store(&chaves)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		var keys []map[string]string
		for _, c := range *s.atual.Load() {
			keys = append(keys, c.jwk())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(s.Close)
	return s
}

func claimsPadrao() jwt.MapClaims {
	return jwt.MapClaims{
		"sub": "user-1", "iss": issuer, "token_use": "access", "client_id": clientID,
		"username": "cliente@local.test", "cognito:groups": []string{"cliente"},
		"iat": agora.Add(-time.Minute).Unix(), "exp": agora.Add(14 * time.Minute).Unix(),
	}
}

func assinar(t *testing.T, c chave, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = c.kid
	s, err := tok.SignedString(c.priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validador(t *testing.T, url string, mut ...func(*Config)) *Validator {
	t.Helper()
	cfg := Config{Issuer: issuer, JWKSURL: url, ClientID: clientID, Now: func() time.Time { return agora }}
	for _, m := range mut {
		m(&cfg)
	}
	v, err := NewValidator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func exigir401(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("esperava erro")
	}
	if errs.KindOf(err) != errs.KindUnauthorized {
		t.Fatalf("esperava 401, veio %v", err)
	}
	if strings.Contains(errs.ToProblem(err).Detail, "kid") {
		t.Fatalf("o motivo interno vazou no problem: %v", errs.ToProblem(err))
	}
}

func TestTokenValido(t *testing.T) {
	k := novaChave(t, "k1")
	srv := novoJWKS(t, k)
	v := validador(t, srv.URL)

	c, err := v.Validate(context.Background(), "Bearer "+assinar(t, k, claimsPadrao()))
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "user-1" || c.Username != "cliente@local.test" || c.ClientID != clientID || c.TokenUse != "access" {
		t.Errorf("claims = %+v", c)
	}
	if !c.HasGroup(GroupCliente) || c.IsAdmin() {
		t.Errorf("grupos = %v", c.Groups)
	}
	if !c.ExpiresAt.Equal(agora.Add(14 * time.Minute)) {
		t.Errorf("exp = %v", c.ExpiresAt)
	}

	// Segunda validação usa o cache.
	if _, err := v.Validate(context.Background(), assinar(t, k, claimsPadrao())); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != 1 {
		t.Errorf("JWKS buscado %d vezes, esperava 1", srv.hits.Load())
	}
}

func TestTokensRecusados(t *testing.T) {
	k := novaChave(t, "k1")
	outra := novaChave(t, "k1") // mesmo kid, chave diferente
	srv := novoJWKS(t, k)
	v := validador(t, srv.URL)

	alterar := func(f func(jwt.MapClaims)) string {
		c := claimsPadrao()
		f(c)
		return assinar(t, k, c)
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsPadrao())
	hs.Header["kid"] = "k1"
	hsTok, _ := hs.SignedString([]byte("segredo"))

	casos := map[string]string{
		"vazio":             "",
		"lixo":              "abc.def.ghi",
		"assinatura errada": assinar(t, outra, claimsPadrao()),
		"HS256":             hsTok,
		"expirado":          alterar(func(c jwt.MapClaims) { c["exp"] = agora.Add(-time.Minute).Unix() }),
		"sem exp":           alterar(func(c jwt.MapClaims) { delete(c, "exp") }),
		"iat no futuro":     alterar(func(c jwt.MapClaims) { c["iat"] = agora.Add(time.Hour).Unix() }),
		"outro issuer":      alterar(func(c jwt.MapClaims) { c["iss"] = "http://evil/pool" }),
		"id token":          alterar(func(c jwt.MapClaims) { c["token_use"] = "id" }),
		"outro client":      alterar(func(c jwt.MapClaims) { c["client_id"] = "outro" }),
		"sem sub":           alterar(func(c jwt.MapClaims) { delete(c, "sub") }),
	}
	for nome, tok := range casos {
		t.Run(nome, func(t *testing.T) {
			_, err := v.Validate(context.Background(), tok)
			exigir401(t, err)
		})
	}
}

func TestLeewayToleraRelogio(t *testing.T) {
	k := novaChave(t, "k1")
	v := validador(t, novoJWKS(t, k).URL)
	c := claimsPadrao()
	c["exp"] = agora.Add(-10 * time.Second).Unix()
	if _, err := v.Validate(context.Background(), assinar(t, k, c)); err != nil {
		t.Fatalf("10s de atraso deveria passar no leeway de 30s: %v", err)
	}
}

func TestIDToken(t *testing.T) {
	k := novaChave(t, "k1")
	v := validador(t, novoJWKS(t, k).URL, func(c *Config) { c.TokenUse = TokenUseID })

	c := claimsPadrao()
	c["token_use"], c["aud"], c["email"] = "id", clientID, "cliente@local.test"
	delete(c, "client_id")
	delete(c, "username")
	c["cognito:username"] = "cliente-username"
	got, err := v.Validate(context.Background(), assinar(t, k, c))
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "cliente@local.test" || got.Username != "cliente-username" {
		t.Errorf("claims = %+v", got)
	}

	c["aud"] = "outro"
	_, err = v.Validate(context.Background(), assinar(t, k, c))
	exigir401(t, err)
}

func TestSemClientIDNaoConfereClient(t *testing.T) {
	k := novaChave(t, "k1")
	v := validador(t, novoJWKS(t, k).URL, func(c *Config) { c.ClientID = "" })
	c := claimsPadrao()
	c["client_id"] = "qualquer"
	if _, err := v.Validate(context.Background(), assinar(t, k, c)); err != nil {
		t.Fatal(err)
	}
}

func TestRotacaoDeChave(t *testing.T) {
	k1, k2 := novaChave(t, "k1"), novaChave(t, "k2")
	srv := novoJWKS(t, k1)
	relogio := agora
	v := validador(t, srv.URL, func(c *Config) { c.Now = func() time.Time { return relogio } })

	if _, err := v.Validate(context.Background(), assinar(t, k1, claimsPadrao())); err != nil {
		t.Fatal(err)
	}

	// O Cognito publica k2. Um kid novo logo após a busca não refaz a busca...
	srv.atual.Store(&[]chave{k1, k2})
	_, err := v.Validate(context.Background(), assinar(t, k2, claimsPadrao()))
	exigir401(t, err)
	if srv.hits.Load() != 1 {
		t.Fatalf("kid desconhecido dentro do minRefresh buscou de novo (%d)", srv.hits.Load())
	}

	// ...mas depois de 1 minuto busca e aceita.
	relogio = relogio.Add(61 * time.Second)
	if _, err := v.Validate(context.Background(), assinar(t, k2, claimsPadrao())); err != nil {
		t.Fatalf("k2 deveria ser aceita após o refresh: %v", err)
	}
	if srv.hits.Load() != 2 {
		t.Errorf("hits = %d, esperava 2", srv.hits.Load())
	}

	// Depois do TTL, renova mesmo com kid conhecido.
	relogio = relogio.Add(2 * time.Hour)
	recente := claimsPadrao()
	recente["iat"], recente["exp"] = relogio.Unix(), relogio.Add(15*time.Minute).Unix()
	if _, err := v.Validate(context.Background(), assinar(t, k1, recente)); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != 3 {
		t.Errorf("hits = %d, esperava 3 após o TTL", srv.hits.Load())
	}
}

func TestFalhasDoJWKS(t *testing.T) {
	k := novaChave(t, "k1")
	tok := assinar(t, k, claimsPadrao())

	servir := func(status int, body string) string {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(s.Close)
		return s.URL
	}
	semN := k.jwk()
	semN["n"] = "!!"
	semE := k.jwk()
	semE["e"] = "!!"
	expZero := k.jwk()
	expZero["e"] = ""
	jsonDe := func(keys ...map[string]string) string {
		b, _ := json.Marshal(map[string]any{"keys": keys})
		return string(b)
	}

	casos := map[string]string{
		"fora do ar":     "http://127.0.0.1:1/jwks.json",
		"500":            servir(500, "erro"),
		"json inválido":  servir(200, "{"),
		"sem chaves RSA": servir(200, `{"keys":[{"kty":"EC","kid":"k1"},{"kty":"RSA","kid":"k2","use":"enc"}]}`),
		"n inválido":     servir(200, jsonDe(semN)),
		"e inválido":     servir(200, jsonDe(semE)),
		"e vazio":        servir(200, jsonDe(expZero)),
		"url inválida":   "://sem-esquema",
	}
	for nome, url := range casos {
		t.Run(nome, func(t *testing.T) {
			_, err := validador(t, url).Validate(context.Background(), tok)
			exigir401(t, err)
		})
	}
}

func TestFalhaNoRefreshAposTTL(t *testing.T) {
	k1 := novaChave(t, "k1")
	srv := novoJWKS(t, k1)
	relogio := agora
	v := validador(t, srv.URL, func(c *Config) { c.Now = func() time.Time { return relogio } })
	if _, err := v.Validate(context.Background(), assinar(t, k1, claimsPadrao())); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	relogio = relogio.Add(61 * time.Second)
	_, err := v.Validate(context.Background(), assinar(t, novaChave(t, "k9"), claimsPadrao()))
	exigir401(t, err)
}

func TestNewValidatorConfig(t *testing.T) {
	if _, err := NewValidator(Config{}); err == nil {
		t.Error("Issuer vazio deveria falhar")
	}
	if _, err := NewValidator(Config{Issuer: issuer, TokenUse: "refresh"}); err == nil {
		t.Error("TokenUse inválido deveria falhar")
	}
	v, err := NewValidator(Config{Issuer: issuer + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if v.cfg.JWKSURL != issuer+"/.well-known/jwks.json" || v.cfg.TokenUse != TokenUseAccess ||
		v.cfg.Leeway != 30*time.Second || v.cfg.CacheTTL != time.Hour || v.cfg.HTTPClient == nil {
		t.Errorf("padrões = %+v", v.cfg)
	}
}

func TestRequireAdminEContexto(t *testing.T) {
	admin := Claims{UserID: "a", Groups: []string{GroupAdmin}}
	semGrupo := Claims{UserID: "b"}
	if err := RequireAdmin(admin); err != nil {
		t.Errorf("admin recusado: %v", err)
	}
	err := RequireAdmin(semGrupo)
	if !errors.Is(err, ErrForbidden) || errs.KindOf(err) != errs.KindForbidden {
		t.Errorf("sem grupo deveria ser 403, veio %v", err)
	}

	if _, ok := FromContext(context.Background()); ok {
		t.Error("contexto vazio não deveria ter claims")
	}
	got, ok := FromContext(WithClaims(context.Background(), admin))
	if !ok || got.UserID != "a" {
		t.Errorf("FromContext = %+v, %v", got, ok)
	}
}
