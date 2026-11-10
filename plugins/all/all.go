// Package all 是插件登记表：插件包在 init 里调用 Register 登记自身工厂，
// 由 plugins/builtin 做 blank import 触发。新增插件只需两处：写包 + 在 builtin 里加一行。
package all

import (
	"sort"

	"github.com/yazzyk/stock-agent/pkg/plugin"
)

var factories []func() plugin.Plugin

// Register 登记一个插件工厂。同一插件包重复 init 不会发生，故不做去重。
func Register(f func() plugin.Plugin) {
	if f == nil {
		return
	}
	factories = append(factories, f)
}

// All 按登记顺序实例化全部插件（顺序由 builtin 的 import 顺序决定）。
func All() []plugin.Plugin {
	out := make([]plugin.Plugin, 0, len(factories))
	for _, f := range factories {
		out = append(out, f())
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Manifest().ID < out[j].Manifest().ID
	})
	return out
}

// Count 返回已登记插件数，供 selftest 判断构建是否漏了 builtin import。
func Count() int { return len(factories) }
