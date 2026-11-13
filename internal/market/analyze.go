package market

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// Indicators 是面板要用的指标序列集合，长度与 bars 等长，缺口为 NaN（JSON 输出 null）。
type Indicators struct {
	MA5    Series `json:"ma5"`
	MA10   Series `json:"ma10"`
	MA20   Series `json:"ma20"`
	MA60   Series `json:"ma60"`
	Volume Series `json:"volume"`
}

// ComputeIndicators 按面板口径一次算齐 MA5/10/20/60 与成交量。
func ComputeIndicators(bars []contract.Bar) Indicators {
	closes := Closes(bars)
	volumes := make([]float64, len(bars))
	for i, b := range bars {
		volumes[i] = b.Volume
	}
	return Indicators{
		MA5:    Series(SMA(closes, 5)),
		MA10:   Series(SMA(closes, 10)),
		MA20:   Series(SMA(closes, 20)),
		MA60:   Series(SMA(closes, 60)),
		Volume: Series(volumes),
	}
}

// Closes 抽取收盘价序列。
func Closes(bars []contract.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.Close
	}
	return out
}

// Summary 是多空解读摘要（中文），键名与 kline 面板逐字对齐。
type Summary struct {
	Trend string `json:"trend"`
	MA    string `json:"ma"`
	RSI   string `json:"rsi"`
	MACD  string `json:"macd"`
	ATR   string `json:"atr"`
}

// Analysis 是一次完整技术分析的结果。
type Analysis struct {
	Last       contract.Bar `json:"last"`
	Summary    Summary      `json:"summary"`
	Patterns   []Pattern    `json:"patterns"`
	Support    []float64    `json:"support"`
	Resistance []float64    `json:"resistance"`
	Indicators Indicators   `json:"indicators"`
}

// HorizonAnalysis 是单一时间尺度（短线/长线）的结构研判，字段名与前端契约逐字对齐。
type HorizonAnalysis struct {
	Key        string    `json:"key"`
	Title      string    `json:"title"`
	Window     int       `json:"window"`
	Bias       string    `json:"bias"`
	Summary    Summary   `json:"summary"`
	Patterns   []Pattern `json:"patterns"`
	Support    []float64 `json:"support"`
	Resistance []float64 `json:"resistance"`
}

// 分析用的默认参数，集中在一处，避免面板与工具口径漂移。
const (
	analyzeRSIPeriod   = 14
	analyzeATRPeriod   = 14
	analyzePatternSpan = 30
	structureLookback  = 120
)

// 双尺度研判的窗口、阈值与三态结论字面量；前端按「偏多/偏空/中性」着色，改动即破坏兼容。
const (
	horizonShort = "short"
	horizonLong  = "long"

	shortHorizonWindow = 60
	longHorizonWindow  = 250
	longMinSample      = 120
	horizonPatternSpan = 30

	biasBull    = "偏多"
	biasBear    = "偏空"
	biasNeutral = "中性"
)

// Analyze 汇总最近一段行情：摘要 + 形态 + 结构位 + 指标序列。
// 少于 2 根 K 线时返回零值（Last 仍填充），不做任何外推。
func Analyze(bars []contract.Bar) Analysis {
	if len(bars) == 0 {
		return Analysis{}
	}
	closes := Closes(bars)
	ind := ComputeIndicators(bars)
	rsi := RSI(closes, analyzeRSIPeriod)
	atr := ATR(bars, analyzeATRPeriod)
	dif, dea, macd := MACD(closes, 12, 26, 9)
	support, resistance := Structure(bars, structureLookback)

	return Analysis{
		Last: bars[len(bars)-1],
		Summary: Summary{
			Trend: trendSummary(bars, ind),
			MA:    maSummary(bars, ind),
			RSI:   rsiSummary(rsi),
			MACD:  macdSummary(dif, dea, macd),
			ATR:   atrSummary(bars[len(bars)-1], atr),
		},
		Patterns:   PatternsRecent(bars, analyzePatternSpan),
		Support:    support,
		Resistance: resistance,
		Indicators: ind,
	}
}

