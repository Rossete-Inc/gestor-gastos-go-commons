//go:build integration

package auth

// Teste contra o cognito-local do infra-base. Para rodar:
//
//	task local:up && task local:seed      (no clone do gestor-gastos-infra-base)
//	set -a; source <clone do infra-base>/local/.env.cognito; set +a
//	go test -tags integration ./auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func loginLocal(t *testing.T, endpoint, clientID, user, senha string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"AuthFlow": "USER_PASSWORD_AUTH", "ClientId": clientID,
		"AuthParameters": map[string]string{"USERNAME": user, "PASSWORD": senha},
	})
	req, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.InitiateAuth")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		AuthenticationResult struct{ AccessToken string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AuthenticationResult.AccessToken == "" {
		t.Fatalf("login falhou (status %d): %v", resp.StatusCode, err)
	}
	return out.AuthenticationResult.AccessToken
}

func TestCognitoLocal(t *testing.T) {
	iss, client, endpoint := os.Getenv("COGNITO_ISSUER"), os.Getenv("COGNITO_CLIENT_ID"), os.Getenv("COGNITO_ENDPOINT")
	if iss == "" || client == "" || endpoint == "" {
		t.Skip("carregue local/.env.cognito do infra-base para rodar")
	}
	v, err := NewValidator(Config{Issuer: iss, ClientID: client})
	if err != nil {
		t.Fatal(err)
	}

	admin, err := v.Validate(context.Background(), loginLocal(t, endpoint, client, "admin@local.test", "Local1234"))
	if err != nil {
		t.Fatal(err)
	}
	if !admin.IsAdmin() || admin.UserID == "" {
		t.Errorf("admin = %+v", admin)
	}

	cliente, err := v.Validate(context.Background(), loginLocal(t, endpoint, client, "cliente@local.test", "Local1234"))
	if err != nil {
		t.Fatal(err)
	}
	if cliente.IsAdmin() || !cliente.HasGroup(GroupCliente) {
		t.Errorf("cliente = %+v", cliente)
	}
}
