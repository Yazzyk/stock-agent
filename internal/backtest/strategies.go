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
type StrategySpec struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Desc    string     `json:"desc"`
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

// Specs 返回内置策略清单。
func Specs() []StrategySpec {
	return []StrategySpec{
		{ID: "ma", Name: "均线交叉", Desc: "快线上穿慢线满仓、下穿空仓",
			Params: []ParamDef{{Key: "fast", Label: "快线", Type: "int", Min: 2, Max: 60, Step: 1}, {Key: "slow", Label: "慢线", Type: "int", Min: 5, Max: 250, Step: 1}},
			Default: ParamSet{"fast": 5, "slow": 20}},
		{ID: "rsi", Name: "RSI 反转", Desc: "RSI 低于下限买入、高于上限卖出",
			Params: []ParamDef{{Key: "period", Label: "周期", Type: "int", Min: 2, Max: 60, Step: 1}, {Key: "lower", Label: "下限", Type: "float", Min: 5, Max: 45, Step: 1}, {Key: "upper", Label: "上限", Type: "float", Min: 55, Max: 95, Step: 1}},
			Default: ParamSet{"period": 14, "lower": 30, "upper": 70}},
		{ID: "momentum", Name: "动量", Desc: "N 日涨幅超过阈值满仓，否则空仓",
			Params: []ParamDef{{Key: "lookback", Label: "回看天数", Type: "int", Min: 3, Max: 120, Step: 1}, {Key: "threshold", Label: "阈值", Type: "float", Min: 0, Max: 0.5, Step: 0.01}},
			Default: ParamSet{"lookback": 20, "threshold": 0.05}},
		{ID: "buyhold", Name: "买入持有", Desc: "基线：首日满仓不动", Params: nil, Default: ParamSet{}},
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
