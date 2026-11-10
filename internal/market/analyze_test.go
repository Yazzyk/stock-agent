package market

import (
	"math"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// waveBars 造一段「上涨回调再上涨」的行情，保证既有局部高点也有局部低点。
func waveBars(n int) []contract.Bar {
	bars := make([]contract.Bar, n)
	price := 10.0
	for i := 0; i < n; i++ {
		delta := 0.15
		if (i/5)%2 == 1 {
			delta = -0.12
		}
		open := price
		close := price + delta
		high := math.Max(open, close) + 0.05
		low := math.Min(open, close) - 0.05
		bars[i] = contract.Bar{Open: open, High: high, Low: low, Close: close, Volume: float64(1000 + i*10)}
		price = close
	}
	return bars
}

func TestAnalyzeSummaryFields(t *testing.T) {
	bars := waveBars(80)
	got := Analyze(bars)
	if got.Last.Close != bars[len(bars)-1].Close {
		t.Fatalf("Last 应为最后一根 K 线，实际 %+v", got.Last)
	}
	cases := []struct {
		name  string
		text  string
		words []string
	}{
		{"趋势", got.Summary.Trend, []string{"排列", "震荡"}},
		{"均线", got.Summary.MA, []string{"收盘", "MA"}},
		{"RSI", got.Summary.RSI, []string{"RSI"}},
		{"MACD", got.Summary.MACD, []string{"DIF", "DEA"}},
		{"ATR", got.Summary.ATR, []string{"ATR"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.TrimSpace(tc.text) == "" {
				t.Fatalf("%s 摘要不能为空", tc.name)
			}
			hit := false
			for _, w := range tc.words {
				if strings.Contains(tc.text, w) {
					hit = true
				}
			}
			if !hit {
				t.Fatalf("%s 摘要缺少关键信息: %q", tc.name, tc.text)
			}
		})
	}
}

func TestAnalyzeIndicatorsAligned(t *testing.T) {
	bars := waveBars(80)
	got := Analyze(bars)
	series := map[string]Series{
		"ma5": got.Indicators.MA5, "ma10": got.Indicators.MA10,
		"ma20": got.Indicators.MA20, "ma60": got.Indicators.MA60,
		"volume": got.Indicators.Volume,
	}
	for name, s := range series {
		if len(s) != len(bars) {
			t.Fatalf("%s 长度 = %d，期望 %d", name, len(s), len(bars))
		}
	}
	if !math.IsNaN(float64(got.Indicators.MA60[58])) || math.IsNaN(float64(got.Indicators.MA60[59])) {
		t.Fatal("MA60 应在第 60 根（下标 59）才有首个有效值")
	}
}

func TestAnalyzeEmptyAndShort(t *testing.T) {
	if got := Analyze(nil); got.Last.Close != 0 || len(got.Patterns) != 0 {
		t.Fatalf("空输入应返回零值，实际 %+v", got)
	}
	short := []contract.Bar{{Open: 10, High: 10.2, Low: 9.8, Close: 10, Volume: 1000}}
	got := Analyze(short)
	if got.Last.Close != 10 {
		t.Fatalf("单根输入 Last 应填充，实际 %+v", got.Last)
	}
	if !strings.Contains(got.Summary.Trend, "样本") {
		t.Fatalf("样本不足时应明确提示，实际 %q", got.Summary.Trend)
	}
}

func TestStructureLevels(t *testing.T) {
	bars := waveBars(120)
	lastClose := bars[len(bars)-1].Close
	support, resistance := Structure(bars, 120)
	if len(support) > 3 || len(resistance) > 3 {
		t.Fatalf("结构位最多各 3 个，实际 support=%v resistance=%v", support, resistance)
	}
	for _, v := range support {
		if !(v < lastClose) {
			t.Fatalf("支撑位 %v 应低于收盘 %v", v, lastClose)
		}
	}
	for _, v := range resistance {
		if !(v > lastClose) {
			t.Fatalf("阻力位 %v 应高于收盘 %v", v, lastClose)
		}
	}
	// 支撑按由近到远（降序）、阻力按由近到远（升序）
	for i := 1; i < len(support); i++ {
		if support[i] > support[i-1] {
			t.Fatalf("支撑位应按降序排列: %v", support)
		}
	}
	for i := 1; i < len(resistance); i++ {
		if resistance[i] < resistance[i-1] {
			t.Fatalf("阻力位应按升序排列: %v", resistance)
		}
	}
}

func TestStructureFallback(t *testing.T) {
	// 单调上涨只有最低价可当支撑，最高价在收盘时已被包含
	bars := make([]contract.Bar, 0, 10)
	for i := 0; i < 10; i++ {
		p := 10 + float64(i)*0.1
		bars = append(bars, contract.Bar{Open: p, High: p + 0.05, Low: p - 0.05, Close: p + 0.04, Volume: 1000})
	}
	support, _ := Structure(bars, 120)
	if len(support) == 0 {
		t.Fatal("无分形极值时也应给出区间最低价作为支撑")
	}
	if support[0] <= 0 {
		t.Fatalf("支撑位非法: %v", support)
	}
}

func TestComputeIndicatorsVolume(t *testing.T) {
	bars := []contract.Bar{{Volume: 100}, {Volume: 200}}
	ind := ComputeIndicators(bars)
	if len(ind.Volume) != 2 || ind.Volume[0] != 100 || ind.Volume[1] != 200 {
		t.Fatalf("成交量序列应原样输出，实际 %+v", ind.Volume)
	}
	if len(ind.MA5) != 2 {
		t.Fatalf("MA5 长度 = %d，期望 2", len(ind.MA5))
	}
}
