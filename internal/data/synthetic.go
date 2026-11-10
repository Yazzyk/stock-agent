package data

import (
"context"
"fmt"
"math"
"math/rand"
"time"

"github.com/yazzyk/stock-agent/internal/market"
)

// Synthetic 是确定性合成行情：同一 code + 同一 endDate 永远得到同一序列。
//
// 为什么需要它？
//  1. 回测与策略研究需要「可复现」——真数据会被除权、退市、停牌污染；
//  2. 没有网络也能把整条链路跑通；
//  3. 你可以在已知漂移/波动的前提下，验证回测引擎和指标算得对不对。
type Synthetic struct {
// End 是最后一根 K 线的日期，零值表示今天。
End time.Time
// Drift 是年化漂移，0.08 表示年化 8%。
Drift float64
// Vol 是年化波动率，0.30 表示 30%。
Vol float64
}

// Name 返回数据源名称。
func (s *Synthetic) Name() string { return "synthetic" }

// Daily 生成 limit 根日线。
func (s *Synthetic) Daily(ctx context.Context, code string, limit int) (market.Series, error) {
if err := ctx.Err(); err != nil {
return market.Series{}, err
}
if limit <= 0 {
return market.Series{}, fmt.Errorf("data: limit 必须为正")
}

end := s.End
if end.IsZero() {
end = time.Now()
}
drift, vol := s.Drift, s.Vol
if drift == 0 {
drift = 0.08
}
if vol == 0 {
vol = 0.30
}

// 用 code 做种子：同一只票的历史是稳定的
seed := int64(0)
for _, r := range code {
seed = seed*131 + int64(r)
}
rng := rand.New(rand.NewSource(seed))

// 先按自然日生成交易日（跳过周末），再倒推 limit 天
days := make([]time.Time, 0, limit*2)
for d := end.AddDate(0, 0, -limit*2); !d.After(end); d = d.AddDate(0, 0, 1) {
if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
continue
}
days = append(days, d)
}
if len(days) > limit {
days = days[len(days)-limit:]
}

// 几何布朗运动 + 轻微均值回复的波动率
mu := drift / 252
sigma := vol / math.Sqrt(252)
price := 10 + rng.Float64()*40 // 起始价 10~50

bars := make([]market.Bar, 0, len(days))
for i, d := range days {
z := rng.NormFloat64()
ret := mu + sigma*z
prev := price
price = price * math.Exp(ret)
if price < 1 {
price = 1
}

open := prev * math.Exp(sigma*rng.NormFloat64()*0.3)
high := math.Max(open, price) * (1 + math.Abs(rng.NormFloat64())*sigma*0.6)
low := math.Min(open, price) * (1 - math.Abs(rng.NormFloat64())*sigma*0.6)

bar := market.Bar{
Date:     d,
Open:     round2(open),
High:     round2(high),
Low:      round2(low),
Close:    round2(price),
Volume:   math.Round(50_000 + rng.Float64()*200_000),
Amount:   math.Round(price * 1000 * (50_000 + rng.Float64()*200_000)),
Turnover: round2(1 + rng.Float64()*5),
}
if i > 0 {
bar.PctChg = round2((bar.Close/bars[i-1].Close - 1) * 100)
}
bars = append(bars, bar)
}

sr := market.Series{Code: code, Name: "合成-" + code, Bars: bars}
if err := sr.Validate(); err != nil {
return market.Series{}, err
}
return sr, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
