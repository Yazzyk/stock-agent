// Package backtest 实现诚实的 A 股日线回测：T+1、涨跌停不可成交、停牌跳过、真实成本、无未来函数。
//
// 三条不可让步的规则：
//  1. 第 i 日收盘产生的信号，只能在第 i+1 日开盘成交（引擎保证，不依赖策略自律）；
//  2. 买入当日不可卖出（T+1）；
//  3. 成本必须计入：佣金（万三、最低 5 元）、印花税（卖出 0.05%）、过户费（双边 0.001%）、滑点（单边 0.1%）。
package backtest

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/internal/market"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

// CostModel 是 A 股交易成本参数，默认值与 docs/DOMAIN.md §4.3 一致。
type CostModel struct {
	CommissionRate float64 `json:"commissionRate"` // 佣金费率（双边）
	MinCommission  float64 `json:"minCommission"`  // 单笔最低佣金（元）
	StampTaxRate   float64 `json:"stampTaxRate"`   // 印花税（仅卖出）
	TransferRate   float64 `json:"transferRate"`   // 过户费（双边）
	SlippageRate   float64 `json:"slippageRate"`   // 滑点（单边）
}

// DefaultCost 返回 2023-08-28 之后的 A 股默认成本。
func DefaultCost() CostModel {
	return CostModel{
		CommissionRate: 0.0003,
		MinCommission:  5,
		StampTaxRate:   0.0005,
		TransferRate:   0.00001,
		SlippageRate:   0.001,
	}
}

func (c CostModel) validate() CostModel {
	if c.CommissionRate < 0 || c.StampTaxRate < 0 || c.TransferRate < 0 || c.SlippageRate < 0 {
		return DefaultCost()
	}
	if c.CommissionRate == 0 && c.MinCommission == 0 && c.StampTaxRate == 0 {
		return DefaultCost()
	}
	return c
}

// Strategy 只在 OnBar 里看到 bars[:i+1]，返回目标仓位（0..1）。
// 状态型策略（如 RSI 反转）自己维护状态，但不得缓存未来数据。
type Strategy interface {
	Name() string
	OnBar(bars []contract.Bar, i int) float64
}

// Options 是回测参数。
type Options struct {
	InitialCash float64
	Cost        CostModel
	// LimitPct 返回该标的的涨跌停比例（0.1/0.2/0.3/0.05）；为 nil 时按代码推断。
	LimitPct func(code string) float64
	// Warmup 是跳过的最少 bar 数，避免指标未成形就交易。
	Warmup int
}

// Trade 是一笔完整交易（建仓到清仓）。
type Trade struct {
	EntryDate  string  `json:"entryDate"`
	ExitDate   string  `json:"exitDate"`
	EntryPrice float64 `json:"entryPrice"`
	ExitPrice  float64 `json:"exitPrice"`
	Shares     int     `json:"shares"`
	PnL        float64 `json:"pnl"`
	ReturnPct  float64 `json:"returnPct"`
	Days       int     `json:"days"`
}

// Fill 是一笔实际成交（开仓/加仓/减仓/清仓各算一笔），供前端在 K 线上画买卖标记。
// 与 Trade（建仓到清仓的往返）互补而不是替代：Trade 回答「这笔赚了多少」，
// Fill 回答「哪天、什么价位、买卖了多少」。
type Fill struct {
	Date   string  `json:"date"`
	Side   string  `json:"side"` // buy | sell
	Price  float64 `json:"price"`
	Qty    int     `json:"qty"`
	Reason string  `json:"reason,omitempty"`
}

// maxFills 是成交明细的条数上限（契约 §3.7.1）：超出截断并留一条提示，
// 否则十年日线的动量策略能产出几万条，前端 canvas 画不动、JSON 也传不完。
const maxFills = 500

// EquityPoint 是每日净值。
type EquityPoint struct {
	Date     string  `json:"date"`
	Equity   float64 `json:"equity"`
	Cash     float64 `json:"cash"`
	Position float64 `json:"position"`
	Shares   int     `json:"shares"`
}

// CostBreakdown 是成本明细（回测报告必须展示这四行）。
type CostBreakdown struct {
	Commission float64 `json:"commission"`
	StampTax   float64 `json:"stampTax"`
	Transfer   float64 `json:"transfer"`
	Slippage   float64 `json:"slippage"`
}

// Total 返回四项成本合计。
func (c CostBreakdown) Total() float64 {
	return c.Commission + c.StampTax + c.Transfer + c.Slippage
}

