package market

import (
	"math"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// hasPattern 判断结果里是否包含指定名称与下标的形态。
func hasPattern(patterns []Pattern, name string, index int) (Pattern, bool) {
	for _, p := range patterns {
		if p.Name == name && p.Index == index {
			return p, true
		}
	}
	return Pattern{}, false
}

func TestPatternDoji(t *testing.T) {
	bars := []contract.Bar{
		{Open: 10, High: 10.2, Low: 9.8, Close: 10.01, Volume: 1000},
	}
	got := Patterns(bars)
	p, ok := hasPattern(got, PatternDoji, 0)
	if !ok {
		t.Fatalf("应识别出十字星，实际: %+v", got)
	}
	if p.Confidence <= 0 || p.Confidence > 1 {
		t.Fatalf("置信度应在 (0,1]，实际 %v", p.Confidence)
	}
	if !strings.Contains(p.Note, "振幅") {
		t.Fatalf("说明应包含量化依据，实际 %q", p.Note)
	}

	t.Run("实体够大不算十字星", func(t *testing.T) {
		bars := []contract.Bar{{Open: 10, High: 10.3, Low: 9.9, Close: 10.2, Volume: 1000}}
		if _, ok := hasPattern(Patterns(bars), PatternDoji, 0); ok {
			t.Fatal("实体占振幅 3/4，不应识别为十字星")
		}
	})
}

func TestPatternHammer(t *testing.T) {
	bars := []contract.Bar{
		{Open: 10, High: 10.06, Low: 9.7, Close: 10.05, Volume: 1000},
	}
	got := Patterns(bars)
	p, ok := hasPattern(got, PatternHammer, 0)
	if !ok {
		t.Fatalf("应识别出锤子线，实际: %+v", got)
	}
	if p.Confidence < 0.4 || p.Confidence > 1 {
		t.Fatalf("锤子线置信度异常: %v", p.Confidence)
	}

	t.Run("上影过长不算锤子", func(t *testing.T) {
		bars := []contract.Bar{{Open: 10, High: 10.5, Low: 9.7, Close: 10.05, Volume: 1000}}
		if _, ok := hasPattern(Patterns(bars), PatternHammer, 0); ok {
			t.Fatal("上影占振幅过半，不应识别为锤子线")
		}
	})
}

func TestPatternEngulfing(t *testing.T) {
	t.Run("看涨吞没", func(t *testing.T) {
		bars := []contract.Bar{
			{Open: 10.5, High: 10.6, Low: 10.1, Close: 10.2, Volume: 1000},
			{Open: 10.15, High: 10.6, Low: 10.1, Close: 10.55, Volume: 1200},
		}
		p, ok := hasPattern(Patterns(bars), PatternBullishEngulfing, 1)
		if !ok {
			t.Fatalf("应识别出看涨吞没: %+v", Patterns(bars))
		}
		if p.Confidence <= 0 || p.Confidence > 1 {
			t.Fatalf("置信度异常: %v", p.Confidence)
		}
	})

	t.Run("看跌吞没", func(t *testing.T) {
		bars := []contract.Bar{
			{Open: 10.2, High: 10.6, Low: 10.1, Close: 10.5, Volume: 1000},
			{Open: 10.55, High: 10.6, Low: 10.1, Close: 10.15, Volume: 1200},
		}
		if _, ok := hasPattern(Patterns(bars), PatternBearishEngulfing, 1); !ok {
			t.Fatalf("应识别出看跌吞没: %+v", Patterns(bars))
		}
	})

	t.Run("实体未吞没不算", func(t *testing.T) {
		bars := []contract.Bar{
			{Open: 10.5, High: 10.6, Low: 10.1, Close: 10.2, Volume: 1000},
			{Open: 10.19, High: 10.4, Low: 10.1, Close: 10.3, Volume: 1200},
		}
		if _, ok := hasPattern(Patterns(bars), PatternBullishEngulfing, 1); ok {
			t.Fatal("当前实体小于前一根，不应识别为吞没")
		}
	})
}

func TestPatternThreeWhiteSoldiers(t *testing.T) {
	bars := []contract.Bar{
		{Open: 10.0, High: 10.2, Low: 9.9, Close: 10.1, Volume: 1000},
		{Open: 10.05, High: 10.4, Low: 10.0, Close: 10.3, Volume: 1100},
		{Open: 10.25, High: 10.7, Low: 10.2, Close: 10.6, Volume: 1300},
	}
	p, ok := hasPattern(Patterns(bars), PatternThreeWhiteSoldiers, 2)
	if !ok {
		t.Fatalf("应识别出三连阳: %+v", Patterns(bars))
	}
	if p.Confidence < 0.5 || p.Confidence > 1 {
		t.Fatalf("三连阳置信度异常: %v", p.Confidence)
	}

	t.Run("中间夹阴线不算", func(t *testing.T) {
		bars := []contract.Bar{
			{Open: 10.0, High: 10.2, Low: 9.9, Close: 10.1, Volume: 1000},
			{Open: 10.4, High: 10.45, Low: 10.2, Close: 10.3, Volume: 1100},
			{Open: 10.3, High: 10.7, Low: 10.25, Close: 10.6, Volume: 1300},
		}
		if _, ok := hasPattern(Patterns(bars), PatternThreeWhiteSoldiers, 2); ok {
			t.Fatal("第二根是阴线，不应识别为三连阳")
		}
	})
}

func TestPatternVolumeBreakout(t *testing.T) {
	build := func(lastClose, lastVolume float64) []contract.Bar {
		bars := make([]contract.Bar, 0, 30)
		for i := 0; i < 29; i++ {
			bars = append(bars, contract.Bar{Open: 10, High: 10.1, Low: 9.9, Close: 10, Volume: 1000})
		}
		bars = append(bars, contract.Bar{Open: 10.05, High: lastClose + 0.05, Low: 10, Close: lastClose, Volume: lastVolume})
		return bars
	}

	t.Run("放量突破", func(t *testing.T) {
		p, ok := hasPattern(Patterns(build(10.5, 3000)), PatternVolumeBreakout, 29)
		if !ok {
			t.Fatalf("应识别出放量突破: %+v", Patterns(build(10.5, 3000)))
		}
		if p.Confidence <= 0 || p.Confidence > 1 {
			t.Fatalf("置信度异常: %v", p.Confidence)
		}
	})

	t.Run("缩量突破不算", func(t *testing.T) {
		if _, ok := hasPattern(Patterns(build(10.5, 1200)), PatternVolumeBreakout, 29); ok {
			t.Fatal("量能不足 1.5 倍，不应识别为放量突破")
		}
	})

	t.Run("未破前高不算", func(t *testing.T) {
		if _, ok := hasPattern(Patterns(build(10.05, 5000)), PatternVolumeBreakout, 29); ok {
			t.Fatal("收盘未创新高，不应识别为放量突破")
		}
	})
}

func TestPatternsRecentWindows(t *testing.T) {
	bars := make([]contract.Bar, 0, 40)
	bars = append(bars, contract.Bar{Open: 10, High: 10.2, Low: 9.8, Close: 10.01, Volume: 1000})
	// 填充用的普通阳线：实体占振幅 3/4，不会命中十字星或锤子线
	for i := 1; i < 39; i++ {
		bars = append(bars, contract.Bar{Open: 10, High: 10.07, Low: 9.99, Close: 10.06, Volume: 1000})
	}
	// 最后一个点再造一个十字星
	bars = append(bars, contract.Bar{Open: 10, High: 10.2, Low: 9.8, Close: 10.01, Volume: 1000})

	all := Patterns(bars)
	if len(all) < 2 {
		t.Fatalf("应识别出至少两个十字星，实际 %+v", all)
	}
	recent := PatternsRecent(bars, 5)
	for _, p := range recent {
		if p.Index < len(bars)-5 {
			t.Fatalf("窗口外的形态不应返回: %+v", p)
		}
	}
	if len(recent) != 1 {
		t.Fatalf("最近 5 根只应命中最后一个十字星，实际 %+v", recent)
	}
	if len(PatternsRecent(bars, 0)) != len(all) {
		t.Fatal("lookback<=0 应等价于全量扫描")
	}
}

func TestPatternsConfidenceBounded(t *testing.T) {
	// 混合极值行情：置信度必须在 [0,1] 内，不允许 NaN
	bars := make([]contract.Bar, 0, 40)
	for i := 0; i < 40; i++ {
		open := 10 + math.Sin(float64(i))*2
		close := 10 + math.Cos(float64(i))*2
		high := math.Max(open, close) + 0.4
		low := math.Min(open, close) - 0.4
		bars = append(bars, contract.Bar{Open: open, High: high, Low: low, Close: close, Volume: float64(1000 + i*50)})
	}
	for _, p := range Patterns(bars) {
		if math.IsNaN(p.Confidence) || p.Confidence < 0 || p.Confidence > 1 {
			t.Fatalf("置信度越界: %+v", p)
		}
		if p.Index < 0 || p.Index >= len(bars) {
			t.Fatalf("下标越界: %+v", p)
		}
		if strings.TrimSpace(p.Note) == "" {
			t.Fatalf("形态必须带说明: %+v", p)
		}
	}
}
