package backtest

import (
	"time"

	"github.com/yazzyk/stock-agent/internal/market"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

func parseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

// ---------- 均线交叉 ----------

type MAConfig struct {
	Fast int
	Slow int
}

type MAStrategy struct {
	cfg    MAConfig
	closes []float64
}

func NewMA(fast, slow int) *MAStrategy {
	if fast <= 0 {
		fast = 5
	}
	if slow <= fast {
		slow = fast * 4
	}
	return &MAStrategy{cfg: MAConfig{Fast: fast, Slow: slow}}
}

func (s *MAStrategy) Name() string {
	return "均线交叉(" + itoa(s.cfg.Fast) + "/" + itoa(s.cfg.Slow) + ")"
}

func (s *MAStrategy) OnBar(bars []contract.Bar, i int) float64 {
	if i < s.cfg.Slow {
		return 0
	}
	s.closes = s.closes[:0]
	for _, b := range bars[:i+1] {
		s.closes = append(s.closes, b.Close)
	}
	fast := market.SMA(s.closes, s.cfg.Fast)
	slow := market.SMA(s.closes, s.cfg.Slow)
	f, okF := last(fast)
	sl, okS := last(slow)
	if !okF || !okS {
		return 0
	}
	if f > sl {
		return 1
	}
	return 0
}

// ---------- RSI 反转 ----------

type RSIStrategy struct {
	period    int
	lower     float64
	upper     float64
	holding   bool
	lastIndex int
}

func NewRSI(period int, lower, upper float64) *RSIStrategy {
	if period <= 0 {
		period = 14
	}
	if lower <= 0 {
		lower = 30
	}
	if upper <= 0 {
		upper = 70
	}
	return &RSIStrategy{period: period, lower: lower, upper: upper, lastIndex: -1}
}

func (s *RSIStrategy) Name() string { return "RSI反转(" + itoa(s.period) + ")" }

func (s *RSIStrategy) OnBar(bars []contract.Bar, i int) float64 {
	if i < s.period {
		return 0
	}
	closes := make([]float64, 0, i+1)
	for _, b := range bars[:i+1] {
		closes = append(closes, b.Close)
	}
	rsi := market.RSI(closes, s.period)
	v, ok := last(rsi)
	if !ok {
		return boolFloat(s.holding)
	}
	// 每个 bar 只更新一次状态，保证重放幂等。
	if s.lastIndex != i {
		if !s.holding && v < s.lower {
			s.holding = true
		} else if s.holding && v > s.upper {
			s.holding = false
		}
		s.lastIndex = i
	}
	return boolFloat(s.holding)
}

// ---------- 动量 ----------

type MomentumStrategy struct {
	lookback  int
	threshold float64
}

func NewMomentum(lookback int, threshold float64) *MomentumStrategy {
	if lookback <= 0 {
		lookback = 20
	}
	return &MomentumStrategy{lookback: lookback, threshold: threshold}
}

func (s *MomentumStrategy) Name() string { return "动量(" + itoa(s.lookback) + "日)" }

func (s *MomentumStrategy) OnBar(bars []contract.Bar, i int) float64 {
	if i < s.lookback {
		return 0
	}
	prev := bars[i-s.lookback].Close
	if prev <= 0 {
		return 0
	}
	ret := bars[i].Close/prev - 1
	if ret > s.threshold {
		return 1
	}
	return 0
}

// ---------- 买入持有（基线，必测） ----------

type BuyHold struct{}

func (BuyHold) Name() string { return "买入持有" }
func (BuyHold) OnBar([]contract.Bar, int) float64 {
	return 1
}

// ---------- 注册表 ----------

// ParamSet 是一个参数组合。
type ParamSet map[string]any

// StrategySpec 描述一个可用策略及其参数网格（供前端/agent 调用）。
// Rules 是逐条中文规则，Formula 是一句话信号公式，两者都直接展示给用户和 Agent。
type StrategySpec struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Desc    string     `json:"desc"`
	Formula string     `json:"formula,omitempty"`
	Rules   []string   `json:"rules,omitempty"`
	Params  []ParamDef `json:"params"`
	Default ParamSet   `json:"default"`
}

type ParamDef struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Type  string  `json:"type"` // int | float
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Step  float64 `json:"step,omitempty"`
}