// Result 是一次回测的完整结果。
type Result struct {
	Strategy    string  `json:"strategy"`
	Code        string  `json:"code"`
	Start       string  `json:"start"`
	End         string  `json:"end"`
	Bars        int     `json:"bars"`
	InitialCash float64 `json:"initialCash"`
	FinalEquity float64 `json:"finalEquity"`
	TotalReturn float64 `json:"totalReturn"`
	CAGR        float64 `json:"cagr"`
	MaxDrawdown float64 `json:"maxDrawdown"`
	Sharpe      float64 `json:"sharpe"`
	Sortino     float64 `json:"sortino"`
	WinRate     float64 `json:"winRate"`
	Turnover    float64 `json:"turnover"`
	TradeCount  int     `json:"tradeCount"`
	Trades      []Trade `json:"trades"`
	// Fills 是逐笔成交（买卖各一条，≤500 条），供前端画买卖点；Trades 语义不变。
	Fills  []Fill        `json:"fills,omitempty"`
	Equity []EquityPoint `json:"equity"`
	Costs  CostBreakdown `json:"costs"`
	// Warnings 记录降级事实（例如数据不足、涨跌停导致错过信号）。
	Warnings []string `json:"warnings,omitempty"`
}

// Run 在给定日线上跑一个策略。bars 必须按日期升序。
func Run(code string, bars []contract.Bar, strat Strategy, opt Options) Result {
	if opt.InitialCash <= 0 {
		opt.InitialCash = 100000
	}
	opt.Cost = opt.Cost.validate()
	if opt.LimitPct == nil {
		opt.LimitPct = LimitPctFor
	}
	res := Result{Strategy: strat.Name(), Code: code, InitialCash: opt.InitialCash, Bars: len(bars)}
	if len(bars) < 3 {
		res.Warnings = append(res.Warnings, "K 线不足 3 根，无法回测")
		res.FinalEquity = opt.InitialCash
		return res
	}
	res.Start, res.End = bars[0].Date, bars[len(bars)-1].Date

	cash := opt.InitialCash
	var shares int
	var buyDate string
	var entryPrice float64
	var entryDate string
	warmup := opt.Warmup
	if warmup < 1 {
		warmup = 1
	}

	prevClose := bars[0].Close
	for i := 0; i < len(bars)-1; i++ {
		next := bars[i+1]
		// 停牌日不交易，只记账（净仓位按最近收盘估值）。
		if next.Volume <= 0 {
			res.appendEquity(next, cash, shares, next.Close)
			prevClose = next.Close
			continue
		}
		limit := opt.LimitPct(code)
		upLimit := round2(prevClose * (1 + limit))
		downLimit := round2(prevClose * (1 - limit))

		target := 0.0
		if i >= warmup {
			target = clamp01(strat.OnBar(bars, i))
		}
		equityAtOpen := cash + float64(shares)*next.Open
		wantShares := int((equityAtOpen * target) / next.Open / 100 * 100) // 整手
		if wantShares < 0 {
			wantShares = 0
		}
		// 满仓时滑点与手续费会让"按权益买入"超出可用现金，这里按现金上限裁剪，
		// 否则 target=1 永远买不进（真实交易也会先算可用资金）。
		buyPrice := next.Open * (1 + opt.Cost.SlippageRate)
		if buyPrice > 0 {
			maxAffordable := int(cash/(buyPrice*(1+opt.Cost.CommissionRate+opt.Cost.TransferRate))/100) * 100
			if wantShares > maxAffordable {
				wantShares = maxAffordable
			}
		}

		switch {
		case wantShares > shares:
			// 买入：开盘涨停买不进
			if next.Open >= upLimit-1e-9 {
				res.Warnings = appendUnique(res.Warnings, "涨停开盘无法买入，信号被跳过")
			} else {
				buy := wantShares - shares
				price := next.Open * (1 + opt.Cost.SlippageRate)
				amount := price * float64(buy)
				commission := math.Max(amount*opt.Cost.CommissionRate, opt.Cost.MinCommission)
				transfer := amount * opt.Cost.TransferRate
				slippage := (price - next.Open) * float64(buy)
				cost := commission + transfer
				if amount+cost <= cash {
					reason := "开仓"
					if shares > 0 {
						reason = "加仓"
					}
					res.appendFill(Fill{Date: next.Date, Side: "buy", Price: round2(price), Qty: buy, Reason: reason})
					cash -= amount + cost
					shares = wantShares
					res.Costs.Commission += commission
					res.Costs.Transfer += transfer
					res.Costs.Slippage += slippage
					if buyDate != next.Date {
						entryDate = next.Date
						entryPrice = price
					}
					buyDate = next.Date
				}
			}
		case wantShares < shares:
			// 卖出：T+1 限制 + 开盘跌停卖不出
			if buyDate == next.Date {
				res.Warnings = appendUnique(res.Warnings, "T+1：买入当日不可卖出")
			} else if next.Open <= downLimit+1e-9 {
				res.Warnings = appendUnique(res.Warnings, "跌停开盘无法卖出，信号被跳过")
			} else {
				sell := shares - wantShares
				price := next.Open * (1 - opt.Cost.SlippageRate)
				amount := price * float64(sell)
				commission := math.Max(amount*opt.Cost.CommissionRate, opt.Cost.MinCommission)
				stamp := amount * opt.Cost.StampTaxRate
				transfer := amount * opt.Cost.TransferRate
				slippage := (next.Open - price) * float64(sell)
				reason := "减仓"
				if wantShares == 0 {
					reason = "清仓"
				}
				res.appendFill(Fill{Date: next.Date, Side: "sell", Price: round2(price), Qty: sell, Reason: reason})
				cash += amount - commission - stamp - transfer
				shares = wantShares
				res.Costs.Commission += commission
				res.Costs.StampTax += stamp
				res.Costs.Transfer += transfer
				res.Costs.Slippage += slippage
				if shares == 0 && entryPrice > 0 {
					pnl := (price-entryPrice)*float64(sell) - commission - stamp - transfer
					res.Trades = append(res.Trades, Trade{
						EntryDate: entryDate, ExitDate: next.Date,
						EntryPrice: round2(entryPrice), ExitPrice: round2(price),
						Shares: sell, PnL: round2(pnl),
						ReturnPct: round4((price - entryPrice) / entryPrice),
						Days:      daysBetween(entryDate, next.Date),
					})
					entryPrice = 0
					entryDate = ""
					buyDate = ""
				}
			}
		}
		res.appendEquity(next, cash, shares, next.Close)
		prevClose = next.Close
	}

	// 收尾：仍持仓则按最后收盘估值，不计清仓成本（避免虚构成交）。
	res.FinalEquity = cash + float64(shares)*bars[len(bars)-1].Close
	if shares > 0 {
		res.Warnings = appendUnique(res.Warnings, "回测结束仍持有仓位，按最后收盘价估值")
	}
	res.computeMetrics()
	return res
}

