package market

import (
	"fmt"
	"math"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 形态名称。前端与模型按这些字面量识别，改动即破坏兼容。
const (
	PatternDoji               = "十字星"
	PatternHammer             = "锤子线"
	PatternBullishEngulfing   = "看涨吞没"
	PatternBearishEngulfing   = "看跌吞没"
	PatternThreeWhiteSoldiers = "三连阳"
	PatternVolumeBreakout     = "放量突破"
)

// Pattern 是一个命中的 K 线形态。Index 是形态完成的那根 K 线下标（0 起）。
type Pattern struct {
	Name       string  `json:"name"`
	Index      int     `json:"index"`
	Confidence float64 `json:"confidence"`
	Note       string  `json:"note"`
}

// 突破判定用的回溯根数与放量倍数，写成常量便于测试与文档口径一致。
const (
	breakoutLookback = 20
	breakoutVolume   = 1.5
)

// Patterns 扫描全部 K 线，按时间先后返回命中的形态。
// 判定只用当前与历史 K 线，形态完成后的数据不参与。
func Patterns(bars []contract.Bar) []Pattern {
	out := make([]Pattern, 0, 8)
	for i := range bars {
		if p, ok := dojiAt(bars, i); ok {
			out = append(out, p)
		}
		if p, ok := hammerAt(bars, i); ok {
			out = append(out, p)
		}
		if p, ok := engulfingAt(bars, i); ok {
			out = append(out, p)
		}
		if p, ok := threeWhiteSoldiersAt(bars, i); ok {
			out = append(out, p)
		}
		if p, ok := volumeBreakoutAt(bars, i); ok {
			out = append(out, p)
		}
	}
	return out
}

// PatternsRecent 只返回最近 lookback 根内的形态；lookback<=0 时等价于全量扫描。
func PatternsRecent(bars []contract.Bar, lookback int) []Pattern {
	all := Patterns(bars)
	if lookback <= 0 {
		return all
	}
	start := len(bars) - lookback
	if start < 0 {
		start = 0
	}
	out := make([]Pattern, 0, len(all))
	for _, p := range all {
		if p.Index >= start {
			out = append(out, p)
		}
	}
	return out
}

func dojiAt(bars []contract.Bar, i int) (Pattern, bool) {
	b := bars[i]
	rng := b.High - b.Low
	if rng <= 0 {
		return Pattern{}, false
	}
	body := math.Abs(b.Close - b.Open)
	ratio := body / rng
	if ratio > 0.1 {
		return Pattern{}, false
	}
	conf := clamp01(0.5 + 0.5*(1-ratio/0.1))
	return Pattern{
		Name:       PatternDoji,
		Index:      i,
		Confidence: round2(conf),
		Note:       fmt.Sprintf("实体仅占振幅 %.1f%%，多空分歧明显，等待方向选择", ratio*100),
	}, true
}

func hammerAt(bars []contract.Bar, i int) (Pattern, bool) {
	b := bars[i]
	rng := b.High - b.Low
	if rng <= 0 {
		return Pattern{}, false
	}
	body := math.Abs(b.Close - b.Open)
	upper := b.High - math.Max(b.Open, b.Close)
	lower := math.Min(b.Open, b.Close) - b.Low
	if lower < 2*body || upper > 0.3*rng || body/rng > 0.35 {
		return Pattern{}, false
	}
	strength := lower / rng
	conf := clamp01(0.4 + 0.6*strength)
	return Pattern{
		Name:       PatternHammer,
		Index:      i,
		Confidence: round2(conf),
		Note:       fmt.Sprintf("下影占振幅 %.1f%%，低位承接明显", strength*100),
	}, true
}

func engulfingAt(bars []contract.Bar, i int) (Pattern, bool) {
	if i == 0 {
		return Pattern{}, false
	}
	prev, cur := bars[i-1], bars[i]
	prevBody := math.Abs(prev.Close - prev.Open)
	curBody := math.Abs(cur.Close - cur.Open)
	if prevBody <= 0 || curBody <= 0 || curBody <= prevBody {
		return Pattern{}, false
	}
	prevBear := prev.Close < prev.Open
	prevBull := prev.Close > prev.Open
	curBull := cur.Close > cur.Open
	curBear := cur.Close < cur.Open
	conf := round2(clamp01(0.45 + 0.55*math.Min(1, curBody/prevBody-1)))

	if prevBear && curBull && cur.Open <= prev.Close && cur.Close >= prev.Open {
		return Pattern{
			Name:       PatternBullishEngulfing,
			Index:      i,
			Confidence: conf,
			Note:       fmt.Sprintf("阳线实体吞没前一根阴线（%.2f 倍），短线转强", curBody/prevBody),
		}, true
	}
	if prevBull && curBear && cur.Open >= prev.Close && cur.Close <= prev.Open {
		return Pattern{
			Name:       PatternBearishEngulfing,
			Index:      i,
			Confidence: conf,
			Note:       fmt.Sprintf("阴线实体吞没前一根阳线（%.2f 倍），短线转弱", curBody/prevBody),
		}, true
	}
	return Pattern{}, false
}

func threeWhiteSoldiersAt(bars []contract.Bar, i int) (Pattern, bool) {
	if i < 2 {
		return Pattern{}, false
	}
	a, b, c := bars[i-2], bars[i-1], bars[i]
	if a.Close <= a.Open || b.Close <= b.Open || c.Close <= c.Open {
		return Pattern{}, false
	}
	if !(a.Close < b.Close && b.Close < c.Close) {
		return Pattern{}, false
	}
	// 连续三根阳线且重心抬高，第三根涨幅越大信号越强（封顶 1）。
	base := a.Open
	if base <= 0 {
		return Pattern{}, false
	}
	gain := c.Close/base - 1
	return Pattern{
		Name:       PatternThreeWhiteSoldiers,
		Index:      i,
		Confidence: round2(clamp01(0.5 + 5*math.Min(gain, 0.1))),
		Note:       fmt.Sprintf("三连阳累计上涨 %.2f%%，重心持续抬高", gain*100),
	}, true
}

func volumeBreakoutAt(bars []contract.Bar, i int) (Pattern, bool) {
	if i < breakoutLookback {
		return Pattern{}, false
	}
	high, volSum := 0.0, 0.0
	for j := i - breakoutLookback; j < i; j++ {
		if bars[j].High > high {
			high = bars[j].High
		}
		volSum += bars[j].Volume
	}
	avgVol := volSum / float64(breakoutLookback)
	if high <= 0 || avgVol <= 0 {
		return Pattern{}, false
	}
	if bars[i].Close <= high || bars[i].Volume < breakoutVolume*avgVol {
		return Pattern{}, false
	}
	ratio := bars[i].Volume / avgVol
	conf := clamp01(0.5 + 0.25*(ratio-breakoutVolume))
	return Pattern{
		Name:       PatternVolumeBreakout,
		Index:      i,
		Confidence: round2(conf),
		Note:       fmt.Sprintf("放量 %.2f 倍突破 %d 日高点 %.2f", ratio, breakoutLookback, high),
	}, true
}

func clamp01(v float64) float64 {
	if !valid(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
