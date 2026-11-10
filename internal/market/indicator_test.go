package market

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// seq 造 1..n 的等差价格序列，便于手算期望值。
func seq(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i + 1)
	}
	return out
}

func near(t *testing.T, got, want, tol float64, label string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v，期望 %v（容差 %v）", label, got, want, tol)
	}
}

func TestSMA(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		n      int
		want   map[int]float64 // 下标 → 期望值
		nanAt  []int
	}{
		{
			name:   "长度不足全是缺口",
			values: []float64{1, 2},
			n:      3,
			want:   map[int]float64{},
			nanAt:  []int{0, 1},
		},
		{
			name:   "1..10 的 3 日均线",
			values: seq(10),
			n:      3,
			want:   map[int]float64{2: 2, 3: 3, 5: 5, 9: 9},
			nanAt:  []int{0, 1},
		},
		{
			name:   "n=1 等于原值",
			values: []float64{3, 1, 4},
			n:      1,
			want:   map[int]float64{0: 3, 1: 1, 2: 4},
		},
		{
			name:   "非法 n 返回全缺口",
			values: seq(5),
			n:      0,
			want:   map[int]float64{},
			nanAt:  []int{0, 1, 2, 3, 4},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SMA(tc.values, tc.n)
			if len(got) != len(tc.values) {
				t.Fatalf("长度 = %d，期望与输入等长 %d", len(got), len(tc.values))
			}
			for i, want := range tc.want {
				near(t, got[i], want, 1e-9, "SMA 下标")
			}
			for _, i := range tc.nanAt {
				if !math.IsNaN(got[i]) {
					t.Fatalf("下标 %d 应为 NaN，实际 %v", i, got[i])
				}
			}
		})
	}
}

func TestEMA(t *testing.T) {
	got := EMA(seq(10), 3)
	// 种子 = (1+2+3)/3 = 2，之后每次 ema = v*0.5 + prev*0.5，因等差数列恰好等于下标值。
	for i := 2; i < 10; i++ {
		near(t, got[i], float64(i), 1e-9, "EMA 下标")
	}
	if !math.IsNaN(got[0]) || !math.IsNaN(got[1]) {
		t.Fatal("EMA 前两位应为 NaN")
	}

	t.Run("n 大于长度全缺口", func(t *testing.T) {
		out := EMA(seq(3), 5)
		for i, v := range out {
			if !math.IsNaN(v) {
				t.Fatalf("下标 %d 应为 NaN，实际 %v", i, v)
			}
		}
	})
}

func TestRSI(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		n      int
		idx    int
		want   float64
	}{
		{"全涨为 100", seq(20), 14, 19, 100},
		{"全跌为 0", reversed(seq(20)), 14, 19, 0},
		{"完全不动为 50", flat(20, 10), 14, 19, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RSI(tc.values, tc.n)
			if len(got) != len(tc.values) {
				t.Fatalf("长度 = %d，期望 %d", len(got), len(tc.values))
			}
			for i := 0; i < tc.n; i++ {
				if !math.IsNaN(got[i]) {
					t.Fatalf("下标 %d 应为 NaN，实际 %v", i, got[i])
				}
			}
			near(t, got[tc.idx], tc.want, 1e-9, "RSI")
		})
	}

	t.Run("涨跌交替落在 0..100", func(t *testing.T) {
		values := make([]float64, 40)
		for i := range values {
			if i%2 == 0 {
				values[i] = 10 + float64(i)*0.1
			} else {
				values[i] = 10 + float64(i)*0.1 - 0.3
			}
		}
		got := RSI(values, 14)
		for i := 14; i < len(values); i++ {
			if !(got[i] > 0 && got[i] < 100) {
				t.Fatalf("下标 %d 的 RSI = %v，应严格落在 (0,100)", i, got[i])
			}
		}
	})
}

