// Package market 定义行情数据模型与指标计算。
//
// 设计原则：这一层不关心数据从哪来（东财 / 本地 CSV / 合成数据），
// 也不关心谁在用它（Agent / 回测 / 策略）。它只有事实与算术。
package market

import (
"fmt"
"math"
"time"
)

// Bar 是一根 K 线（默认日线）。
type Bar struct {
Date   time.Time `json:"date"`
Open   float64   `json:"open"`
High   float64   `json:"high"`
Low    float64   `json:"low"`
Close  float64   `json:"close"`
Volume float64   `json:"volume"` // 手
Amount float64   `json:"amount"` // 元
// PctChg 是当日涨跌幅（%），由数据源给出或本地计算。
PctChg float64 `json:"pct_chg"`
// Turnover 是换手率（%），可能缺失。
Turnover float64 `json:"turnover"`
}

// Series 是一只标的的连续 K 线序列，按时间升序。
type Series struct {
Code string `json:"code"`
Name string `json:"name"`
Bars []Bar  `json:"bars"`
}

// Len 返回 K 线数量。
func (s Series) Len() int { return len(s.Bars) }

// Slice 返回 [from, to) 的子序列（浅拷贝切片）。
func (s Series) Slice(from, to int) Series {
if from < 0 {
from = 0
}
if to > len(s.Bars) {
to = len(s.Bars)
}
if from >= to {
return Series{Code: s.Code, Name: s.Name}
}
return Series{Code: s.Code, Name: s.Name, Bars: s.Bars[from:to]}
}

// Closes 提取收盘价序列。
func (s Series) Closes() []float64 {
out := make([]float64, len(s.Bars))
for i, b := range s.Bars {
out[i] = b.Close
}
return out
}

// Last 返回最后一根 K 线。
func (s Series) Last() (Bar, bool) {
if len(s.Bars) == 0 {
return Bar{}, false
}
return s.Bars[len(s.Bars)-1], true
}

// ---------------------------------------------------------------------------
// 指标：都是纯函数，便于单测与回测复用
// ---------------------------------------------------------------------------

// SMA 计算简单移动平均。返回长度与输入相同，前 period-1 个为 NaN。
func SMA(vals []float64, period int) []float64 {
out := make([]float64, len(vals))
for i := range out {
out[i] = math.NaN()
}
if period <= 0 {
return out
}
var sum float64
for i, v := range vals {
sum += v
if i >= period {
sum -= vals[i-period]
}
if i >= period-1 {
out[i] = sum / float64(period)
}
}
return out
}

// EMA 计算指数移动平均。
func EMA(vals []float64, period int) []float64 {
out := make([]float64, len(vals))
if period <= 0 || len(vals) == 0 {
for i := range out {
out[i] = math.NaN()
}
return out
}
k := 2.0 / (float64(period) + 1)
out[0] = vals[0]
for i := 1; i < len(vals); i++ {
out[i] = vals[i]*k + out[i-1]*(1-k)
}
return out
}

// RSI 计算相对强弱指标（Wilder 平滑）。前 period 个为 NaN。
func RSI(vals []float64, period int) []float64 {
out := make([]float64, len(vals))
for i := range out {
out[i] = math.NaN()
}
if len(vals) <= period || period <= 0 {
return out
}
var gain, loss float64
for i := 1; i <= period; i++ {
d := vals[i] - vals[i-1]
if d >= 0 {
gain += d
} else {
loss -= d
}
}
avgGain := gain / float64(period)
avgLoss := loss / float64(period)
out[period] = rsiFrom(avgGain, avgLoss)

for i := period + 1; i < len(vals); i++ {
d := vals[i] - vals[i-1]
g, l := 0.0, 0.0
if d >= 0 {
g = d
} else {
l = -d
}
avgGain = (avgGain*float64(period-1) + g) / float64(period)
avgLoss = (avgLoss*float64(period-1) + l) / float64(period)
out[i] = rsiFrom(avgGain, avgLoss)
}
return out
}

func rsiFrom(avgGain, avgLoss float64) float64 {
if avgLoss == 0 {
return 100
}
rs := avgGain / avgLoss
return 100 - 100/(1+rs)
}

// ATR 计算真实波幅均值。
func ATR(bars []Bar, period int) []float64 {
out := make([]float64, len(bars))
for i := range out {
out[i] = math.NaN()
}
if len(bars) <= period || period <= 0 {
return out
}
tr := make([]float64, len(bars))
for i, b := range bars {
if i == 0 {
tr[i] = b.High - b.Low
continue
}
prev := bars[i-1].Close
tr[i] = math.Max(b.High-b.Low, math.Max(math.Abs(b.High-prev), math.Abs(b.Low-prev)))
}
var sum float64
for i := 1; i <= period; i++ {
sum += tr[i]
}
out[period] = sum / float64(period)
for i := period + 1; i < len(bars); i++ {
out[i] = (out[i-1]*float64(period-1) + tr[i]) / float64(period)
}
return out
}

// MaxDrawdown 返回最大回撤（正数，例如 0.23 表示 23%）。
func MaxDrawdown(equity []float64) float64 {
if len(equity) == 0 {
return 0
}
peak := equity[0]
var mdd float64
for _, v := range equity {
if v > peak {
peak = v
}
if peak > 0 {
dd := (peak - v) / peak
if dd > mdd {
mdd = dd
}
}
}
return mdd
}

// Returns 由净值序列算逐日收益率。
func Returns(equity []float64) []float64 {
if len(equity) < 2 {
return nil
}
out := make([]float64, 0, len(equity)-1)
for i := 1; i < len(equity); i++ {
if equity[i-1] == 0 {
out = append(out, 0)
continue
}
out = append(out, equity[i]/equity[i-1]-1)
}
return out
}

// Sharpe 计算年化夏普（无风险利率按 0 处理，annualDays 通常 252）。
func Sharpe(rets []float64, annualDays int) float64 {
if len(rets) < 2 {
return 0
}
var mean float64
for _, r := range rets {
mean += r
}
mean /= float64(len(rets))

var variance float64
for _, r := range rets {
d := r - mean
variance += d * d
}
variance /= float64(len(rets) - 1)
sd := math.Sqrt(variance)
if sd == 0 {
return 0
}
return mean / sd * math.Sqrt(float64(annualDays))
}

// Validate 检查序列的基本合法性，供数据层在返回前自检。
func (s Series) Validate() error {
if len(s.Bars) == 0 {
return fmt.Errorf("market: 序列 %s 为空", s.Code)
}
for i, b := range s.Bars {
if b.High < b.Low {
return fmt.Errorf("market: 第 %d 根 K 线 high < low（%s）", i, s.Code)
}
if b.Close <= 0 || b.Open <= 0 {
return fmt.Errorf("market: 第 %d 根 K 线价格非正（%s）", i, s.Code)
}
if i > 0 && !b.Date.After(s.Bars[i-1].Date) {
return fmt.Errorf("market: 第 %d 根 K 线时间未递增（%s）", i, s.Code)
}
}
return nil
}