func trendSummary(bars []contract.Bar, ind Indicators) string {
	last := bars[len(bars)-1]
	if len(bars) < 20 {
		return fmt.Sprintf("样本仅 %d 根，均线体系不完整，暂不判定趋势", len(bars))
	}
	ma5 := lastValue(ind.MA5, len(bars)-1)
	ma10 := lastValue(ind.MA10, len(bars)-1)
	ma20 := lastValue(ind.MA20, len(bars)-1)
	ma60 := lastValue(ind.MA60, len(bars)-1)
	if !valid(ma5) || !valid(ma10) || !valid(ma20) {
		return "均线数据不足，暂不判定趋势"
	}
	above60 := ""
	if valid(ma60) {
		if last.Close >= ma60 {
			above60 = "，且站上 MA60"
		} else {
			above60 = "，但位于 MA60 下方"
		}
	}
	switch {
	case ma5 > ma10 && ma10 > ma20:
		return fmt.Sprintf("多头排列（MA5>MA10>MA20），趋势向上%s", above60)
	case ma5 < ma10 && ma10 < ma20:
		return fmt.Sprintf("空头排列（MA5<MA10<MA20），趋势向下%s", above60)
	default:
		return fmt.Sprintf("均线纠缠，震荡格局%s", above60)
	}
}

func maSummary(bars []contract.Bar, ind Indicators) string {
	last := bars[len(bars)-1]
	ma5 := lastValue(ind.MA5, len(bars)-1)
	ma20 := lastValue(ind.MA20, len(bars)-1)
	switch {
	case valid(ma5) && valid(ma20) && ma20 != 0:
		pct := (last.Close/ma20 - 1) * 100
		return fmt.Sprintf("收盘 %.2f，MA5 %.2f，MA20 %.2f，距 MA20 %+.2f%%", last.Close, ma5, ma20, pct)
	case valid(ma5):
		return fmt.Sprintf("收盘 %.2f，MA5 %.2f（MA20 样本不足）", last.Close, ma5)
	default:
		return fmt.Sprintf("收盘 %.2f（均线样本不足 5 根）", last.Close)
	}
}

func rsiSummary(rsi []float64) string {
	if len(rsi) == 0 {
		return "RSI 样本不足"
	}
	v := rsi[len(rsi)-1]
	if !valid(v) {
		return fmt.Sprintf("RSI(%d) 样本不足 %d 根", analyzeRSIPeriod, analyzeRSIPeriod+1)
	}
	zone := "中性"
	switch {
	case v >= 80:
		zone = "严重超买"
	case v >= 70:
		zone = "超买"
	case v <= 20:
		zone = "严重超卖"
	case v <= 30:
		zone = "超卖"
	case v >= 55:
		zone = "偏强"
	case v <= 45:
		zone = "偏弱"
	}
	return fmt.Sprintf("RSI(%d)=%.1f，%s", analyzeRSIPeriod, v, zone)
}

func macdSummary(dif, dea, macd []float64) string {
	d, e, h := lastValue(dif, len(dif)-1), lastValue(dea, len(dea)-1), lastValue(macd, len(macd)-1)
	if !valid(d) || !valid(e) || !valid(h) {
		return "MACD 样本不足（需至少 34 根 K 线）"
	}
	state := "DIF 与 DEA 粘合"
	if d > e {
		state = "DIF 在 DEA 上方（金叉状态）"
	} else if d < e {
		state = "DIF 在 DEA 下方（死叉状态）"
	}
	bar := "红柱扩张"
	if h < 0 {
		bar = "绿柱扩张"
	}
	if len(macd) >= 2 {
		prev := lastValue(macd, len(macd)-2)
		if valid(prev) {
			if math.Abs(h) < math.Abs(prev) {
				bar = "柱体收敛"
			} else if h > 0 && prev <= 0 {
				bar = "红柱翻出"
			} else if h < 0 && prev >= 0 {
				bar = "绿柱翻出"
			}
		}
	}
	return fmt.Sprintf("DIF %.3f / DEA %.3f / 柱 %.3f，%s、%s", d, e, h, state, bar)
}

func atrSummary(last contract.Bar, atr []float64) string {
	v := lastValue(atr, len(atr)-1)
	if !valid(v) {
		return fmt.Sprintf("ATR(%d) 样本不足", analyzeATRPeriod)
	}
	if last.Close <= 0 {
		return fmt.Sprintf("ATR(%d)=%.2f", analyzeATRPeriod, v)
	}
	return fmt.Sprintf("ATR(%d)=%.2f，占收盘 %.2f%%，为日均真实波幅参考", analyzeATRPeriod, v, v/last.Close*100)
}

