// Package config lê a configuração comum a todos os serviços a partir de
// variáveis de ambiente.
//
// RUNTIME escolhe os adaptadores: "local" (HTTP, emuladores) ou "aws"
// (Lambda Invoke, EventBridge, DynamoDB e Cognito reais). Não há valor
// padrão, para que um serviço nunca rode contra a AWS por engano nem o
// contrário.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Runtime indica onde o processo está rodando.
type Runtime string

const (
	Local Runtime = "local"
	AWS   Runtime = "aws"
)

// Base é a configuração que todo serviço tem.
type Base struct {
	Runtime  Runtime
	Env      string // local, dev ou prod
	Service  string // nome do serviço nos logs
	Region   string
	LogLevel string
}

// IsLocal informa se o processo usa os adaptadores locais.
func (b Base) IsLocal() bool { return b.Runtime == Local }

// LookupFunc tem a assinatura de os.LookupEnv; permite injetar variáveis em testes.
type LookupFunc func(key string) (string, bool)

// Load lê a configuração base do ambiente do processo.
func Load(service string) (Base, error) { return LoadFrom(os.LookupEnv, service) }

// LoadFrom lê a configuração base usando lookup.
func LoadFrom(lookup LookupFunc, service string) (Base, error) {
	r := Reader{lookup: lookup}

	b := Base{
		Runtime:  Runtime(strings.ToLower(r.Require("RUNTIME"))),
		Service:  service,
		Region:   r.Get("AWS_REGION", "us-east-1"),
		LogLevel: r.Get("LOG_LEVEL", "info"),
	}
	switch b.Runtime {
	case Local:
		b.Env = r.Get("ENV", "local")
		if b.Env != "local" {
			r.fail(fmt.Errorf("ENV=%q não combina com RUNTIME=local; use ENV=local", b.Env))
		}
	case AWS:
		b.Env = r.Require("ENV")
		if b.Env != "dev" && b.Env != "prod" {
			r.fail(fmt.Errorf("ENV=%q inválido com RUNTIME=aws; use dev ou prod", b.Env))
		}
	case "":
		// O erro de variável ausente já foi registrado por Require.
	default:
		r.fail(fmt.Errorf("RUNTIME=%q inválido; use local ou aws", b.Runtime))
	}
	if service == "" {
		r.fail(errors.New("nome do serviço vazio"))
	}

	if err := r.Err(); err != nil {
		return Base{}, err
	}
	return b, nil
}

// Reader lê variáveis e acumula os erros, para que o serviço informe todas
// as variáveis faltando de uma vez só.
type Reader struct {
	lookup LookupFunc
	errs   []error
}

// NewReader cria um Reader; lookup nil usa os.LookupEnv.
func NewReader(lookup LookupFunc) *Reader {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	return &Reader{lookup: lookup}
}

// Get devolve a variável ou def quando ela estiver ausente ou vazia.
func (r *Reader) Get(key, def string) string {
	if v, ok := r.lookup(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// Require devolve a variável e registra um erro quando ela estiver ausente ou vazia.
func (r *Reader) Require(key string) string {
	v := r.Get(key, "")
	if v == "" {
		r.fail(fmt.Errorf("variável de ambiente %s é obrigatória", key))
	}
	return v
}

// Err devolve todos os erros acumulados, ou nil.
func (r *Reader) Err() error { return errors.Join(r.errs...) }

func (r *Reader) fail(err error) { r.errs = append(r.errs, err) }
