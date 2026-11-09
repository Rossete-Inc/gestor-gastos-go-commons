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
| `invoke` | Chamada síncrona BFF → serviço com envelope `operation` + `payload` + `userId`. Dois transportes atrás da interface `Transport`: Lambda Invoke (`RUNTIME=aws`) e POST HTTP em `/invoke` (`RUNTIME=local`). O `Router` do serviço atende os dois com o mesmo handler, e erros de domínio voltam ao BFF com o mesmo `Kind`, `Code` e `Fields` |
| `events` | Eventos assíncronos (`UserConfirmed`, `UserDeleted`). Publisher EventBridge (`RUNTIME=aws`) e publisher local, que faz POST do mesmo envelope a cada URL de `LOCAL_EVENT_TARGETS` em paralelo; a falha de um consumidor não impede os outros. O `Router` do consumidor atende a Lambda e o `cmd/local` (`POST /events`) com o mesmo handler |

O próximo pacote são os helpers de paginação e centavos (4.4).

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

### invoke

No serviço, o mesmo `Router` atende a Lambda e o `cmd/local`:

```go
r := invoke.NewRouter(log)
r.Handle("ObterGasto", invoke.Typed(func(ctx context.Context, userID string, in ObterGastoIn) (Gasto, error) {
    return svc.Obter(ctx, userID, in.ID) // errs.NotFound chega ao BFF como 404
}))

lambda.Start(r.LambdaHandler())                         // cmd/lambda
http.ListenAndServe(":8083", r.HTTPHandler())           // cmd/local, POST /invoke
```

No BFF, o transporte é escolhido pelo `RUNTIME`. O destino é o ARN com alias na AWS e a URL do `cmd/local` localmente:

```go
t, err := invoke.NewTransport(base.Runtime, os.Getenv("EXPENSES_TARGET"), lambda.NewFromConfig(awsCfg), nil)
gastos := invoke.NewClient(t)

var g Gasto
err = gastos.Call(ctx, "ObterGasto", claims.UserID, ObterGastoIn{ID: id}, &g)
```

### events

Quem publica escolhe o publisher pelo `RUNTIME`:

```go
pub, err := events.NewPublisher(events.Options{
    Runtime:      base.Runtime,
    Source:       "gestor-gastos.users-service",
    BusName:      os.Getenv("EVENT_BUS_NAME"),          // aws
    API:          eventbridge.NewFromConfig(awsCfg),    // aws
    LocalTargets: os.Getenv("LOCAL_EVENT_TARGETS"),     // local: "http://localhost:8082/events,http://localhost:8083/events"
    Log:          log,
})
err = pub.Publish(ctx, events.UserDeleted, events.UserDeletedDetail{UserID: id})
```

Quem consome registra o handler, que precisa ser idempotente porque a AWS entrega "pelo menos uma vez":

```go
r := events.NewRouter(log)
r.Handle(events.UserDeleted, events.Typed(func(ctx context.Context, e events.Event, d events.UserDeletedDetail) error {
    return store.ApagarTudoDoUsuario(ctx, d.UserID)
}))

lambda.Start(r.LambdaHandler())                 // cmd/lambda; erro → retentativa e DLQ
mux.Handle(events.Path, r.HTTPHandler())        // cmd/local, POST /events
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
