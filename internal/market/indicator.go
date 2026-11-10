// Package market 是纯函数行情计算库：技术指标、K 线形态、结构位与多空解读。
//
// 三条铁律（全部有测试守着）：
//  1. 每个指标函数的返回长度一定等于输入长度，缺口用 NaN 占位，调用方按 length==bars 对齐即可；
//  2. 不使用未来数据：第 i 根的输出只依赖 [0..i]，绝无中心平滑或整段归一；
//  3. 不做隐式单位换算，涨跌幅/占比一律百分数（2.34 表示 2.34%）。
package market

import (
	"encoding/json"
	"math"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// Series 是「带 NaN 缺口」的指标序列，JSON 里 NaN 输出为 null。
// 前端 K 线面板据此直接在折线图里留空，不需要额外约定。
type Series []float64

func (s Series) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	out := make([]any, len(s))
	for i, v := range s {
		if valid(v) {
			out[i] = v
			continue
		}
		out[i] = nil
	}
	return json.Marshal(out)
}

// valid 判断数值可用：NaN/Inf 都视为缺口。
func valid(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// newSeries 生成填满 NaN 的等长序列。
func newSeries(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

// SMA 是简单移动平均；前 n-1 个点无值（NaN）。
// 输入里若出现 NaN/Inf，该值进入窗口后对应输出也视为缺口，不做插值。
func SMA(values []float64, n int) []float64 {
	out := newSeries(len(values))
	if n <= 0 || len(values) < n {
		return out
	}
	sum := 0.0
	bad := 0
	for i, v := range values {
		if valid(v) {
			sum += v
		} else {
			bad++
		}
		if i >= n {
			if valid(values[i-n]) {
				sum -= values[i-n]
			} else {
				bad--
			}
		}
		if i >= n-1 && bad == 0 {
			out[i] = sum / float64(n)
		}
	}
	return out
}

// EMA 是指数移动平均；用前 n 个有效值的 SMA 作为种子，再按 2/(n+1) 递推。
func EMA(values []float64, n int) []float64 { return emaSeries(values, n) }

// emaSeries 允许序列前段是 NaN（如 MACD 的 DIF），从第一个有效值开始计算。
func emaSeries(values []float64, n int) []float64 {
	out := newSeries(len(values))
	if n <= 0 || len(values) == 0 {
		return out
	}
	first := -1
	for i, v := range values {
		if valid(v) {
			first = i
			break
		}
	}
	if first < 0 || first+n > len(values) {
		return out
	}
	sum := 0.0
	for i := first; i < first+n; i++ {
		if !valid(values[i]) {
			return out
		}
		sum += values[i]
	}
	prev := sum / float64(n)
	out[first+n-1] = prev
	k := 2.0 / float64(n+1)
	for i := first + n; i < len(values); i++ {
		if !valid(values[i]) {
			continue
		}
		prev = values[i]*k + prev*(1-k)
		out[i] = prev
	}
	return out
}

// RSI 是 Wilder 相对强弱指标；第 n 个点开始有值。
// 全涨（无下跌）返回 100，全跌返回 0，完全不动返回 50。
func RSI(values []float64, n int) []float64 {
	out := newSeries(len(values))
	if n <= 0 || len(values) <= n {
		return out
	}
	gains, losses := 0.0, 0.0
	for i := 1; i <= n; i++ {
		d := values[i] - values[i-1]
		if d >= 0 {
			gains += d
		} else {
			losses -= d
		}
	}
	avgGain := gains / float64(n)
	avgLoss := losses / float64(n)
	out[n] = rsiValue(avgGain, avgLoss)
	for i := n + 1; i < len(values); i++ {
		d := values[i] - values[i-1]
		g, l := 0.0, 0.0
		if d >= 0 {
			g = d
		} else {
			l = -d
		}
		avgGain = (avgGain*float64(n-1) + g) / float64(n)
		avgLoss = (avgLoss*float64(n-1) + l) / float64(n)
		out[i] = rsiValue(avgGain, avgLoss)
	}
	return out
}

func rsiValue(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		if avgGain == 0 {
			return 50
		}
		return 100
	}
	return 100 - 100/(1+avgGain/avgLoss)
}

// MACD 返回 DIF、DEA 与柱状值（柱 = DIF - DEA，未乘 2）。
// 第 slow-1 个点开始有 DIF，DEA 再滞后 signal-1 个点。
func MACD(values []float64, fast, slow, signal int) (dif, dea, macd []float64) {
	dif = newSeries(len(values))
	dea = newSeries(len(values))
	macd = newSeries(len(values))
	if fast <= 0 || slow <= 0 || signal <= 0 || fast >= slow || len(values) < slow {
		return dif, dea, macd
	}
	emaFast := emaSeries(values, fast)
	emaSlow := emaSeries(values, slow)
	for i := range values {
		if valid(emaFast[i]) && valid(emaSlow[i]) {
			dif[i] = emaFast[i] - emaSlow[i]
		}
	}
	dea = emaSeries(dif, signal)
	for i := range values {
		if valid(dif[i]) && valid(dea[i]) {
			macd[i] = dif[i] - dea[i]
		}
	}
	return dif, dea, macd
}

// ATR 是 Wilder 平均真实波幅；真实波幅含跳空（与上一根收盘比）。
func ATR(bars []contract.Bar, n int) []float64 {
	out := newSeries(len(bars))
	if n <= 0 || len(bars) < n {
		return out
	}
	tr := make([]float64, len(bars))
	for i, b := range bars {
		if i == 0 {
			tr[i] = b.High - b.Low
			continue
		}
		prevClose := bars[i-1].Close
		tr[i] = math.Max(b.High-b.Low, math.Max(math.Abs(b.High-prevClose), math.Abs(b.Low-prevClose)))
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += tr[i]
	}
	out[n-1] = sum / float64(n)
	for i := n; i < len(bars); i++ {
		out[i] = (out[i-1]*float64(n-1) + tr[i]) / float64(n)
	}
	return out
}

// BOLL 返回中轨（SMA）与上下轨；k 通常取 2，标准差用总体口径。
func BOLL(values []float64, n int, k float64) (mid, upper, lower []float64) {
	mid = SMA(values, n)
	upper = newSeries(len(values))
	lower = newSeries(len(values))
	if n <= 0 || k <= 0 || len(values) < n {
		return mid, upper, lower
	}
	for i := n - 1; i < len(values); i++ {
		m := mid[i]
		if !valid(m) {
			continue
		}
		sq := 0.0
		for j := i - n + 1; j <= i; j++ {
			d := values[j] - m
			sq += d * d
		}
		sd := math.Sqrt(sq / float64(n))
		upper[i] = m + k*sd
		lower[i] = m - k*sd
	}
	return mid, upper, lower
}

// KDJ 返回 K、D、J；种子 K=D=50，J = 3K - 2D。
func KDJ(bars []contract.Bar, n, kPeriod, dPeriod int) (k, d, j []float64) {
	k = newSeries(len(bars))
	d = newSeries(len(bars))
	j = newSeries(len(bars))
	if n <= 0 || kPeriod <= 0 || dPeriod <= 0 || len(bars) < n {
		return k, d, j
	}
	prevK, prevD := 50.0, 50.0
	for i := n - 1; i < len(bars); i++ {
		high, low := bars[i].High, bars[i].Low
		for x := i - n + 1; x <= i; x++ {
			if bars[x].High > high {
				high = bars[x].High
			}
			if bars[x].Low < low {
				low = bars[x].Low
			}
		}
		rsv := 50.0
		if high > low {
			rsv = (bars[i].Close - low) / (high - low) * 100
		}
		curK := (float64(kPeriod-1)*prevK + rsv) / float64(kPeriod)
		curD := (float64(dPeriod-1)*prevD + curK) / float64(dPeriod)
		k[i], d[i] = curK, curD
		j[i] = 3*curK - 2*curD
		prevK, prevD = curK, curD
	}
	return k, d, j
}

// MaxDrawdown 返回最大回撤（正的小数：0.25 表示 -25%）。少于两个点返回 0。
func MaxDrawdown(equity []float64) float64 {
	if len(equity) < 2 {
		return 0
	}
	peak := equity[0]
	maxDD := 0.0
	for _, v := range equity[1:] {
		if v > peak {
			peak = v
			continue
		}
		if peak <= 0 {
			continue
		}
		if dd := (peak - v) / peak; dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

// Returns 把价格序列转成简单收益率序列（长度 len-1）；前值为 0 的点跳过。
func Returns(prices []float64) []float64 {
	if len(prices) < 2 {
		return nil
	}
	out := make([]float64, 0, len(prices)-1)
	for i := 1; i < len(prices); i++ {
		prev := prices[i-1]
		if prev == 0 {
			continue
		}
		out = append(out, prices[i]/prev-1)
	}
	return out
}

// AnnualVolatility 用样本标准差（n-1）年化收益率波动；不足两个点返回 0。
func AnnualVolatility(returns []float64, periodsPerYear int) float64 {
	if len(returns) < 2 || periodsPerYear <= 0 {
		return 0
	}
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))
	sq := 0.0
	for _, r := range returns {
		d := r - mean
		sq += d * d
	}
	return math.Sqrt(sq/float64(len(returns)-1)) * math.Sqrt(float64(periodsPerYear))
}

// AnnualVolatilityFromPrices 是「价格序列直算年化波动」的便捷入口。
func AnnualVolatilityFromPrices(prices []float64, periodsPerYear int) float64 {
	return AnnualVolatility(Returns(prices), periodsPerYear)
}

// VolumeRatio（量比）是当日成交量与前 n 日均量之比；前 n 根无值。
// 均值口径排除当日，避免自身把分母抬高。
func VolumeRatio(bars []contract.Bar, n int) []float64 {
	out := newSeries(len(bars))
	if n <= 0 || len(bars) <= n {
		return out
	}
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += bars[i].Volume
	}
	for i := n; i < len(bars); i++ {
		mean := sum / float64(n)
		if mean > 0 {
			out[i] = bars[i].Volume / mean
		}
		sum += bars[i].Volume - bars[i-n].Volume
	}
	return out
}
