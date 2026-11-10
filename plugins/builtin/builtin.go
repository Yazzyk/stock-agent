// Package builtin 通过 blank import 装配全部内置插件。
// 新增插件：写 plugins/<name> 包，在 init 里 all.Register(...)，然后在这里加一行 import。
package builtin

import (
	_ "github.com/yazzyk/stock-agent/plugins/agentlab"
	_ "github.com/yazzyk/stock-agent/plugins/all"
	_ "github.com/yazzyk/stock-agent/plugins/fundflow"
	_ "github.com/yazzyk/stock-agent/plugins/kline"
	_ "github.com/yazzyk/stock-agent/plugins/knowledge"
	_ "github.com/yazzyk/stock-agent/plugins/modelsettings"
	_ "github.com/yazzyk/stock-agent/plugins/quant"
	_ "github.com/yazzyk/stock-agent/plugins/watchlist"
)
