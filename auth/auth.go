// Package auth valida os JWT do Cognito (ou do cognito-local) e expõe as
// claims de que o BFF e os serviços precisam: userId, e-mail e grupos.
//
// O JWKS é configurável, então o mesmo código valida tokens do cognito-local
// (http://localhost:9229/<pool>/.well-known/jwks.json) e do Cognito real
// (https://cognito-idp.<região>.amazonaws.com/<pool>/.well-known/jwks.json).
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/errs"
)

// Grupos do User Pool.
const (
	GroupAdmin   = "admin"
	GroupCliente = "cliente"
)

// Tipos de token do Cognito (claim token_use).
const (
	TokenUseAccess = "access"
	TokenUseID     = "id"
)

// Claims são os dados do usuário autenticado.
type Claims struct {
	UserID    string // claim sub; é o único identificador do usuário nos serviços
	Username  string
	Email     string // só no ID token
	Groups    []string
	TokenUse  string
	ClientID  string
	ExpiresAt time.Time
}

// HasGroup informa se o usuário está no grupo.
func (c Claims) HasGroup(g string) bool { return slices.Contains(c.Groups, g) }

// IsAdmin informa se o usuário está no grupo admin. Quem não é admin é tratado
// como cliente, inclusive usuários ainda sem grupo.
func (c Claims) IsAdmin() bool { return c.HasGroup(GroupAdmin) }

// Config define como validar os tokens.
type Config struct {
	// Issuer é a URL do User Pool, por exemplo
	// https://cognito-idp.us-east-1.amazonaws.com/us-east-1_abc ou
	// http://localhost:9229/local_abc. Obrigatório.
	Issuer string
	// JWKSURL tem como padrão Issuer + "/.well-known/jwks.json".
	JWKSURL string
	// ClientID do app client. Quando informado, o token precisa ter sido
	// emitido para ele (client_id no access token, aud no ID token).
	ClientID string
	// TokenUse aceito; padrão "access".
	TokenUse string
	// Leeway tolera diferença de relógio; padrão 30s.
	Leeway time.Duration
	// CacheTTL da lista de chaves; padrão 1h.
	CacheTTL time.Duration
	// HTTPClient para buscar o JWKS; padrão com timeout de 5s.
	HTTPClient *http.Client
	// Now permite fixar o relógio em testes.
	Now func() time.Time
}

// Validator valida tokens. É seguro para uso concorrente.
type Validator struct {
	cfg  Config
	jwks *jwksCache
}

// NewValidator cria um Validator. As chaves só são baixadas no primeiro uso.
func NewValidator(cfg Config) (*Validator, error) {
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	if cfg.Issuer == "" {
		return nil, errors.New("auth: Issuer é obrigatório")
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = cfg.Issuer + "/.well-known/jwks.json"
	}
	if cfg.TokenUse == "" {
		cfg.TokenUse = TokenUseAccess
	}
	if cfg.TokenUse != TokenUseAccess && cfg.TokenUse != TokenUseID {
		return nil, fmt.Errorf("auth: TokenUse %q inválido", cfg.TokenUse)
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 30 * time.Second
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = time.Hour
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Validator{
		cfg: cfg,
		jwks: &jwksCache{
			url: cfg.JWKSURL, client: cfg.HTTPClient, ttl: cfg.CacheTTL,
			minRefresh: time.Minute, now: cfg.Now,
		},
	}, nil
}

type cognitoClaims struct {
	jwt.RegisteredClaims
	TokenUse string   `json:"token_use"`
	ClientID string   `json:"client_id"`
	Username string   `json:"username"`
	CogUser  string   `json:"cognito:username"`
	Email    string   `json:"email"`
	Groups   []string `json:"cognito:groups"`
}

// Validate confere assinatura, emissor, expiração, token_use e client.
// Qualquer falha devolve errs.Unauthorized com código "token_invalido"; o
// motivo fica só na causa interna, para o log.
func (v *Validator) Validate(ctx context.Context, token string) (Claims, error) {
	token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(token), "Bearer "))
	if token == "" {
		return Claims{}, unauthorized(errors.New("token ausente"))
	}

	var cc cognitoClaims
	_, err := jwt.ParseWithClaims(token, &cc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.jwks.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.cfg.Leeway),
		jwt.WithTimeFunc(v.cfg.Now),
	)
	if err != nil {
		return Claims{}, unauthorized(err)
	}

	if cc.TokenUse != v.cfg.TokenUse {
		return Claims{}, unauthorized(fmt.Errorf("token_use %q, esperado %q", cc.TokenUse, v.cfg.TokenUse))
	}
	if v.cfg.ClientID != "" {
		ok := cc.ClientID == v.cfg.ClientID
		if cc.TokenUse == TokenUseID {
			ok = slices.Contains(cc.Audience, v.cfg.ClientID)
		}
		if !ok {
			return Claims{}, unauthorized(errors.New("token emitido para outro app client"))
		}
	}
	if cc.Subject == "" {
		return Claims{}, unauthorized(errors.New("token sem sub"))
	}

	username := cc.Username
	if username == "" {
		username = cc.CogUser
	}
	return Claims{
		UserID:    cc.Subject,
		Username:  username,
		Email:     cc.Email,
		Groups:    cc.Groups,
		TokenUse:  cc.TokenUse,
		ClientID:  cc.ClientID,
		ExpiresAt: cc.ExpiresAt.Time,
	}, nil
}

func unauthorized(cause error) error {
	return errs.Unauthorized("token_invalido", "Sessão inválida ou expirada. Faça login novamente.").WithCause(cause)
}

// ErrForbidden é devolvido por RequireAdmin.
var ErrForbidden = errs.Forbidden("somente_admin", "Acesso restrito a administradores.")

// RequireAdmin devolve um erro 403 se o usuário não for admin.
func RequireAdmin(c Claims) error {
	if !c.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

type ctxKey struct{}

// WithClaims guarda as claims no contexto da requisição.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext devolve as claims do contexto.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(Claims)
	return c, ok
}