// Structure 用分形高低点聚类出支撑位与阻力位（相对最后一根收盘价）。
//
// 口径：窗口 ±2 根的局部极值点，按 1.5% 容差聚类，触及次数多者优先；
// 低于收盘价的算支撑（由近到远），高于收盘价的算阻力（由近到远），各取前 3 个。
// 没有极值点时退化为区间最低价/最高价，保证面板永远有参考位。
func Structure(bars []contract.Bar, lookback int) (support, resistance []float64) {
	if len(bars) == 0 {
		return nil, nil
	}
	start := 0
	if lookback > 0 && len(bars) > lookback {
		start = len(bars) - lookback
	}
	seg := bars[start:]
	lastClose := bars[len(bars)-1].Close
	sup, res := swingLevels(seg, 2)
	outSup := pickLevels(sup, lastClose, true, 3)
	outRes := pickLevels(res, lastClose, false, 3)
	if len(outSup) == 0 && lastClose > 0 {
		if low := lowestLow(seg); low > 0 && low < lastClose {
			outSup = []float64{round2(low)}
		}
	}
	if len(outRes) == 0 && lastClose > 0 {
		if high := highestHigh(seg); high > lastClose {
			outRes = []float64{round2(high)}
		}
	}
	return outSup, outRes
}

// swingLevels 找局部极值（窗口 ±w），返回聚类后的价位与对应权重。
func swingLevels(bars []contract.Bar, w int) (support, resistance []levelCluster) {
	for i := w; i < len(bars)-w; i++ {
		isLow, isHigh := true, true
		for j := i - w; j <= i+w; j++ {
			if j == i {
				continue
			}
			if bars[j].Low <= bars[i].Low {
				isLow = false
			}
			if bars[j].High >= bars[i].High {
				isHigh = false
			}
		}
		if isLow {
			support = addLevel(support, bars[i].Low)
		}
		if isHigh {
			resistance = addLevel(resistance, bars[i].High)
		}
	}
	return support, resistance
}

type levelCluster struct {
	price   float64
	touches int
}

// addLevel 把价位并入 1.5% 容差内最近的簇，没有则新建。
func addLevel(levels []levelCluster, price float64) []levelCluster {
	if price <= 0 {
		return levels
	}
	for i := range levels {
		if math.Abs(price-levels[i].price)/levels[i].price <= 0.015 {
			// 用加权平均把簇中心往新价位挪一点，避免极端值把代表价拉偏。
			levels[i].price = (levels[i].price*float64(levels[i].touches) + price) / float64(levels[i].touches+1)
			levels[i].touches++
			return levels
		}
	}
	return append(levels, levelCluster{price: price, touches: 1})
}

// pickLevels 按「触及次数多优先、距离近优先」排序并取值。
func pickLevels(levels []levelCluster, lastClose float64, below bool, limit int) []float64 {
	filtered := make([]levelCluster, 0, len(levels))
	for _, l := range levels {
		if below && l.price < lastClose {
			filtered = append(filtered, l)
		}
		if !below && l.price > lastClose {
			filtered = append(filtered, l)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].touches != filtered[j].touches {
			return filtered[i].touches > filtered[j].touches
		}
		if below {
			return filtered[i].price > filtered[j].price
		}
		return filtered[i].price < filtered[j].price
	})
	out := make([]float64, 0, limit)
	for _, l := range filtered {
		if len(out) >= limit {
			break
		}
		out = append(out, round2(l.price))
	}
	if below {
		sort.Sort(sort.Reverse(sort.Float64Slice(out)))
	} else {
		sort.Float64s(out)
	}
	return out
}

func lowestLow(bars []contract.Bar) float64 {
	low := math.Inf(1)
	for _, b := range bars {
		if b.Low > 0 && b.Low < low {
			low = b.Low
		}
	}
	if math.IsInf(low, 1) {
		return 0
	}
	return low
}

func highestHigh(bars []contract.Bar) float64 {
	high := 0.0
	for _, b := range bars {
		if b.High > high {
			high = b.High
		}
	}
	return high
}

// lastValue 安全取序列末位（NaN 表示缺口）。
func lastValue(s []float64, i int) float64 {
	if i < 0 || i >= len(s) {
		return math.NaN()
	}
	return s[i]
}