// appendFill 记录一笔成交；超过上限只提示一次，不静默丢数据。
func (r *Result) appendFill(fill Fill) {
	if len(r.Fills) >= maxFills {
		r.Warnings = appendUnique(r.Warnings, fmt.Sprintf("成交明细超过 %d 笔，已截断（只保留前 %d 笔）", maxFills, maxFills))
		return
	}
	r.Fills = append(r.Fills, fill)
}

func (r *Result) appendEquity(bar contract.Bar, cash float64, shares int, price float64) {
	r.Equity = append(r.Equity, EquityPoint{
		Date: bar.Date, Equity: round2(cash + float64(shares)*price),
		Cash: round2(cash), Position: round2(float64(shares) * price), Shares: shares,
	})
}

func (r *Result) computeMetrics() {
	if len(r.Equity) == 0 {
		return
	}
	eq := make([]float64, 0, len(r.Equity))
	for _, p := range r.Equity {
		eq = append(eq, p.Equity)
	}
	r.MaxDrawdown = round4(market.MaxDrawdown(eq))
	r.TotalReturn = round4(r.FinalEquity/r.InitialCash - 1)

	days := len(eq)
	if days > 1 {
		years := float64(days) / 252.0
		if years > 0 && r.FinalEquity > 0 {
			r.CAGR = round4(math.Pow(r.FinalEquity/r.InitialCash, 1/years) - 1)
		}
	}

	rets := make([]float64, 0, len(eq)-1)
	for i := 1; i < len(eq); i++ {
		if eq[i-1] <= 0 {
			continue
		}
		rets = append(rets, eq[i]/eq[i-1]-1)
	}
	r.Sharpe = round2(sharpe(rets))
	r.Sortino = round2(sortino(rets))

	wins := 0
	turnover := 0.0
	for _, t := range r.Trades {
		if t.PnL > 0 {
			wins++
		}
		turnover += math.Abs(t.EntryPrice*float64(t.Shares)) + math.Abs(t.ExitPrice*float64(t.Shares))
	}
	r.TradeCount = len(r.Trades)
	if r.TradeCount > 0 {
		r.WinRate = round4(float64(wins) / float64(r.TradeCount))
	}
	if r.InitialCash > 0 {
		r.Turnover = round2(turnover / r.InitialCash)
	}
}

