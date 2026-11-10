package test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/yazzyk/stock-agent/internal/market"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 回测不变量：最大回撤的精确值（docs/ROADMAP.md 第 2 步钉死的口径）。
func TestMaxDrawdownExact(t *testing.T) {
	cases := []struct {
		name   string
		equity []float64
		want   float64
	}{
		{"路线图样例 0.25", []float64{1, 1.2, 0.9, 1.1}, 0.25},
		{"单调上涨无回撤", []float64{1, 1.1, 1.2, 1.3}, 0},
		{"横盘无回撤", []float64{1, 1, 1, 1}, 0},
		{"腰斩", []float64{1, 0.5, 0.75}, 0.5},
		{"先涨后回撤取最大", []float64{1, 2, 1, 3, 1.5}, 0.5},
		{"空序列", nil, 0},
		{"单点", []float64{1.23}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := market.MaxDrawdown(tc.equity); math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("MaxDrawdown(%v) = %.12f，期望 %.12f", tc.equity, got, tc.want)
			}
		})
	}
}

// 收益序列：长度 len-1，且前值为 0 跳过（避免除零）。
func TestReturnsShape(t *testing.T) {
	prices := []float64{1, 2, 4, 3}
	got := market.Returns(prices)
	if len(got) != len(prices)-1 {
		t.Fatalf("Returns 长度应为 %d，实际 %d", len(prices)-1, len(got))
	}
	want := []float64{1, 1, -0.25}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12 {
			t.Fatalf("Returns[%d] = %v，期望 %v", i, got[i], want[i])
		}
	}
	if market.Returns([]float64{1}) != nil {
		t.Fatal("不足两个点应返回 nil")
	}
}

// 波动率：常数收益序列波动为 0；离散度更大的序列波动更大。
func TestAnnualVolatilityMonotonic(t *testing.T) {
	zero := make([]float64, 60)
	if v := market.AnnualVolatility(zero, 244); v != 0 {
		t.Fatalf("全零收益波动应为 0，实际 %v", v)
	}
	constant := make([]float64, 60)
	for i := range constant {
		constant[i] = 0.001
	}
	// 常数序列的样本标准差在浮点下约 1e-17，按容差断言而不是要求精确 0
	if v := market.AnnualVolatility(constant, 244); v > 1e-12 {
		t.Fatalf("常数收益波动应≈0，实际 %v", v)
	}
	small := []float64{0.01, -0.01, 0.012, -0.008, 0.011, -0.009}
	large := []float64{0.05, -0.05, 0.06, -0.04, 0.055, -0.045}
	vSmall := market.AnnualVolatility(small, 244)
	vLarge := market.AnnualVolatility(large, 244)
	if !(vLarge > vSmall) {
		t.Fatalf("离散度更大的序列年化波动应更大：small=%.6f large=%.6f", vSmall, vLarge)
	}
}

// ---------- 未来函数检测 ----------
//
// 不变量：第 i 根的输出只能依赖 [0..i]。
// 两种证伪方式：① 只喂前缀数据；② 把未来数据改得面目全非。
// 两种情况下第 i 根的输出都必须与全量计算完全一致。

func sameNum(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	if math.IsInf(a, 0) || math.IsInf(b, 0) {
		return a == b
	}
	scale := math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
	return math.Abs(a-b) <= 1e-9*scale
}

func syntheticCloses(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 10 + 2*math.Sin(float64(i)*0.35) + 0.05*float64(i%7)
	}
	return out
}

func barsFromCloses(closes []float64) []contract.Bar {
	bars := make([]contract.Bar, len(closes))
	for i, c := range closes {
		open := c * (1 - 0.004*math.Sin(float64(i)))
		high := math.Max(open, c) * 1.006
		low := math.Min(open, c) * 0.994
		vol := 1000 + float64(i%7)*37
		bars[i] = contract.Bar{
			Date:   fmt.Sprintf("2026-01-%02d", i%28+1),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  c,
			Volume: vol,
			Amount: c * vol,
		}
	}
	return bars
}