// AnalyzeHorizons 按「短线 + 长线」两个尺度各算一份研判，顺序固定为短线在前。
func AnalyzeHorizons(bars []contract.Bar) []HorizonAnalysis {
	return []HorizonAnalysis{
		AnalyzeHorizon(bars, horizonShort),
		AnalyzeHorizon(bars, horizonLong),
	}
}

// AnalyzeHorizon 计算单一尺度的结构研判：短线取最近 60 根，长线取最近 250 根。
// 结论只做结构描述，不给出买卖指令；样本不足时在摘要里明确提示。
func AnalyzeHorizon(bars []contract.Bar, horizon string) HorizonAnalysis {
	key := normalizeHorizon(horizon)
	window := horizonWindow(bars, key)
	out := HorizonAnalysis{
		Key:    key,
		Title:  horizonTitle(key),
		Window: len(window),
		Bias:   biasNeutral,
		Summary: Summary{
			Trend: "K 线样本为空，暂不研判",
			MA:    "均线样本不足",
			RSI:   "RSI 样本不足",
			MACD:  "MACD 样本不足",
			ATR:   "ATR 样本不足",
		},
		Patterns:   []Pattern{},
		Support:    []float64{},
		Resistance: []float64{},
	}
	if len(window) == 0 {
		return out
	}

	last := window[len(window)-1]
	closes := Closes(window)
	rsi := RSI(closes, analyzeRSIPeriod)
	atr := ATR(window, analyzeATRPeriod)
	dif, dea, hist := MACD(closes, 12, 26, 9)

	var bias, reason, trend string
	if key == horizonLong {
		bias, reason = longBias(window, dif, dea)
		trend = longTrendSummary(window)
		out.Summary = Summary{
			Trend: trendWithBias(trend, bias, reason),
			MA:    longMASummary(window),
			RSI:   rsiSummary(rsi),
			MACD:  macdSummary(dif, dea, hist),
			ATR:   atrSummary(last, atr),
		}
	} else {
		bias, reason = shortBias(window, rsi, dif, dea)
		trend = shortTrendSummary(window)
		out.Summary = Summary{
			Trend: trendWithBias(trend, bias, reason),
			MA:    maSummary(window, ComputeIndicators(window)),
			RSI:   rsiSummary(rsi),
			MACD:  macdSummary(dif, dea, hist),
			ATR:   atrSummary(last, atr),
		}
	}
	out.Bias = bias
	out.Patterns = PatternsRecent(window, horizonPatternSpan)
	out.Support, out.Resistance = Structure(window, len(window))
	return out
}

// normalizeHorizon 归一化尺度参数，未知值回落到短线。
func normalizeHorizon(horizon string) string {
	if strings.EqualFold(strings.TrimSpace(horizon), horizonLong) {
		return horizonLong
	}
	return horizonShort
}

// horizonTitle 返回尺度标题，面板直接展示；周期可切换，因此用根数而非「日」描述。
func horizonTitle(key string) string {
	if key == horizonLong {
		return "长线（近 250 根）"
	}
	return "短线（近 60 根）"
}

// horizonWindow 截取尺度对应的 K 线窗口：短线 60 根，长线 250 根，不足则用全部。
func horizonWindow(bars []contract.Bar, key string) []contract.Bar {
	limit := shortHorizonWindow
	if key == horizonLong {
		limit = longHorizonWindow
	}
	if len(bars) > limit {
		return bars[len(bars)-limit:]
	}
	return bars
}

// trendWithBias 把三态结论与一句理由并入趋势摘要，前端不必再拼接。
func trendWithBias(trend, bias, reason string) string {
	if reason == "" {
		return trend + "；结构结论" + bias
	}
	return fmt.Sprintf("%s；结构结论%s（%s）", trend, bias, reason)
}

