package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) LookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadFrom(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		want    Base
		wantErr []string
	}{
		{
			name: "local com padrões",
			vars: map[string]string{"RUNTIME": "local"},
			want: Base{Runtime: Local, Env: "local", Service: "svc", Region: "us-east-1", LogLevel: "info"},
		},
		{
			name: "aws dev",
			vars: map[string]string{"RUNTIME": "AWS", "ENV": "dev", "LOG_LEVEL": "debug", "AWS_REGION": "sa-east-1"},
			want: Base{Runtime: AWS, Env: "dev", Service: "svc", Region: "sa-east-1", LogLevel: "debug"},
		},
		{name: "sem RUNTIME", vars: map[string]string{}, wantErr: []string{"RUNTIME é obrigatória"}},
		{name: "RUNTIME só com espaços", vars: map[string]string{"RUNTIME": "  "}, wantErr: []string{"RUNTIME é obrigatória"}},
		{name: "RUNTIME inválido", vars: map[string]string{"RUNTIME": "k8s"}, wantErr: []string{`RUNTIME="k8s" inválido`}},
		{name: "aws sem ENV", vars: map[string]string{"RUNTIME": "aws"}, wantErr: []string{"ENV é obrigatória"}},
		{name: "aws com ENV local", vars: map[string]string{"RUNTIME": "aws", "ENV": "local"}, wantErr: []string{`ENV="local" inválido`}},
		{name: "local com ENV prod", vars: map[string]string{"RUNTIME": "local", "ENV": "prod"}, wantErr: []string{"não combina com RUNTIME=local"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadFrom(env(tt.vars), "svc")
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("esperava erro, veio %+v", got)
				}
				for _, w := range tt.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("erro %q não contém %q", err, w)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadFromServiçoVazio(t *testing.T) {
	if _, err := LoadFrom(env(map[string]string{"RUNTIME": "local"}), ""); err == nil {
		t.Fatal("esperava erro para serviço vazio")
	}
}

func TestReaderAcumulaErros(t *testing.T) {
	r := NewReader(env(map[string]string{"A": "1"}))
	if got := r.Require("A"); got != "1" {
		t.Errorf("Require(A) = %q", got)
	}
	r.Require("B")
	r.Require("C")
	err := r.Err()
	if err == nil || !strings.Contains(err.Error(), "B") || !strings.Contains(err.Error(), "C") {
		t.Fatalf("esperava erros de B e C, veio %v", err)
	}
}

func TestLoadUsaAmbienteDoProcesso(t *testing.T) {
	t.Setenv("RUNTIME", "local")
	t.Setenv("ENV", "")
	b, err := Load("svc")
	if err != nil || !b.IsLocal() {
		t.Fatalf("Load = %+v, %v", b, err)
	}
	if NewReader(nil).Get("RUNTIME", "") != "local" {
		t.Error("NewReader(nil) deveria usar os.LookupEnv")
	}
}