func cutPoints(n int) []int {
	pts := []int{7}
	if n > 40 {
		pts = append(pts, 25)
	}
	pts = append(pts, n-1)
	seen := map[int]bool{}
	out := make([]int, 0, len(pts))
	for _, p := range pts {
		if p > 0 && p < n && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func checkSeriesNoLookahead(t *testing.T, name string, closes []float64, fn func([]float64) []float64) {
	t.Helper()
	full := fn(closes)
	if len(full) != len(closes) {
		t.Fatalf("%s 输出长度 %d ≠ 输入长度 %d", name, len(full), len(closes))
	}
	for _, cut := range cutPoints(len(closes)) {
		prefix := fn(closes[:cut])
		if len(prefix) != cut {
			t.Fatalf("%s 前缀输出长度 %d ≠ %d", name, len(prefix), cut)
		}
		if !sameNum(prefix[cut-1], full[cut-1]) {
			t.Fatalf("%s 用到未来数据：仅前 %d 根算得 %v，全量算得 %v", name, cut, prefix[cut-1], full[cut-1])
		}
		tampered := append([]float64(nil), closes...)
		for i := cut; i < len(tampered); i++ {
			tampered[i] = tampered[i]*3 + 7
		}
		out := fn(tampered)
		if !sameNum(out[cut-1], full[cut-1]) {
			t.Fatalf("%s 受未来数据影响：篡改第 %d 根之后，第 %d 根从 %v 变成 %v",
				name, cut, cut, full[cut-1], out[cut-1])
		}
	}
}

func checkBarsNoLookahead(t *testing.T, name string, bars []contract.Bar, fn func([]contract.Bar) []float64) {
	t.Helper()
	full := fn(bars)
	if len(full) != len(bars) {
		t.Fatalf("%s 输出长度 %d ≠ 输入长度 %d", name, len(full), len(bars))
	}
	for _, cut := range cutPoints(len(bars)) {
		prefix := fn(bars[:cut])
		if len(prefix) != cut {
			t.Fatalf("%s 前缀输出长度 %d ≠ %d", name, len(prefix), cut)
		}
		if !sameNum(prefix[cut-1], full[cut-1]) {
			t.Fatalf("%s 用到未来数据：仅前 %d 根算得 %v，全量算得 %v", name, cut, prefix[cut-1], full[cut-1])
		}
		tampered := append([]contract.Bar(nil), bars...)
		for i := cut; i < len(tampered); i++ {
			b := tampered[i]
			b.Open *= 3
			b.High *= 3
			b.Low *= 3
			b.Close *= 3
			b.Volume *= 5
			b.Amount *= 5
			tampered[i] = b
		}
		out := fn(tampered)
		if !sameNum(out[cut-1], full[cut-1]) {
			t.Fatalf("%s 受未来数据影响：篡改第 %d 根之后，第 %d 根从 %v 变成 %v",
				name, cut, cut, full[cut-1], out[cut-1])
		}
	}
}

// 所有指标都不得偷看未来，且输出长度必须等于输入长度。
func TestIndicatorsNoLookahead(t *testing.T) {
	closes := syntheticCloses(80)
	bars := barsFromCloses(closes)

	seriesCases := []struct {
		name string
		fn   func([]float64) []float64
	}{
		{"SMA(5)", func(v []float64) []float64 { return market.SMA(v, 5) }},
		{"SMA(20)", func(v []float64) []float64 { return market.SMA(v, 20) }},
		{"EMA(12)", func(v []float64) []float64 { return market.EMA(v, 12) }},
		{"RSI(6)", func(v []float64) []float64 { return market.RSI(v, 6) }},
		{"RSI(14)", func(v []float64) []float64 { return market.RSI(v, 14) }},
		{"MACD.DIF", func(v []float64) []float64 { d, _, _ := market.MACD(v, 12, 26, 9); return d }},
		{"MACD.DEA", func(v []float64) []float64 { _, d, _ := market.MACD(v, 12, 26, 9); return d }},
		{"MACD.BAR", func(v []float64) []float64 { _, _, m := market.MACD(v, 12, 26, 9); return m }},
		{"BOLL.MID", func(v []float64) []float64 { m, _, _ := market.BOLL(v, 20, 2); return m }},
		{"BOLL.UPPER", func(v []float64) []float64 { _, u, _ := market.BOLL(v, 20, 2); return u }},
		{"BOLL.LOWER", func(v []float64) []float64 { _, _, l := market.BOLL(v, 20, 2); return l }},
	}
	for _, tc := range seriesCases {
		t.Run(tc.name, func(t *testing.T) {
			checkSeriesNoLookahead(t, tc.name, closes, tc.fn)
		})
	}

	barsCases := []struct {
		name string
		fn   func([]contract.Bar) []float64
	}{
		{"ATR(14)", func(b []contract.Bar) []float64 { return market.ATR(b, 14) }},
		{"KDJ.K", func(b []contract.Bar) []float64 { k, _, _ := market.KDJ(b, 9, 3, 3); return k }},
		{"KDJ.D", func(b []contract.Bar) []float64 { _, d, _ := market.KDJ(b, 9, 3, 3); return d }},
		{"KDJ.J", func(b []contract.Bar) []float64 { _, _, j := market.KDJ(b, 9, 3, 3); return j }},
		{"VolumeRatio(5)", func(b []contract.Bar) []float64 { return market.VolumeRatio(b, 5) }},
	}
	for _, tc := range barsCases {
		t.Run(tc.name, func(t *testing.T) {
			checkBarsNoLookahead(t, tc.name, bars, tc.fn)
		})
	}
}

// 窗口大于输入时，输出依然是等长全 NaN，而不是空切片或 panic。
func TestIndicatorLengthInvariant(t *testing.T) {
	closes := syntheticCloses(10)
	bars := barsFromCloses(closes)
	cases := []struct {
		name string
		out  []float64
	}{
		{"SMA 窗口超大", market.SMA(closes, 99)},
		{"EMA 窗口超大", market.EMA(closes, 99)},
		{"RSI 窗口超大", market.RSI(closes, 99)},
		{"ATR 窗口超大", market.ATR(bars, 99)},
	}
	for _, tc := range cases {
		if len(tc.out) != len(closes) {
			t.Fatalf("%s 输出长度应为 %d，实际 %d", tc.name, len(closes), len(tc.out))
		}
		for i, v := range tc.out {
			if !math.IsNaN(v) {
				t.Fatalf("%s 第 %d 个应为 NaN（数据不足），实际 %v", tc.name, i, v)
			}
		}
	}
}

// Series 的 JSON 契约：NaN 序列化成 null，前端据此断线而不是画到 0。
func TestSeriesJSON(t *testing.T) {
	raw, err := json.Marshal(market.Series{1.5, math.NaN(), 3})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(raw) != "[1.5,null,3]" {
		t.Fatalf("NaN 应序列化为 null，实际 %s", raw)
	}
	raw, err = json.Marshal(market.Series(nil))
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if string(raw) != "null" {
		t.Fatalf("nil 序列应输出 null，实际 %s", raw)
	}
}