// shortTrendSummary 描述短线 MA5/MA10/MA20 排列与收盘相对 MA20 的位置。
func shortTrendSummary(bars []contract.Bar) string {
	last := bars[len(bars)-1]
	if len(bars) < 20 {
		return fmt.Sprintf("样本仅 %d 根（不足 20 根），短线均线体系不完整，暂不判定趋势", len(bars))
	}
	closes := Closes(bars)
	ma5 := lastValue(SMA(closes, 5), len(closes)-1)
	ma10 := lastValue(SMA(closes, 10), len(closes)-1)
	ma20 := lastValue(SMA(closes, 20), len(closes)-1)
	switch {
	case ma5 > ma10 && ma10 > ma20:
		return fmt.Sprintf("短线多头排列（MA5>MA10>MA20），收盘 %.2f 位于 MA20 %.2f 上方", last.Close, ma20)
	case ma5 < ma10 && ma10 < ma20:
		return fmt.Sprintf("短线空头排列（MA5<MA10<MA20），收盘 %.2f 位于 MA20 %.2f 下方", last.Close, ma20)
	default:
		position := "上方"
		if last.Close < ma20 {
			position = "下方"
		}
		return fmt.Sprintf("短线均线纠缠（MA5 %.2f / MA10 %.2f / MA20 %.2f），收盘位于 MA20 %s", ma5, ma10, ma20, position)
	}
}

// shortBias 归纳短线三态倾向并给出结构理由；样本不足 20 根时回到中性。
func shortBias(bars []contract.Bar, rsi, dif, dea []float64) (bias, reason string) {
	if len(bars) < 20 {
		return biasNeutral, fmt.Sprintf("样本仅 %d 根，不足 20 根，尚不足以判断短线结构", len(bars))
	}
	closes := Closes(bars)
	last := bars[len(bars)-1]
	ma5 := lastValue(SMA(closes, 5), len(closes)-1)
	ma10 := lastValue(SMA(closes, 10), len(closes)-1)
	ma20 := lastValue(SMA(closes, 20), len(closes)-1)

	score := 0
	parts := make([]string, 0, 4)
	switch {
	case ma5 > ma10 && ma10 > ma20:
		score += 2
		parts = append(parts, "MA5>MA10>MA20")
	case ma5 < ma10 && ma10 < ma20:
		score -= 2
		parts = append(parts, "MA5<MA10<MA20")
	default:
		parts = append(parts, "均线纠缠")
	}
	if last.Close >= ma20 {
		score++
		parts = append(parts, "收盘站上 MA20")
	} else {
		score--
		parts = append(parts, "收盘位于 MA20 下方")
	}
	if d, e := lastValue(dif, len(dif)-1), lastValue(dea, len(dea)-1); valid(d) && valid(e) {
		switch {
		case d > e:
			score++
			parts = append(parts, "MACD 处于多头")
		case d < e:
			score--
			parts = append(parts, "MACD 处于空头")
		}
	}
	if v := lastValue(rsi, len(rsi)-1); valid(v) {
		switch {
		case v >= 55:
			score++
			parts = append(parts, fmt.Sprintf("RSI %.1f 偏强", v))
		case v <= 45:
			score--
			parts = append(parts, fmt.Sprintf("RSI %.1f 偏弱", v))
		default:
			parts = append(parts, fmt.Sprintf("RSI %.1f 中性", v))
		}
	}
	switch {
	case score >= 2:
		bias = biasBull
	case score <= -2:
		bias = biasBear
	default:
		bias = biasNeutral
	}
	return bias, strings.Join(parts, "、")
}

// longTrendSummary 描述长线均线排列、收盘相对 MA60/MA120 的位置与窗口回撤结构。
func longTrendSummary(bars []contract.Bar) string {
	last := bars[len(bars)-1]
	closes := Closes(bars)
	ma20 := lastValue(SMA(closes, 20), len(closes)-1)
	ma60 := lastValue(SMA(closes, 60), len(closes)-1)
	ma120 := lastValue(SMA(closes, 120), len(closes)-1)

	parts := make([]string, 0, 6)
	if valid(ma20) && valid(ma60) {
		if ma20 >= ma60 {
			parts = append(parts, "MA20 位于 MA60 上方")
		} else {
			parts = append(parts, "MA20 位于 MA60 下方")
		}
	}
	if valid(ma60) && valid(ma120) {
		if ma60 >= ma120 {
			parts = append(parts, "MA60 位于 MA120 上方")
		} else {
			parts = append(parts, "MA60 位于 MA120 下方")
		}
	}
	if valid(ma60) {
		if last.Close >= ma60 {
			parts = append(parts, fmt.Sprintf("收盘 %.2f 站上 MA60 %.2f", last.Close, ma60))
		} else {
			parts = append(parts, fmt.Sprintf("收盘 %.2f 位于 MA60 %.2f 下方", last.Close, ma60))
		}
	} else {
		parts = append(parts, "MA60 样本不足（需 60 根）")
	}
	if valid(ma120) {
		if last.Close >= ma120 {
			parts = append(parts, "站上 MA120")
		} else {
			parts = append(parts, "位于 MA120 下方")
		}
	} else {
		parts = append(parts, "MA120 样本不足（需 120 根）")
	}
	parts = append(parts, fmt.Sprintf("窗口内最大回撤 %.1f%%", MaxDrawdown(closes)*100))

	text := "长线结构：" + strings.Join(parts, "，")
	if len(bars) < longMinSample {
		text = fmt.Sprintf("样本仅 %d 根（不足 %d 根，长线结论仅供参考）。%s", len(bars), longMinSample, text)
	}
	return text
}

