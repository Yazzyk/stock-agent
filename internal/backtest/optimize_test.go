package backtest

import (
	"math"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// waveBars 生成带周期的波动日线，保证均线/RSI 等策略能产生交易与差异化的参数表现。
func waveBars(n int) []contract.Bar {
	closes := make([]float64, n)
	for i := range closes {
		closes[i] = 20 + 4*math.Sin(float64(i)/6) + 0.01*float64(i)
	}
	return makeBars(closes)
}

// TestOptimizeReturnsStableCandidates 覆盖参数优化的主路径：
// 候选非空、推荐参数来自候选、稳健性在 0~1、walk-forward 与报告都存在。
func TestOptimizeReturnsStableCandidates(t *testing.T) {
	bars := waveBars(240)
	res := Optimize("600000", bars, "ma", DefaultGrid("ma"), 3,
		Options{InitialCash: 100000, Cost: DefaultCost(), Warmup: 2})
	if res.Note != "" {
		t.Fatalf("样本充足时不应有 note: %s", res.Note)
	}
	if len(res.Candidates) == 0 {
		t.Fatal("候选为空")
	}
	if len(res.Candidates) > maxOptimizeCandidates {
		t.Fatalf("候选超过上限: %d", len(res.Candidates))
	}
	if res.Best == nil {
		t.Fatal("缺少推荐参数")
	}
	found := false
	bestKey := paramsString(res.Best.Params)
	for _, c := range res.Candidates {
		if c.Stability < 0 || c.Stability > 1 {
			t.Fatalf("稳健性越界: %+v", c)
		}
		if paramsString(c.Params) == bestKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("推荐参数不在候选中: %s", bestKey)
	}
	if res.WalkForward == nil || len(res.WalkForward.Folds) == 0 {
		t.Fatalf("推荐参数应跑 walk-forward: %+v", res.WalkForward)
	}
	for _, key := range []string{"推荐参数", "邻域稳健性", "Walk-forward", "警告"} {
		if !strings.Contains(res.Report, key) {
			t.Fatalf("报告缺少 %q", key)
		}
	}
}

// TestOptimizeHandlesShortSample 覆盖样本不足：返回 note 与报告，不 panic、不产生候选。
func TestOptimizeHandlesShortSample(t *testing.T) {
	res := Optimize("600000", trendingBars(20, 10, 0.1), "ma", DefaultGrid("ma"), 3, Options{})
	if res.Note == "" {
		t.Fatal("样本不足应给出 note")
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("样本不足不应产生候选: %d", len(res.Candidates))
	}
	if res.Best != nil {
		t.Fatal("样本不足不应给出推荐参数")
	}
	if !strings.Contains(res.Report, "不足") {
		t.Fatalf("报告应说明样本不足: %s", res.Report)
	}
}

// TestOptimizeBuyHoldHasNoNeighbors 覆盖无邻域场景：网格只有一个点，
// 稳健性应为 0，推荐参数仍是该点。
func TestOptimizeBuyHoldHasNoNeighbors(t *testing.T) {
	res := Optimize("600000", waveBars(120), "buyhold", DefaultGrid("buyhold"), 2,
		Options{InitialCash: 100000, Cost: DefaultCost(), Warmup: 2})
	if len(res.Candidates) != 1 {
		t.Fatalf("买入持有应只有一个候选: %d", len(res.Candidates))
	}
	if res.Candidates[0].Stability != 0 {
		t.Fatalf("无邻域时稳健性应为 0: %v", res.Candidates[0].Stability)
	}
	if res.Best == nil || len(res.Best.Params) != 0 {
		t.Fatalf("推荐参数应为空参数集: %+v", res.Best)
	}
}
