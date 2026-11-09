// Package money soma valores em centavos de real (int64) sem perda de
// precisão.
//
// Valores monetários nunca passam por float: R$ 0,10 + R$ 0,20 são
// 10 + 20 = 30 centavos, exatamente R$ 0,30. As somas detectam estouro de
// int64 em vez de dar a volta silenciosamente.
package money

import (
	"errors"
	"math"
)

// ErrOverflow indica que a soma não cabe em int64.
var ErrOverflow = errors.New("money: soma de centavos estourou int64")

// Add soma dois valores em centavos.
func Add(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// Sum soma valores em centavos. Sem valores, devolve 0.
func Sum(values ...int64) (int64, error) {
	var total int64
	for _, v := range values {
		var err error
		if total, err = Add(total, v); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// SumBy soma o valor em centavos de cada item, por exemplo o total de
// gastos do mês.
func SumBy[T any](items []T, value func(T) int64) (int64, error) {
	var total int64
	for _, it := range items {
		var err error
		if total, err = Add(total, value(it)); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// SumByKey soma os valores agrupados por chave, por exemplo os totais por
// grupo ou por categoria. Sem itens, devolve um mapa vazio.
func SumByKey[T any, K comparable](items []T, key func(T) K, value func(T) int64) (map[K]int64, error) {
	totals := make(map[K]int64)
	for _, it := range items {
		k := key(it)
		t, err := Add(totals[k], value(it))
		if err != nil {
			return nil, err
		}
		totals[k] = t
	}
	return totals, nil
}