// Specs 返回内置策略清单（含一句话公式、逐条规则与参数范围）。
func Specs() []StrategySpec {
	return []StrategySpec{
		{ID: "ma", Name: "均线交叉", Desc: "快线上穿慢线满仓、下穿空仓",
			Formula: "MA(close, fast) > MA(close, slow) → 满仓，否则空仓",
			Rules: []string{
				"信号：第 i 根日线收盘后，用收盘价序列计算 fast 日与 slow 日简单均线；快线在慢线上方则目标仓位 100%，否则 0%。",
				"成交：目标仓位只在第 i+1 根开盘执行；均线未成形的前 slow 根 K 线保持空仓。",
				"仓位：只有满仓/空仓两种状态，按 100 股整手向下取整；成交同时受 T+1、涨跌停与停牌约束。",
				"参数：fast 是快线周期，slow 是慢线周期，fast 必须小于 slow；步长 1 天。",
				"失效条件：震荡市里快慢线反复穿越会产生大量来回交易，成本吃掉收益；单边趋势反转时信号必然滞后。",
			},
			Params:  []ParamDef{{Key: "fast", Label: "快线", Type: "int", Min: 2, Max: 60, Step: 1}, {Key: "slow", Label: "慢线", Type: "int", Min: 5, Max: 250, Step: 1}},
			Default: ParamSet{"fast": 5, "slow": 20}},
		{ID: "rsi", Name: "RSI 反转", Desc: "RSI 低于下限买入、高于上限卖出",
			Formula: "RSI(period) < lower → 满仓；RSI(period) > upper → 空仓",
			Rules: []string{
				"信号：第 i 根收盘后用收盘价算 period 日 RSI（先取 period 日平均涨跌幅，之后按 period 做 Wilder 平滑）。",
				"状态：空仓时 RSI 跌破 lower 即满仓；持仓时 RSI 升破 upper 即空仓；介于两者之间维持原状态。",
				"成交：状态变化在第 i+1 根开盘执行；RSI 未成形的前 period 根 K 线保持空仓。",
				"参数：period 是 RSI 周期；lower/upper 是买入/卖出阈值，默认 30/70，网格里取 upper=100-lower 保持对称。",
				"失效条件：强势单边上涨中 RSI 长期高于 upper，卖出后可能错过趋势；下跌中 RSI 可能长期低于 lower，买入即被套。",
			},
			Params:  []ParamDef{{Key: "period", Label: "周期", Type: "int", Min: 2, Max: 60, Step: 1}, {Key: "lower", Label: "下限", Type: "float", Min: 5, Max: 45, Step: 1}, {Key: "upper", Label: "上限", Type: "float", Min: 55, Max: 95, Step: 1}},
			Default: ParamSet{"period": 14, "lower": 30, "upper": 70}},
		{ID: "momentum", Name: "动量", Desc: "N 日涨幅超过阈值满仓，否则空仓",
			Formula: "close[i] / close[i-lookback] - 1 > threshold → 满仓，否则空仓",
			Rules: []string{
				"信号：第 i 根收盘后计算 lookback 日涨幅 ret = close[i]/close[i-lookback]-1。",
				"规则：ret 严格大于 threshold 时目标满仓，否则空仓；不足 lookback 根 K 线时保持空仓。",
				"成交：目标仓位在第 i+1 根开盘执行；T+1、涨跌停与停牌照常约束。",
				"参数：lookback 是回看天数；threshold 是涨幅阈值，0 表示只要区间不下跌就持有。",
				"失效条件：阈值对标的波动率很敏感，同一阈值在高低波动标的上表现差异很大；急涨急跌时次日开盘成交会明显滑价。",
			},
			Params:  []ParamDef{{Key: "lookback", Label: "回看天数", Type: "int", Min: 3, Max: 120, Step: 1}, {Key: "threshold", Label: "阈值", Type: "float", Min: 0, Max: 0.5, Step: 0.01}},
			Default: ParamSet{"lookback": 20, "threshold": 0.05}},
		{ID: "buyhold", Name: "买入持有", Desc: "基线：首日满仓不动",
			Formula: "始终满仓（第 2 根 K 线开盘买入并持有）",
			Rules: []string{
				"信号：OnBar 恒返回 1，即在第 2 根 K 线开盘买入，之后不再调仓。",
				"用途：基线。任何主动策略跑不赢买入持有时，先怀疑策略本身，而不是继续调参数。",
				"成交：只买入一次；回测结束仍持仓按最后收盘价估值，不计清仓成本。",
				"失效条件：这是基准而非可交易策略，不含止损、仓位管理与风险控制。",
			},
			Params: nil, Default: ParamSet{}},
	}
}

// Rules 返回所有策略共享的成交、成本与统计口径说明（面板「算法说明」与 Agent 提示词共用）。
func Rules() []string {
	return []string{
		"标的与频率：单只 A 股日线（也可切周线/月线，指标按所选周期的 K 线计算）。",
		"信号时点：第 i 根 K 线收盘后计算目标仓位，只允许在第 i+1 根开盘成交；引擎强制，策略看不到未来数据。",
		"T+1：买入当日不可卖出；同日出现的卖出信号会因 T+1 被拒绝并记入回测提示。",
		"涨跌停：开盘触及涨停不买入、触及跌停不卖出，信号被跳过并在回测提示里记一条。",
		"停牌：成交量为 0 的 K 线不交易，只按最近收盘价估值。",
		"成本：佣金万三（单笔最低 5 元，双边）、印花税卖出万五、过户费双边 0.001%、滑点单边 0.1%，全部计入净值和报告。",
		"仓位：目标仓位只有 0 或 1（满仓/空仓），按 100 股整手向下取整，现金不足时按可用资金裁剪。",
		"复权：优先前复权；数据源不支持时降级为不复权并在结果里明确提示，此时结论要打折。",
		"统计口径：年化按 252 个交易日折算；最大回撤按日净值峰谷；夏普为日收益均值/标准差×√252；胜率按「建仓→清仓」往返交易统计。",
		"边界：回测结束仍持仓按最后收盘价估值、不计清仓成本；成交明细超过 500 笔会截断并提示。",
	}
}

// Build 按 id + 参数构造策略。
func Build(id string, params ParamSet) Strategy {
	switch id {
	case "ma":
		return NewMA(intParam(params, "fast", 5), intParam(params, "slow", 20))
	case "rsi":
		return NewRSI(intParam(params, "period", 14), floatParam(params, "lower", 30), floatParam(params, "upper", 70))
	case "momentum":
		return NewMomentum(intParam(params, "lookback", 20), floatParam(params, "threshold", 0.05))
	default:
		return BuyHold{}
	}
}

func intParam(p ParamSet, key string, def int) int {
	if p == nil {
		return def
	}
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	default:
		return def
	}
}

func floatParam(p ParamSet, key string, def float64) float64 {
	if p == nil {
		return def
	}
	switch v := p[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return def
	}
}

func last(s []float64) (float64, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if !isNaN(s[i]) {
			return s[i], true
		}
	}
	return 0, false
}

func isNaN(v float64) bool { return v != v }

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [8]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
