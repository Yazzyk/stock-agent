package test

import (
	"fmt"
	"math"
	"testing"

	"github.com/yazzyk/stock-agent/internal/backtest"
	"github.com/yazzyk/stock-agent/internal/market"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 造一段可复现的日线：先涨后跌再涨，保证有回撤也有趋势，便于观察未来函数。
func backtestBars(n int) []contract.Bar {
	bars := make([]contract.Bar, n)
	price := 10.0
	for i := 0; i < n; i++ {
		drift := 0.004 * math.Sin(float64(i)*0.22)
		price = price * (1 + drift)
		open := price * (1 - 0.002*math.Sin(float64(i)))
		high := math.Max(open, price) * 1.004
		low := math.Min(open, price) * 0.996
		bars[i] = contract.Bar{
			Date:   fmt.Sprintf("2026-%02d-%02d", 1+i/28, 1+i%28),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  price,
			Volume: 100000 + float64(i%5)*1000,
			Amount: price * (100000 + float64(i%5)*1000),
		}
	}
	return bars
}

func defaultOptions() backtest.Options {
	return backtest.Options{InitialCash: 100000, Cost: backtest.DefaultCost(), Warmup: 5}
}

// 无未来函数：把"未来"（第 cut 根之后）的价格改成完全不同的量级，
// 第 cut 根之前每一天的净值必须一模一样。
func TestBacktestNoLookahead(t *testing.T) {
	bars := backtestBars(80)
	const cut = 50

	full := backtest.Run("600000", bars, backtest.NewMA(3, 5), defaultOptions())

	tampered := append([]contract.Bar(nil), bars...)
	for i := cut; i < len(tampered); i++ {
		b := tampered[i]
		b.Open *= 3
		b.High *= 3
		b.Low *= 3
		b.Close *= 3
		b.Volume *= 4
		b.Amount *= 4
		tampered[i] = b
	}
	after := backtest.Run("600000", tampered, backtest.NewMA(3, 5), defaultOptions())

	if len(full.Equity) == 0 {
		t.Fatal("回测应产出净值曲线")
	}
	if len(after.Equity) != len(full.Equity) {
		t.Fatalf("净值曲线长度应一致（同一批 bar 数）：%d vs %d", len(full.Equity), len(after.Equity))
	}
	cutDate := bars[cut].Date
	compared := 0
	for i := 0; i < len(full.Equity) && i < len(after.Equity); i++ {
		if full.Equity[i].Date >= cutDate {
			break
		}
		if full.Equity[i].Date != after.Equity[i].Date {
			t.Fatalf("第 %d 天日期错位：%s vs %s", i, full.Equity[i].Date, after.Equity[i].Date)
		}
		if math.Abs(full.Equity[i].Equity-after.Equity[i].Equity) > 1e-6 {
			t.Fatalf("第 %d 天(%s) 净值受未来数据影响: %.2f vs %.2f（存在未来函数）",
				i, full.Equity[i].Date, full.Equity[i].Equity, after.Equity[i].Equity)
		}
		compared++
	}
	if compared < cut-5 {
		t.Fatalf("应对比 %d 天以上的历史净值，实际只对比了 %d 天", cut-5, compared)
	}
}

// 成本单调性：佣金费率、滑点上升，最终净值必须下降，成本合计必须上升。
func TestBacktestCostMonotonicity(t *testing.T) {
	bars := backtestBars(80)
	strat := backtest.NewMA(3, 5)

	base := defaultOptions()
	cheap := backtest.Run("600000", bars, strat, base)

	expensive := base
	expensive.Cost.CommissionRate = base.Cost.CommissionRate * 10
	rich := backtest.Run("600000", bars, strat, expensive)

	if rich.FinalEquity >= cheap.FinalEquity {
		t.Fatalf("佣金提高到 10 倍后净值应下降：cheap=%.2f rich=%.2f", cheap.FinalEquity, rich.FinalEquity)
	}
	if rich.Costs.Commission <= cheap.Costs.Commission {
		t.Fatalf("佣金合计应上升：cheap=%.2f rich=%.2f", cheap.Costs.Commission, rich.Costs.Commission)
	}

	slippery := base
	slippery.Cost.SlippageRate = base.Cost.SlippageRate * 5
	slip := backtest.Run("600000", bars, strat, slippery)
	if slip.FinalEquity >= cheap.FinalEquity {
		t.Fatalf("滑点提高后净值应下降：cheap=%.2f slip=%.2f", cheap.FinalEquity, slip.FinalEquity)
	}

	// 四项成本都必须出现（报告里不能只写佣金）
	if cheap.Costs.StampTax < 0 || cheap.Costs.Transfer < 0 || cheap.Costs.Slippage < 0 {
		t.Fatalf("成本明细不应为负: %+v", cheap.Costs)
	}
	if cheap.Costs.Total() <= 0 {
		t.Fatalf("有成交就应有成本，实际: %+v", cheap.Costs)
	}
}

// 最大回撤必须与净值序列自身算出的一致（同口径、可复核）。
func TestBacktestMaxDrawdownConsistent(t *testing.T) {
	bars := backtestBars(120)
	res := backtest.Run("600000", bars, backtest.NewMA(3, 8), defaultOptions())
	if len(res.Equity) < 3 {
		t.Fatalf("净值点数不足: %d", len(res.Equity))
	}
	series := make([]float64, len(res.Equity))
	for i, p := range res.Equity {
		series[i] = p.Equity
	}
	want := market.MaxDrawdown(series)
	// 报告口径：最大回撤四舍五入到 4 位小数（报告里显示为百分比 2 位）。
	// 因此 QA 侧按同一口径复核：round4(按净值算出的回撤) 必须与报告的完全相等。
	wantRounded := math.Round(want*10000) / 10000
	if math.Abs(res.MaxDrawdown-wantRounded) > 1e-12 {
		t.Fatalf("MaxDrawdown 与净值序列不一致：报告 %.12f，按净值算 %.12f（4 位小数应为 %.4f）",
			res.MaxDrawdown, want, wantRounded)
	}
	if math.Abs(res.MaxDrawdown-want) > 5e-5 {
		t.Fatalf("MaxDrawdown 与净值序列差超过四舍五入误差：报告 %.12f，按净值算 %.12f", res.MaxDrawdown, want)
	}
	if res.MaxDrawdown < 0 || res.MaxDrawdown > 1 {
		t.Fatalf("最大回撤应在 [0,1]，实际 %v", res.MaxDrawdown)
	}

	// 单调上涨 + 买入持有：只可能因为入场成本出现极小回撤，不应该出现明显回撤
	up := make([]contract.Bar, 40)
	for i := range up {
		p := 10 + float64(i)*0.1
		up[i] = contract.Bar{Date: fmt.Sprintf("2026-02-%02d", 1+i%28), Open: p, High: p, Low: p, Close: p, Volume: 1000, Amount: p * 1000}
	}
	buyHold := backtest.Run("600000", up, &backtest.BuyHold{}, defaultOptions())
	if buyHold.MaxDrawdown > 0.01 {
		t.Fatalf("单调上涨序列的回撤应只有入场成本量级（<1%%），实际 %v", buyHold.MaxDrawdown)
	}
	if buyHold.FinalEquity <= buyHold.InitialCash {
		t.Fatalf("单调上涨时买入持有应盈利：初始 %.2f 期末 %.2f", buyHold.InitialCash, buyHold.FinalEquity)
	}

	// 数据不足时不交易，净值等于初始资金，回撤为 0
	short := backtest.Run("600000", up[:2], &backtest.BuyHold{}, defaultOptions())
	if short.MaxDrawdown != 0 {
		t.Fatalf("K 线不足时应无回撤，实际 %v", short.MaxDrawdown)
	}
	if short.FinalEquity != short.InitialCash {
		t.Fatalf("K 线不足时净值应等于初始资金：%.2f vs %.2f", short.FinalEquity, short.InitialCash)
	}
	if len(short.Warnings) == 0 {
		t.Fatal("K 线不足应给出 Warnings 而不是静默返回")
	}
}
