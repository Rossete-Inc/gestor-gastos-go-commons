package page

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Rossete-Inc/gestor-gastos-go-commons/errs"
)

var segredo = []byte("0123456789abcdef0123456789abcdef")

const escopo = "USER#u-1|2026-11|grupo=CARTAO"

func novoCodec(t *testing.T, s []byte) *Codec {
	t.Helper()
	c, err := NewCodec(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func chave() Key {
	return Key{"PK": "USER#u-1", "SK": "EXP#2026-11-03#01JC9X7Q2W3E4R5T6Y7U8I9O0P"}
}

func TestIdaEVolta(t *testing.T) {
	c := novoCodec(t, segredo)
	cur, err := c.Encode(escopo, chave())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cur, "v1.") || strings.ContainsAny(cur, "+/=") {
		t.Errorf("cursor não é base64url sem padding: %q", cur)
	}
	got, err := c.Decode(escopo, cur)
	if err != nil {
		t.Fatal(err)
	}
	if got["PK"] != "USER#u-1" || got["SK"] != chave()["SK"] || len(got) != 2 {
		t.Errorf("chave = %v", got)
	}
}

func TestCursorVazio(t *testing.T) {
	c := novoCodec(t, segredo)
	if cur, err := c.Encode(escopo, nil); cur != "" || err != nil {
		t.Errorf("Encode(nil) = %q, %v", cur, err)
	}
	if cur, err := c.Encode(escopo, Key{}); cur != "" || err != nil {
		t.Errorf("Encode({}) = %q, %v", cur, err)
	}
	if k, err := c.Decode(escopo, ""); k != nil || err != nil {
		t.Errorf("Decode(\"\") = %v, %v", k, err)
	}
}

// exigeCursorInvalido confere que o erro é 400 cursor_invalido e que a causa
// não vai na mensagem para o cliente.
func exigeCursorInvalido(t *testing.T, k Key, err error) {
	t.Helper()
	if k != nil {
		t.Errorf("chave devolvida para cursor inválido: %v", k)
	}
	e := errs.As(err)
	if e == nil || e.Kind != errs.KindBadRequest || e.Code != "cursor_invalido" {
		t.Fatalf("erro = %v", err)
	}
	if p := errs.ToProblem(err); strings.Contains(p.Detail, "assinatura") || strings.Contains(p.Detail, "payload") {
		t.Errorf("causa vazou para o cliente: %q", p.Detail)
	}
}

func TestCursorAdulterado(t *testing.T) {
	c := novoCodec(t, segredo)
	cur, _ := c.Encode(escopo, chave())
	parts := strings.Split(cur, ".")

	// Chave forjada: o cliente troca o PK pelo de outro usuário e mantém a assinatura.
	forjada, _ := json.Marshal(Key{"PK": "USER#outro", "SK": chave()["SK"]})
	outroPayload := base64.RawURLEncoding.EncodeToString(forjada)

	// Cursor válido em outra listagem do mesmo usuário.
	outroEscopo, _ := c.Encode("USER#u-1|2026-10|", chave())
	// Cursor gerado com outro segredo.
	outroSegredo, _ := novoCodec(t, bytes.Repeat([]byte("z"), 32)).Encode(escopo, chave())

	casos := map[string]string{
		"payload forjado":         parts[0] + "." + outroPayload + "." + parts[2],
		"assinatura trocada":      parts[0] + "." + parts[1] + "." + strings.Split(outroEscopo, ".")[2],
		"cursor de outro escopo":  outroEscopo,
		"cursor de outro segredo": outroSegredo,
		"truncado":                cur[:len(cur)-5],
		"sem assinatura":          parts[0] + "." + parts[1],
		"partes demais":           cur + ".x",
		"versão desconhecida":     "v2." + parts[1] + "." + parts[2],
		"assinatura não é base64": parts[0] + "." + parts[1] + ".***",
		"lixo":                    "abc",
		"longo demais":            "v1." + strings.Repeat("A", maxCursorLen) + "." + parts[2],
	}
	for nome, cursor := range casos {
		t.Run(nome, func(t *testing.T) {
			// Nos casos de outro escopo, o escopo esperado é o original.
			k, err := c.Decode(escopo, cursor)
			exigeCursorInvalido(t, k, err)
		})
	}

	t.Run("qualquer caractere alterado", func(t *testing.T) {
		const alfabeto = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
		for i := range len(cur) {
			if cur[i] == '.' {
				continue
			}
			troca := alfabeto[(strings.IndexByte(alfabeto, cur[i])+1)%len(alfabeto)]
			mut := cur[:i] + string(troca) + cur[i+1:]
			if k, err := c.Decode(escopo, mut); err == nil {
				t.Fatalf("posição %d alterada passou: %v", i, k)
			}
		}
	})
}

func TestPayloadAssinadoMasInvalido(t *testing.T) {
	// Assinado com o segredo certo, mas o conteúdo não é uma chave válida.
	// Só acontece com bug no servidor; mesmo assim vira 400, nunca panic.
	c := novoCodec(t, segredo)
	assinado := func(payload string) string { return version + "." + payload + "." + c.sign(escopo, payload) }
	casos := map[string]string{
		"payload não é base64": assinado("***"),
		"payload não é JSON":   assinado(base64.RawURLEncoding.EncodeToString([]byte("não é json"))),
		"chave vazia":          assinado(base64.RawURLEncoding.EncodeToString([]byte("{}"))),
	}
	for nome, cursor := range casos {
		t.Run(nome, func(t *testing.T) {
			k, err := c.Decode(escopo, cursor)
			exigeCursorInvalido(t, k, err)
		})
	}
}

func TestNewCodec(t *testing.T) {
	if _, err := NewCodec([]byte("curto")); err == nil {
		t.Error("esperava erro para segredo curto")
	}
	s := append([]byte(nil), segredo...)
	c := novoCodec(t, s)
	cur, _ := c.Encode(escopo, chave())
	s[0] = 'X' // alterar o slice original não pode mudar o Codec
	if _, err := c.Decode(escopo, cur); err != nil {
		t.Errorf("o Codec deve copiar o segredo: %v", err)
	}
}

func TestLimit(t *testing.T) {
	casos := map[int]int{-5: DefaultLimit, 0: DefaultLimit, 1: 1, 20: 20, MaxLimit: MaxLimit, MaxLimit + 1: MaxLimit}
	for in, quer := range casos {
		if got := Limit(in); got != quer {
			t.Errorf("Limit(%d) = %d, quer %d", in, got, quer)
		}
	}
}

func TestNewPage(t *testing.T) {
	b, _ := json.Marshal(New[int](nil, ""))
	if string(b) != `{"items":[]}` {
		t.Errorf("página vazia = %s", b)
	}
	b, _ = json.Marshal(New([]string{"a"}, "v1.x.y"))
	if string(b) != `{"items":["a"],"nextCursor":"v1.x.y"}` {
		t.Errorf("página = %s", b)
	}
}
