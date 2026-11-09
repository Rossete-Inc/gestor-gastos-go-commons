package money

import (
	"errors"
	"math"
	"testing"
)

func TestSomaDeCentavosDaSpec(t *testing.T) {
	// Cenário "Soma de centavos" da spec lancamento-gastos:
	// R$ 0,10 + R$ 0,20 é exatamente R$ 0,30.
	total, err := Sum(10, 20)
	if err != nil || total != 30 {
		t.Fatalf("Sum(10, 20) = %d, %v", total, err)
	}
	// Em float o mesmo cálculo dá 0.30000000000000004; é por isso que valor
	// monetário nunca passa por float.
	a, b := 0.10, 0.20
	if a+b == 0.30 {
		t.Error("esperava que float64 errasse 0.10 + 0.20")
	}
}

func TestSemPerdaDePrecisao(t *testing.T) {
	// Um milhão de gastos de R$ 0,01 somam exatamente R$ 10.000,00.
	vals := make([]int64, 1_000_000)
	for i := range vals {
		vals[i] = 1
	}
	if total, err := Sum(vals...); err != nil || total != 1_000_000 {
		t.Errorf("total = %d, %v", total, err)
	}

	// Valores grandes: 90 mil lançamentos no teto da spec (R$ 1.000.000,00)
	// somam R$ 90 bilhões exatos.
	const teto = int64(100_000_000)
	total, err := SumBy(make([]struct{}, 90_000), func(struct{}) int64 { return teto })
	if err != nil || total != 9_000_000_000_000 {
		t.Errorf("total = %d, %v", total, err)
	}

	// Perto do limite de int64, onde float64 já perde unidades.
	quase := int64(math.MaxInt64 - 1)
	if total, err := Add(quase, 1); err != nil || total != math.MaxInt64 {
		t.Errorf("Add(MaxInt64-1, 1) = %d, %v", total, err)
	}
	if float64(quase)+1 != float64(quase) {
		t.Error("esperava que float64 não distinguisse MaxInt64-1 de MaxInt64")
	}
}

func TestEstouro(t *testing.T) {
	casos := map[string][]int64{
		"positivo": {math.MaxInt64, 1},
		"negativo": {math.MinInt64, -1},
		"no meio":  {math.MaxInt64 / 2, math.MaxInt64 / 2, 2},
	}
	for nome, vals := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := Sum(vals...); !errors.Is(err, ErrOverflow) {
				t.Errorf("Sum(%v) erro = %v", vals, err)
			}
		})
	}
	if _, err := SumBy([]int64{math.MaxInt64, 1}, func(v int64) int64 { return v }); !errors.Is(err, ErrOverflow) {
		t.Errorf("SumBy erro = %v", err)
	}
	if m, err := SumByKey([]int64{math.MaxInt64, 1}, func(int64) string { return "x" }, func(v int64) int64 { return v }); !errors.Is(err, ErrOverflow) || m != nil {
		t.Errorf("SumByKey = %v, %v", m, err)
	}
}

func TestNegativosESemValores(t *testing.T) {
	if total, err := Sum(); err != nil || total != 0 {
		t.Errorf("Sum() = %d, %v", total, err)
	}
	if total, err := Sum(500, -200, -300); err != nil || total != 0 {
		t.Errorf("Sum com estorno = %d, %v", total, err)
	}
}

type gasto struct {
	grupo     string
	categoria string
	centavos  int64
}

func TestSumByKey(t *testing.T) {
	gastos := []gasto{
		{"CARTAO", "alimentacao", 75_000},
		{"CARTAO", "transporte", 10},
		{"CARTAO", "transporte", 20},
		{"FIXO", "aluguel", 150_000},
		{"CARTAO", "transporte", 24_970},
	}
	porGrupo, err := SumByKey(gastos, func(g gasto) string { return g.grupo }, func(g gasto) int64 { return g.centavos })
	if err != nil {
		t.Fatal(err)
	}
	if porGrupo["CARTAO"] != 100_000 || porGrupo["FIXO"] != 150_000 || len(porGrupo) != 2 {
		t.Errorf("por grupo = %v", porGrupo)
	}
	porCategoria, _ := SumByKey(gastos, func(g gasto) string { return g.categoria }, func(g gasto) int64 { return g.centavos })
	if porCategoria["transporte"] != 25_000 {
		t.Errorf("por categoria = %v", porCategoria)
	}
	vazio, err := SumByKey([]gasto(nil), func(g gasto) string { return g.grupo }, func(g gasto) int64 { return g.centavos })
	if err != nil || vazio == nil || len(vazio) != 0 {
		t.Errorf("sem itens = %v, %v", vazio, err)
	}
}