func TestMACD(t *testing.T) {
	values := seq(60)
	dif, dea, macd := MACD(values, 12, 26, 9)
	for name, s := range map[string][]float64{"DIF": dif, "DEA": dea, "MACD": macd} {
		if len(s) != len(values) {
			t.Fatalf("%s 长度 = %d，期望 %d", name, len(s), len(values))
		}
	}
	// 单调上涨：DIF 应为正，且红柱（DIF-DEA）为正
	if !(dif[len(dif)-1] > 0) {
		t.Fatalf("单调上涨序列 DIF 应 > 0，实际 %v", dif[len(dif)-1])
	}
	near(t, macd[len(macd)-1], dif[len(dif)-1]-dea[len(dea)-1], 1e-12, "柱 = DIF - DEA")
	// 缺口边界：DIF 在 slow-1=25 之前无值，DEA 再滞后 signal-1=8 根
	if !math.IsNaN(dif[24]) {
		t.Fatal("DIF 在 slow-1 之前应为 NaN")
	}
	if math.IsNaN(dif[25]) {
		t.Fatal("DIF 在 slow-1 处应有值")
	}
	if !math.IsNaN(dea[32]) || math.IsNaN(dea[33]) {
		t.Fatalf("DEA 首个有效下标应为 25+9-1=33，实际 dea[32]=%v dea[33]=%v", dea[32], dea[33])
	}

	t.Run("样本不足全缺口", func(t *testing.T) {
		d, e, m := MACD(seq(10), 12, 26, 9)
		for i := range d {
			if !math.IsNaN(d[i]) || !math.IsNaN(e[i]) || !math.IsNaN(m[i]) {
				t.Fatalf("下标 %d 应全为 NaN", i)
			}
		}
	})
}

func TestATR(t *testing.T) {
	bars := []contract.Bar{
		{High: 10, Low: 8, Close: 9},
		{High: 11, Low: 9.5, Close: 10.5},
		{High: 12, Low: 10, Close: 11},
		{High: 20, Low: 19, Close: 19.5},
	}
	got := ATR(bars, 3)
	if len(got) != len(bars) {
		t.Fatalf("长度 = %d，期望 %d", len(got), len(bars))
	}
	if !math.IsNaN(got[0]) || !math.IsNaN(got[1]) {
		t.Fatal("ATR 前两根应为 NaN")
	}
	near(t, got[2], 2, 1e-9, "ATR 种子")
	// 第 4 根跳空：TR = max(1, |20-11|, |19-11|) = 9 → ATR = (2*2 + 9)/3
	near(t, got[3], 13.0/3.0, 1e-9, "ATR 递推")
}

func TestBOLL(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5}
	mid, upper, lower := BOLL(values, 5, 2)
	near(t, mid[4], 3, 1e-9, "中轨")
	sd := math.Sqrt(2)
	near(t, upper[4], 3+2*sd, 1e-9, "上轨")
	near(t, lower[4], 3-2*sd, 1e-9, "下轨")

	t.Run("等值序列上下轨收敛于中轨", func(t *testing.T) {
		flatBars := flatBarsOf(10, 5)
		closes := Closes(flatBars)
		m, u, l := BOLL(closes, 5, 2)
		near(t, m[4], 10, 1e-9, "中轨")
		near(t, u[4], 10, 1e-9, "上轨")
		near(t, l[4], 10, 1e-9, "下轨")
	})
}

func TestKDJ(t *testing.T) {
	bars := flatBarsOf(10, 20)
	k, d, j := KDJ(bars, 9, 3, 3)
	if len(k) != len(bars) || len(d) != len(bars) || len(j) != len(bars) {
		t.Fatal("KDJ 长度必须与输入等长")
	}
	for i := 0; i < 8; i++ {
		if !math.IsNaN(k[i]) {
			t.Fatalf("下标 %d 应为 NaN", i)
		}
	}
	// 一字横盘：RSV=50，K=D=50，J=50
	near(t, k[19], 50, 1e-9, "K")
	near(t, d[19], 50, 1e-9, "D")
	near(t, j[19], 50, 1e-9, "J")
}

func TestMaxDrawdown(t *testing.T) {
	cases := []struct {
		name   string
		equity []float64
		want   float64
	}{
		{"给定净值回撤 25%", []float64{100, 150, 112.5}, 0.25},
		{"单调上涨无回撤", []float64{1, 2, 3, 4}, 0},
		{"持续下跌", []float64{100, 50}, 0.5},
		{"单点与空序列", []float64{100}, 0},
		{"空序列", nil, 0},
		{"先新高再回撤取最大", []float64{100, 200, 150, 180, 90}, 0.55},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			near(t, MaxDrawdown(tc.equity), tc.want, 1e-9, "最大回撤")
		})
	}
}