// longMASummary 给出长线均线数值；不足 120 根时明确标注 MA120 缺失。
func longMASummary(bars []contract.Bar) string {
	last := bars[len(bars)-1]
	closes := Closes(bars)
	ma20 := lastValue(SMA(closes, 20), len(closes)-1)
	ma60 := lastValue(SMA(closes, 60), len(closes)-1)
	ma120 := lastValue(SMA(closes, 120), len(closes)-1)
	if len(bars) < longMinSample {
		if valid(ma20) && valid(ma60) {
			return fmt.Sprintf("样本 %d 根，MA120 不足（需 %d 根）；收盘 %.2f，MA20 %.2f，MA60 %.2f", len(bars), longMinSample, last.Close, ma20, ma60)
		}
		return fmt.Sprintf("样本仅 %d 根，长线均线（MA20/MA60/MA120）样本不足", len(bars))
	}
	if valid(ma20) && valid(ma60) && valid(ma120) && ma60 != 0 {
		return fmt.Sprintf("收盘 %.2f，MA20 %.2f，MA60 %.2f，MA120 %.2f，距 MA60 %+.2f%%", last.Close, ma20, ma60, ma120, (last.Close/ma60-1)*100)
	}
	return fmt.Sprintf("收盘 %.2f（长线均线样本不足）", last.Close)
}

// longBias 归纳长线三态倾向并给出结构理由；MA120 缺失时按可得均线降级判断。
func longBias(bars []contract.Bar, dif, dea []float64) (bias, reason string) {
	if len(bars) < 20 {
		return biasNeutral, fmt.Sprintf("样本仅 %d 根，不足 20 根，尚不足以判断长线结构", len(bars))
	}
	closes := Closes(bars)
	last := bars[len(bars)-1]
	ma20 := lastValue(SMA(closes, 20), len(closes)-1)
	ma60 := lastValue(SMA(closes, 60), len(closes)-1)
	ma120 := lastValue(SMA(closes, 120), len(closes)-1)

	score := 0
	parts := make([]string, 0, 5)
	if valid(ma20) {
		if last.Close >= ma20 {
			score++
			parts = append(parts, "收盘位于 MA20 上方")
		} else {
			score--
			parts = append(parts, "收盘位于 MA20 下方")
		}
	}
	if valid(ma60) {
		if last.Close >= ma60 {
			score++
			parts = append(parts, "收盘位于 MA60 上方")
		} else {
			score--
			parts = append(parts, "收盘位于 MA60 下方")
		}
		if valid(ma20) {
			if ma20 >= ma60 {
				score++
				parts = append(parts, "MA20 在 MA60 上方")
			} else {
				score--
				parts = append(parts, "MA20 在 MA60 下方")
			}
		}
	}
	if valid(ma60) && valid(ma120) {
		if ma60 >= ma120 {
			score++
			parts = append(parts, "MA60 在 MA120 上方")
		} else {
			score--
			parts = append(parts, "MA60 在 MA120 下方")
		}
	}
	if d, e := lastValue(dif, len(dif)-1), lastValue(dea, len(dea)-1); valid(d) && valid(e) {
		switch {
		case d > e:
			score++
			parts = append(parts, "MACD 处于多头")
		case d < e:
			score--
			parts = append(parts, "MACD 处于空头")
		}
	}
	switch {
	case score >= 2:
		bias = biasBull
	case score <= -2:
		bias = biasBear
	default:
		bias = biasNeutral
	}
	return bias, strings.Join(parts, "、")
}
