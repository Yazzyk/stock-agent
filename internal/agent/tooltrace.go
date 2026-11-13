package agent

import (
	"encoding/json"
	"strings"
	"time"
)

// 工具轨迹的阶段常量（agent.tool 事件的 phase 字段）。
const (
	toolTraceStart = "start"
	toolTraceEnd   = "end"
)

// 轨迹文本的截断上限（按 rune 计，末尾追加省略号）：
// 事件只用于前端展示，不能因为一条工具结果把事件总线撑爆。
const (
	toolTraceArgsRunes   = 800
	toolTraceResultRunes = 1200
)

// ToolTrace 是一条工具调用轨迹（由 ReAct 层在工具执行前后各产生一条）。
//
// 它只承载"执行现场"的信息（工具名、参数、结果、耗时）；
// 会话/worker/step/id/脱敏截断这些运行期语义由 Runtime 补齐。
type ToolTrace struct {
	// CallSeq 是本次运行内的工具调用序号（从 1 起，start/end 共用同一个值），
	// 仅用于 Runtime 关联 start 与 end 并推导 step，不直接出现在事件里。
	CallSeq int
	// Phase 是阶段：toolTraceStart 或 toolTraceEnd。
	Phase string
	// Meta 是元工具名（tool.search / tool.describe / tool.call）。
	Meta string
	// Tool 是展示用工具名：tool.call 时解析出内层工具名，其它元工具就是自己。
	Tool string
	// Args 是展示用参数原文：tool.call 时是内层 args；未脱敏（Runtime 负责脱敏截断）。
	Args string
	// Result 是工具结果的预览文本（仅 end 有；失败时为空）。
	Result string
	// OK 表示工具是否执行成功（仅 end 有意义）。
	OK bool
	// Error 是工具失败的错误文本（仅 end 失败时非空）。
	Error string
	// MS 是本次工具执行的耗时毫秒数（仅 end 有意义）。
	MS int64
	// At 是本条轨迹产生的时刻（start 为开始时刻，end 为结束时刻）。
	At time.Time
}

// parseToolCallTarget 从 tool.call 的参数里解析内层工具名与内层参数原文。
// 解析失败（不是合法 JSON / name 为空）时返回 ok=false，调用方应保留元工具自身，
// 绝不能因此报错——轨迹只是旁路观测，不能影响工具执行。
func parseToolCallTarget(argsJSON string) (name, innerArgs string, ok bool) {
	var in struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", "", false
	}
	name = strings.TrimSpace(in.Name)
	if name == "" {
		return "", "", false
	}
	innerArgs = strings.TrimSpace(string(in.Args))
	if innerArgs == "" || innerArgs == "null" {
		innerArgs = "{}"
	}
	return name, innerArgs, true
}

// clipTrace 按 rune 截断轨迹文本并追加省略号，避免超长参数/结果进入事件。
func clipTrace(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