func sharpe(rets []float64) float64 {
	if len(rets) < 2 {
		return 0
	}
	mean, sd := meanStd(rets)
	if sd == 0 {
		return 0
	}
	return mean / sd * math.Sqrt(252)
}

func sortino(rets []float64) float64 {
	if len(rets) < 2 {
		return 0
	}
	mean, _ := meanStd(rets)
	var downside float64
	var n int
	for _, r := range rets {
		if r < 0 {
			downside += r * r
			n++
		}
	}
	if n == 0 || downside == 0 {
		return 0
	}
	dd := math.Sqrt(downside / float64(len(rets)))
	return mean / dd * math.Sqrt(252)
}

func meanStd(xs []float64) (float64, float64) {
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var sq float64
	for _, x := range xs {
		sq += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(sq / float64(len(xs)))
}

// Report 生成 markdown 报告；四项成本必须出现，验收会检查。
func (r Result) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 回测报告 · %s（%s）\n\n", r.Strategy, r.Code)
	fmt.Fprintf(&b, "- 区间：%s ~ %s（%d 根日线）\n", r.Start, r.End, r.Bars)
	fmt.Fprintf(&b, "- 初始资金：%.2f 元，期末权益：%.2f 元\n", r.InitialCash, r.FinalEquity)
	fmt.Fprintf(&b, "- 总收益：%.2f%%，年化：%.2f%%，最大回撤：%.2f%%\n", r.TotalReturn*100, r.CAGR*100, r.MaxDrawdown*100)
	fmt.Fprintf(&b, "- 夏普：%.2f，索提诺：%.2f，胜率：%.2f%%，交易 %d 笔，换手 %.2fx\n\n",
		r.Sharpe, r.Sortino, r.WinRate*100, r.TradeCount, r.Turnover)
	b.WriteString("## 成本明细\n\n")
	fmt.Fprintf(&b, "- 佣金：%.2f 元\n", r.Costs.Commission)
	fmt.Fprintf(&b, "- 印花税：%.2f 元\n", r.Costs.StampTax)
	fmt.Fprintf(&b, "- 过户费：%.2f 元\n", r.Costs.Transfer)
	fmt.Fprintf(&b, "- 滑点：%.2f 元\n", r.Costs.Slippage)
	fmt.Fprintf(&b, "- 合计：%.2f 元（占初始资金 %.2f%%）\n\n", r.Costs.Total(), r.Costs.Total()/r.InitialCash*100)
	b.WriteString("## 交易明细\n\n")
	if len(r.Trades) == 0 {
		b.WriteString("（无平仓交易）\n")
	} else {
		b.WriteString("| 建仓 | 平仓 | 买入价 | 卖出价 | 股数 | 盈亏 | 收益 |\n|---|---|---|---|---|---|---|\n")
		for _, t := range r.Trades {
			fmt.Fprintf(&b, "| %s | %s | %.2f | %.2f | %d | %.2f | %.2f%% |\n",
				t.EntryDate, t.ExitDate, t.EntryPrice, t.ExitPrice, t.Shares, t.PnL, t.ReturnPct*100)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n## 回测提示\n\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}
	return b.String()
}

// LimitPctFor 按代码推断涨跌停比例：主板 10%，创业板/科创板 20%，北交所 30%，ST 5%。
func LimitPctFor(code string) float64 {
	c := strings.TrimSpace(code)
	upper := strings.ToUpper(c)
	if strings.HasPrefix(c, "sz") || strings.HasPrefix(c, "sh") || strings.HasPrefix(c, "bj") {
		c = c[2:]
	}
	if strings.Contains(upper, "ST") {
		return 0.05
	}
	switch {
	case strings.HasPrefix(c, "300"), strings.HasPrefix(c, "301"), strings.HasPrefix(c, "688"), strings.HasPrefix(c, "689"):
		return 0.20
	case strings.HasPrefix(c, "8"), strings.HasPrefix(c, "4"), strings.HasPrefix(c, "920"):
		return 0.30
	default:
		return 0.10
	}
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

func daysBetween(a, b string) int {
	ta, errA := parseDate(a)
	tb, errB := parseDate(b)
	if errA != nil || errB != nil {
		return 0
	}
	return int(tb.Sub(ta).Hours() / 24)
}

// TopN 按收益排序后的前 N 条（供报告/前端使用）。
func TopN(trades []Trade, n int) []Trade {
	out := append([]Trade(nil), trades...)
	sort.Slice(out, func(i, j int) bool { return out[i].PnL > out[j].PnL })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}
