// Package test 是跨包外部验收测试集合（qa ownership）。
//
// 它只读产品代码：通过 kernel 的公开 API 与 plugins 契约断言行为，
// 不 import 任何 internal 实现细节之外的东西，也不修改产品包。
// 单独运行：go test ./test/ -count=1 -v
// 纳入一键验收：scripts/verify.sh（go test ./... 会带上本包）。
package test