func TestAnnualVolatility(t *testing.T) {
	t.Run("恒定收益波动为 0", func(t *testing.T) {
		near(t, AnnualVolatility([]float64{0.01, 0.01, 0.01}, 252), 0, 1e-12, "年化波动")
	})
	t.Run("正负交替的已知值", func(t *testing.T) {
		got := AnnualVolatility([]float64{0.01, -0.01, 0.01, -0.01}, 252)
		want := math.Sqrt(0.0004/3) * math.Sqrt(252)
		near(t, got, want, 1e-9, "年化波动")
	})
	t.Run("样本不足为 0", func(t *testing.T) {
		near(t, AnnualVolatility(nil, 252), 0, 0, "年化波动")
	})
	t.Run("Returns 长度正确", func(t *testing.T) {
		got := Returns([]float64{10, 11, 12.1})
		if len(got) != 2 {
			t.Fatalf("Returns 长度 = %d，期望 2", len(got))
		}
		near(t, got[0], 0.1, 1e-12, "第一日收益")
	})
}

func TestVolumeRatio(t *testing.T) {
	bars := []contract.Bar{
		{Volume: 100}, {Volume: 100}, {Volume: 200}, {Volume: 50},
	}
	got := VolumeRatio(bars, 2)
	if len(got) != len(bars) {
		t.Fatalf("长度 = %d，期望 %d", len(got), len(bars))
	}
	if !math.IsNaN(got[0]) || !math.IsNaN(got[1]) {
		t.Fatal("量比前两根应为 NaN")
	}
	near(t, got[2], 2, 1e-9, "量比")
	near(t, got[3], 50.0/150.0, 1e-9, "量比")
}

func TestNoFutureData(t *testing.T) {
	full := seq(60)
	// 篡改后半段（第 30 根起）为极端值，历史段指标必须一字不变
	tampered := append([]float64{}, full...)
	for i := 30; i < len(tampered); i++ {
		tampered[i] = 1e6
	}
	cases := []struct {
		name string
		fn   func([]float64) []float64
	}{
		{"SMA", func(v []float64) []float64 { return SMA(v, 5) }},
		{"EMA", func(v []float64) []float64 { return EMA(v, 5) }},
		{"RSI", func(v []float64) []float64 { return RSI(v, 14) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.fn(full)
			b := tc.fn(tampered)
			for i := 0; i < 30; i++ {
				if math.IsNaN(a[i]) && math.IsNaN(b[i]) {
					continue
				}
				near(t, b[i], a[i], 0, tc.name+" 历史段被未来数据污染")
			}
		})
	}

	t.Run("指标与截断前缀一致", func(t *testing.T) {
		cli := seq(50)
		maFull := SMA(cli, 10)
		for _, cut := range []int{20, 30, 40} {
			maCut := SMA(cli[:cut+1], 10)
			near(t, maCut[cut], maFull[cut], 1e-12, "截断前缀 MA 与全量不一致")
		}
		rsiFull := RSI(cli, 14)
		for _, cut := range []int{20, 30, 40} {
			rsiCut := RSI(cli[:cut+1], 14)
			near(t, rsiCut[cut], rsiFull[cut], 1e-12, "截断前缀 RSI 与全量不一致")
		}
	})
}

func TestSeriesJSONNulls(t *testing.T) {
	s := Series(SMA(seq(5), 3))
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	want := "[null,null,2,3,4]"
	if string(raw) != want {
		t.Fatalf("JSON = %s，期望 %s", raw, want)
	}
	if raw, _ := json.Marshal(Series(nil)); string(raw) != "null" {
		t.Fatalf("nil 序列应输出 null，实际 %s", raw)
	}
}

func reversed(v []float64) []float64 {
	out := make([]float64, len(v))
	for i := range v {
		out[i] = v[len(v)-1-i]
	}
	return out
}

func flat(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func flatBarsOf(price float64, n int) []contract.Bar {
	bars := make([]contract.Bar, n)
	for i := range bars {
		bars[i] = contract.Bar{Open: price, High: price, Low: price, Close: price, Volume: 1000}
	}
	return bars
}
