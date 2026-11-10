package backtest

import (
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// makeBars 生成确定性日线：closes 依次赋值，开高低收围绕收盘构造。
func makeBars(closes []float64) []contract.Bar {
	bars := make([]contract.Bar, 0, len(closes))
	for i, c := range closes {
		prev := c
		if i > 0 {
			prev = closes[i-1]
		}
		open := prev * 1.001
		high := c * 1.01
		if open > high {
			high = open
		}
		low := c * 0.99
		if open < low {
			low = open
		}
		bars = append(bars, contract.Bar{
			Date:   dayDate(i),
			Open:   round2(open),
			High:   round2(high),
			Low:    round2(low),
			Close:  round2(c),
			Volume: 10000,
		})
	}
	return bars
}

func dayDate(i int) string {
	// 2026-01-01 起顺序编号（只用于展示与排序，不参与计算）
	day := 1 + i%28
	month := 1 + (i/28)%12
	return strings.Join([]string{"2026", pad(month), pad(day)}, "-")
}

func pad(v int) string {
	if v < 10 {
		return "0" + string(rune('0'+v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}

func trendingBars(n int, start, step float64) []contract.Bar {
	closes := make([]float64, n)
	for i := range closes {
		closes[i] = start + step*float64(i)
	}
	return makeBars(closes)
}

// ---------- 无未来函数 ----------

func TestNoLookaheadFutureDataDoesNotChangePast(t *testing.T) {
	base := trendingBars(120, 10, 0.1)
	perturbed := append([]contract.Bar(nil), base...)
	// 篡改第 60 根之后的所有价格（未来数据）
	for i := 60; i < len(perturbed); i++ {
		perturbed[i].Open *= 1.5
		perturbed[i].Close *= 1.5
		perturbed[i].High *= 1.5
		perturbed[i].Low *= 1.5
	}
	opt := Options{InitialCash: 100000, Cost: DefaultCost()}
	a := Run("600000", base, NewMA(5, 20), opt)
	b := Run("600000", perturbed, NewMA(5, 20), opt)

	// equity[k] 对应 bars[k+1]：从第 60 根开始被篡改，故只比较 k+1 < 60 的净值。
	for i := 0; i+1 < 60 && i < len(a.Equity) && i < len(b.Equity); i++ {
		if a.Equity[i].Equity != b.Equity[i].Equity {
			t.Fatalf("第 %s 净值受未来数据影响: %v vs %v（存在未来函数）", a.Equity[i].Date, a.Equity[i].Equity, b.Equity[i].Equity)
		}
	}
}

func TestSignalAtICannotAffectTradeAtI(t *testing.T) {
	// 用一个"每天满仓"的策略，验证第一次建仓发生在第 2 根开盘（i=0 信号 → i=1 成交）。
	bars := trendingBars(10, 10, 0)
	opt := Options{InitialCash: 100000, Cost: CostModel{CommissionRate: 0, MinCommission: 0, StampTaxRate: 0, TransferRate: 0, SlippageRate: 0}, Warmup: 1}
	res := Run("600000", bars, BuyHold{}, opt)
	if len(res.Equity) < 3 {
		t.Fatalf("净值点太少: %d", len(res.Equity))
	}
	// equity[0] 对应第 2 根 bar：warmup=1 时 i=1 产生首个信号，只能在 i=2 开盘成交。
	if res.Equity[0].Shares != 0 {
		t.Fatalf("第 2 根不应持仓（首个信号尚未成交）: %+v", res.Equity[0])
	}
	if res.Equity[1].Shares == 0 {
		t.Fatalf("信号次日应完成建仓: %+v", res.Equity[1])
	}
}

// ---------- T+1 与涨跌停 ----------

type flipStrategy struct{}

func (flipStrategy) Name() string { return "flip" }
func (flipStrategy) OnBar(_ []contract.Bar, i int) float64 {
	if i%2 == 0 {
		return 1
	}
	return 0
}

func TestTPlusOneBlocksSameDaySell(t *testing.T) {
	bars := trendingBars(10, 10, 0)
	opt := Options{InitialCash: 100000, Cost: CostModel{CommissionRate: 0.0003, MinCommission: 5, StampTaxRate: 0.0005, TransferRate: 0.00001, SlippageRate: 0}, Warmup: 1}
	res := Run("600000", bars, flipStrategy{}, opt)
	for _, tr := range res.Trades {
		if tr.EntryDate == tr.ExitDate {
			t.Fatalf("买入当日即卖出，违反 T+1: %+v", tr)
		}
	}
}

func TestLimitUpBlocksBuy(t *testing.T) {
	bars := makeBars([]float64{10, 10, 10.4, 10.4, 10.4})
	// 第 3 根（索引 2）开盘价 = 涨幅 10% 涨停 → 无法买入
	bars[2].Open = 11.0
	bars[2].High = 11.0
	bars[2].Low = 10.5
	bars[2].Close = 11.0
	opt := Options{InitialCash: 100000, Cost: CostModel{CommissionRate: 0.0003, MinCommission: 5, StampTaxRate: 0.0005, TransferRate: 0.00001, SlippageRate: 0}, Warmup: 1}
	res := Run("600000", bars, BuyHold{}, opt)
	if res.Equity[1].Shares != 0 {
		t.Fatalf("涨停开盘不应成交: %+v", res.Equity[1])
	}
	if !hasWarning(res.Warnings, "涨停") {
		t.Fatalf("应记录涨停跳过提示: %v", res.Warnings)
	}
}

func TestSuspendedBarSkipsTrading(t *testing.T) {
	bars := trendingBars(6, 10, 0)
	bars[2].Volume = 0 // 停牌
	opt := Options{InitialCash: 100000, Cost: DefaultCost(), Warmup: 1}
	res := Run("600000", bars, BuyHold{}, opt)
	// equity[1] 对应 bars[2]（停牌日）
	if res.Equity[1].Shares != 0 {
		t.Fatalf("停牌日不应成交: %+v", res.Equity[1])
	}
	if res.Equity[1].Equity != res.Equity[0].Equity {
		t.Fatalf("停牌日净值应按最近收盘不变: %v vs %v", res.Equity[0].Equity, res.Equity[1].Equity)
	}
}

// ---------- 成本 ----------

func TestCostMonotonicity(t *testing.T) {
	bars := makeBars(func() []float64 {
		out := make([]float64, 120)
		for i := range out {
			// 制造震荡，让策略频繁交易
			out[i] = 10 + float64(i%10)
		}
		return out
	}())
	free := Options{InitialCash: 100000, Cost: CostModel{CommissionRate: 0, MinCommission: 0, StampTaxRate: 0, TransferRate: 0, SlippageRate: 0}, Warmup: 1}
	paid := Options{InitialCash: 100000, Cost: DefaultCost(), Warmup: 1}
	a := Run("600000", bars, flipStrategy{}, free)
	b := Run("600000", bars, flipStrategy{}, paid)
	if b.FinalEquity > a.FinalEquity+1e-6 {
		t.Fatalf("计入成本后期末权益不应更高: 免费 %v, 收费 %v", a.FinalEquity, b.FinalEquity)
	}
	if b.Costs.Total() <= 0 {
		t.Fatalf("成本明细应 > 0: %+v", b.Costs)
	}
	if b.Costs.Slippage <= 0 {
		t.Fatalf("滑点应被单独记录: %+v", b.Costs)
	}
}

func TestReportContainsCostLines(t *testing.T) {
	bars := trendingBars(40, 10, 0.2)
	res := Run("600000", bars, NewMA(5, 20), Options{InitialCash: 100000, Cost: DefaultCost(), Warmup: 1})
	rep := res.Report()
	for _, key := range []string{"佣金", "印花税", "过户费", "滑点"} {
		if !strings.Contains(rep, key) {
			t.Fatalf("报告缺少成本项 %q:\n%s", key, rep)
		}
	}
	if !strings.Contains(rep, "最大回撤") {
		t.Fatal("报告缺少风险指标")
	}
}

// ---------- 涨跌停比例 ----------

func TestLimitPctFor(t *testing.T) {
	cases := map[string]float64{
		"600000": 0.10, "000001": 0.10, "sh600000": 0.10,
		"300750": 0.20, "301001": 0.20, "688981": 0.20,
		"830799": 0.30, "430047": 0.30,
		"600000ST": 0.05, "ST600000": 0.05,
	}
	for code, want := range cases {
		if got := LimitPctFor(code); got != want {
			t.Fatalf("LimitPctFor(%q) = %v, want %v", code, got, want)
		}
	}
}

// ---------- walk-forward ----------

func TestWalkForwardSplitsAndRanks(t *testing.T) {
	bars := makeBars(func() []float64 {
		out := make([]float64, 300)
		for i := range out {
			out[i] = 10 + 3*sinApprox(float64(i)/9) // 周期波动
		}
		return out
	}())
	wf := WalkForward("600000", bars, "ma", DefaultGrid("ma"), 3, Options{InitialCash: 100000, Cost: DefaultCost(), Warmup: 1})
	if len(wf.Folds) != 3 {
		t.Fatalf("折数 = %d, want 3（%s）", len(wf.Folds), wf.Note)
	}
	prevEnd := ""
	for i, f := range wf.Folds {
		if f.TrainEnd >= f.TestStart {
			t.Fatalf("第 %d 折样本内与样本外重叠: %s ~ %s", i+1, f.TrainEnd, f.TestStart)
		}
		if prevEnd != "" && f.TrainStart < prevEnd {
			t.Fatalf("第 %d 折窗口倒退", i+1)
		}
		prevEnd = f.TrainEnd
		if len(f.Best) == 0 {
			t.Fatalf("第 %d 折没有选出的参数", i+1)
		}
	}
	if wf.Verdict == "" {
		t.Fatal("缺少过拟合结论")
	}
	if !strings.Contains(wf.Report(), "样本外") {
		t.Fatal("报告缺少样本外结论")
	}
}

func TestWalkForwardRejectsInsufficientBars(t *testing.T) {
	bars := trendingBars(30, 10, 0.1)
	wf := WalkForward("600000", bars, "ma", DefaultGrid("ma"), 3, Options{})
	if len(wf.Folds) != 0 || wf.Note == "" {
		t.Fatalf("数据不足时应给出说明而不是硬跑: %+v", wf)
	}
}

func TestBuildFallsBackToBuyHold(t *testing.T) {
	if got := Build("unknown", nil).Name(); got != "买入持有" {
		t.Fatalf("未知策略应回落到买入持有, got %q", got)
	}
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func sinApprox(x float64) float64 {
	// 泰勒展开到 7 阶，避免为了测试引入 math.Sin 的精度依赖
	t := x
	for t > 3.141592653589793 {
		t -= 2 * 3.141592653589793
	}
	s := t
	term := t
	for i := 1; i <= 3; i++ {
		term *= -t * t / float64((2*i)*(2*i+1))
		s += term
	}
	return s
}
