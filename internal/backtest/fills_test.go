package backtest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// toggleStrategy 每天在「满仓/空仓」之间切换，用来制造大量成交（验证 fills 截断）。
type toggleStrategy struct{}

func (toggleStrategy) Name() string { return "toggle" }

func (toggleStrategy) OnBar(_ []contract.Bar, i int) float64 {
	if i%2 == 0 {
		return 1
	}
	return 0
}

// TestFillsRecordBuyAndSell 覆盖成交明细的基本语义：
// 买卖各一条、日期是成交日（信号次日）、价格与股数为正、reason 说明动作。
func TestFillsRecordBuyAndSell(t *testing.T) {
	bars := trendingBars(40, 10, 0.1)
	opt := Options{InitialCash: 100000, Cost: zeroCost(), Warmup: 1}
	res := Run("600000", bars, toggleStrategy{}, opt)
	if len(res.Fills) < 2 {
		t.Fatalf("成交明细过少: %+v", res.Fills)
	}
	first, second := res.Fills[0], res.Fills[1]
	if first.Side != "buy" || second.Side != "sell" {
		t.Fatalf("首两笔方向 = %q/%q，期望 buy/sell", first.Side, second.Side)
	}
	// 第 i 根产生的信号只能在第 i+1 根开盘成交：warmup=1 时 i=2 首次给出满仓信号，
	// 因此首笔成交日必须是 bars[3]（而不是 bars[2]，更不能是 bars[2] 之前的任何一天）。
	if first.Date != bars[3].Date {
		t.Fatalf("首笔成交日 = %q，期望 %q（信号次日）", first.Date, bars[3].Date)
	}
	if first.Qty <= 0 || first.Price <= 0 {
		t.Fatalf("成交数量/价格非法: %+v", first)
	}
	if first.Reason != "开仓" || second.Reason != "清仓" {
		t.Fatalf("成交原因 = %q/%q，期望 开仓/清仓", first.Reason, second.Reason)
	}
	// 成交日期必须落在 K 线日期集合里，不能凭空造日期
	dates := make(map[string]bool, len(bars))
	for _, bar := range bars {
		dates[bar.Date] = true
	}
	for _, fill := range res.Fills {
		if !dates[fill.Date] {
			t.Fatalf("成交日 %q 不在 K 线里", fill.Date)
		}
		if fill.Side != "buy" && fill.Side != "sell" {
			t.Fatalf("side 只能是 buy/sell，实际 %q", fill.Side)
		}
	}
}

// TestFillsTruncatedAt500 钉住契约 §3.7.1 的上限：超出截断且留一条提示，不静默丢。
func TestFillsTruncatedAt500(t *testing.T) {
	bars := trendingBars(1500, 10, 0.01)
	opt := Options{InitialCash: 1000000, Cost: zeroCost(), Warmup: 1}
	res := Run("600000", bars, toggleStrategy{}, opt)
	if len(res.Fills) != maxFills {
		t.Fatalf("成交明细 = %d 条，期望截断到 %d", len(res.Fills), maxFills)
	}
	found := false
	for _, warning := range res.Warnings {
		if strings.Contains(warning, "截断") {
			found = true
		}
	}
	if !found {
		t.Fatalf("截断时必须留下提示: %v", res.Warnings)
	}
}

// TestFillsDoNotBreakTradeShape 保证旧字段语义没被改：fills 是新增键，trades 仍是往返交易。
func TestFillsDoNotBreakTradeShape(t *testing.T) {
	bars := trendingBars(60, 10, 0.1)
	res := Run("600000", bars, toggleStrategy{}, Options{InitialCash: 100000, Cost: zeroCost(), Warmup: 1})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	fills, ok := payload["fills"].([]any)
	if !ok || len(fills) == 0 {
		t.Fatalf("JSON 里缺少 fills: %s", raw)
	}
	one, _ := fills[0].(map[string]any)
	for _, key := range []string{"date", "side", "price", "qty", "reason"} {
		if _, exists := one[key]; !exists {
			t.Fatalf("fill 缺少字段 %q: %v", key, one)
		}
	}
	roundTrips, ok := payload["trades"].([]any)
	if !ok || len(roundTrips) == 0 {
		t.Fatalf("旧的 trades（往返交易）不能消失: %s", raw)
	}
	first, _ := roundTrips[0].(map[string]any)
	if _, exists := first["entryDate"]; !exists {
		t.Fatalf("trades 的往返语义被改了: %v", first)
	}
}

func zeroCost() CostModel {
	return CostModel{CommissionRate: 0, MinCommission: 0, StampTaxRate: 0, TransferRate: 0, SlippageRate: 0}
}
