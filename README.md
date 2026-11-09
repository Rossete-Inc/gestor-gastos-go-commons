# gestor-gastos-go-commons

Biblioteca Go compartilhada pelos serviços e pelo BFF do Gestor de Gastos.

```sh
go get github.com/Rossete-Inc/gestor-gastos-go-commons@latest
```

| Pacote | O que faz |
| --- | --- |
| `config` | Lê `RUNTIME` (`local` ou `aws`, obrigatório e sem padrão), `ENV`, `AWS_REGION` e `LOG_LEVEL`, e acumula todas as variáveis ausentes num único erro |
| `errs` | Erros de domínio tipados (`NotFound`, `Conflict`, `Validation`...) e conversão para `application/problem+json` (RFC 9457) com `code` e `errors[]`. A causa interna nunca vai para o cliente |
| `logging` | Logger `slog` em JSON com `service` e `env`. Redige atributos sensíveis (`email`, `nome`, `senha`, `token`, `descricao`...) e mascara e-mails em qualquer texto |
| `auth` | Valida JWT do Cognito ou do cognito-local por JWKS configurável, com cache e rotação de chave. Confere emissor, expiração, `token_use` e app client, e expõe `UserID`, `Groups` e `IsAdmin()` |

Os próximos pacotes são `invoke` (4.2), `events` (4.3) e os helpers de paginação e centavos (4.4).

## Exemplo

```go
base, err := config.Load("expenses-service")
log := logging.New(logging.Options{Service: base.Service, Env: base.Env, Level: base.LogLevel})

v, err := auth.NewValidator(auth.Config{Issuer: os.Getenv("COGNITO_ISSUER"), ClientID: os.Getenv("COGNITO_CLIENT_ID")})
claims, err := v.Validate(ctx, r.Header.Get("Authorization"))
if err != nil {
    errs.Write(w, r.URL.Path, err) // 401 problem+json
    return
}
log.Info("gasto criado", "userId", claims.UserID) // nunca o e-mail
```

## Testes

```sh
go test ./... -cover
```

O teste de integração do `auth` roda contra o cognito-local do `gestor-gastos-infra-base`:

```sh
# no infra-base
task local:up && task local:seed
# aqui
set -a; source <clone do infra-base>/local/.env.cognito; set +a
go test -tags integration ./auth
```
