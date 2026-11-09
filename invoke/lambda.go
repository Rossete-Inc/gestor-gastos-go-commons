package invoke

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

// LambdaAPI é o subconjunto do client Lambda usado aqui; *lambda.Client o
// satisfaz, e os testes usam um fake.
type LambdaAPI interface {
	Invoke(ctx context.Context, params *lambda.InvokeInput, optFns ...func(*lambda.Options)) (*lambda.InvokeOutput, error)
}

// LambdaTransport envia o envelope por lambda:InvokeFunction síncrono.
type LambdaTransport struct {
	api      LambdaAPI
	function string
}

// NewLambda cria o transporte para a função (ARN ou nome, de preferência com
// o alias "live").
func NewLambda(api LambdaAPI, function string) *LambdaTransport {
	return &LambdaTransport{api: api, function: function}
}

// Send implementa Transport.
func (t *LambdaTransport) Send(ctx context.Context, req Request) (Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("serializar envelope: %w", err)
	}
	if len(body) > MaxPayloadBytes {
		return Response{}, fmt.Errorf("envelope de %d bytes excede o limite de %d", len(body), MaxPayloadBytes)
	}

	out, err := t.api.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(t.function),
		InvocationType: types.InvocationTypeRequestResponse,
		Payload:        body,
	})
	if err != nil {
		return Response{}, fmt.Errorf("lambda %s: %w", t.function, err)
	}
	// FunctionError indica erro não tratado na função (panic, timeout, falta
	// de memória). Erros de domínio vêm dentro de Response.Error.
	if out.FunctionError != nil {
		return Response{}, fmt.Errorf("lambda %s falhou (%s): %s", t.function, *out.FunctionError, truncate(out.Payload, 512))
	}

	var resp Response
	if err := json.Unmarshal(out.Payload, &resp); err != nil {
		return Response{}, fmt.Errorf("lambda %s: resposta fora do envelope: %w", t.function, err)
	}
	return resp, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
