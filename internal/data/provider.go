// Package data 提供行情数据源。
//
// 两个实现：
//   - Synthetic：确定性合成数据，离线可跑，用于学习/回测/单测
//   - EastMoney：东方财富公开接口，真实 A 股日线
//
// 接口只有一个方法，这样 Agent 的工具、回测引擎、策略都不依赖具体数据源。
package data

import (
"context"

"github.com/yazzyk/stock-agent/internal/market"
)

// Provider 是行情数据源。
type Provider interface {
// Daily 返回最近 limit 根日线，按时间升序。
Daily(ctx context.Context, code string, limit int) (market.Series, error)
// Name 返回数据源名称，用于日志与溯源。
Name() string
}
