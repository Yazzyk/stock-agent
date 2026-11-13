package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// ---------- 空回复诊断 ----------
//
// 背景：模型偶尔真的会回一个"什么都没有"的响应（推理模型把预算全花在 reasoning_content、
// 上游内容过滤、流被截断、只调了工具没写结论……）。以前这里统一套一句
// 「模型没有返回内容（可能被内容过滤或超时），请重试。」，把所有真实原因都吞掉了，
// 用户和排障都无从下手。现在改成"可定位的结论"：
//   - Report.EmptyReason 给机器一个稳定的枚举值；
//   - Report.Diagnostics 给出一串可读的排查线索；
//   - Report.Report 本身写清"发生了什么 + 为什么 + 怎么办"。

// 空回复原因（机器可读，稳定枚举，前端可据此给不同的修复入口）。
const (
	// EmptyReasonNoResponse：上游返回了空 chunk，既没有正文也没有推理内容。
	EmptyReasonNoResponse = "no_response"
	// EmptyReasonReasoningOnly：只有推理内容、正文为空（推理模型把预算花在思维链上）。
	EmptyReasonReasoningOnly = "reasoning_only"
	// EmptyReasonTruncated：finish_reason=length，输出预算被截断。
	EmptyReasonTruncated = "truncated"
	// EmptyReasonContentFilter：上游内容过滤拦掉了正文。
	EmptyReasonContentFilter = "content_filter"
	// EmptyReasonSteps：步数上限停机，且没来得及写结论。
	EmptyReasonSteps = "steps"
	// EmptyReasonBudget：token 预算停机，且没来得及写结论。
	EmptyReasonBudget = "budget"
	// EmptyReasonToolOnly：只发起了工具调用，没有给出正文结论。
	EmptyReasonToolOnly = "tool_only"
	// EmptyReasonError：运行期出错（Report.Error 已有原因，这里只是占位）。
	EmptyReasonError = "error"
)

// reasoningFallbackNote 是"只有推理内容"时统一加在正文前的标注。
// 与 internal/llm 的 reasoningFallbackNote 必须保持一致（两处都可能拼这段文本）。
const reasoningFallbackNote = "（以下为模型推理过程，正文为空）"

// reasoningAsContent 把推理内容包装成可展示的正文。
func reasoningAsContent(reasoning string) string {
	body := strings.TrimSpace(reasoning)
	if body == "" {
		return ""
	}
	return reasoningFallbackNote + nl + nl + body
}

// maxOutputTokens 是单次模型调用的输出预算。
// 推理模型（DeepSeek V4 等）会先把预算花在 reasoning_content 上：2048 常常只够推理，
// 正文被挤成空串。8192 能覆盖绝大多数推理模型的思维链 + 正文。
// Policy.MaxTokensPerRun 是整次运行的预算，比单步输出小的时候以它为准（不越权放大成本）。
func (r *Runtime) maxOutputTokens() int {
	if limit := r.opts.Policy.MaxTokensPerRun; limit > 0 && limit < defaultMaxOutputTokens {
		return limit
	}
	return defaultMaxOutputTokens
}

// emptyReplyDiagnosis 是一次"正文为空"的完整诊断结论。
type emptyReplyDiagnosis struct {
	// Reason 是机器可读的枚举（见 EmptyReason* 常量）。
	Reason string
	// Summary 是一句话结论（写进 Report.Report 的第一段）。
	Summary string
	// Advice 是可操作建议。
	Advice string
	// Diagnostics 是逐条排查线索（写进 Report.Diagnostics）。
	Diagnostics []string
}

// diagnoseEmptyReply 根据运行轨迹判断"为什么没有正文"。
// 调用前提：report.Error == ""（有错误时错误本身就是原因）。
func diagnoseEmptyReply(res ReactResult, maxOutputTokens int) emptyReplyDiagnosis {
	d := emptyReplyDiagnosis{}
	finish := strings.TrimSpace(res.FinishReason)
	steps := res.Steps

	if finish != "" {
		d.Diagnostics = append(d.Diagnostics, "上游 finish_reason="+finish)
	}
	d.Diagnostics = append(d.Diagnostics, fmt.Sprintf("步数=%d，工具调用=%d 次，累计 token=%d", steps, res.ToolCalls, res.Tokens))
	if maxOutputTokens > 0 {
		d.Diagnostics = append(d.Diagnostics, fmt.Sprintf("单次输出预算 maxOutputTokens=%d", maxOutputTokens))
	}

	// 1) 上游明确说了结束原因，优先按它定性。
	switch finish {
	case "content_filter":
		d.Reason = EmptyReasonContentFilter
		d.Summary = "上游内容过滤拦掉了模型正文：服务端判定这次回复不适合返回，本次没有可展示内容。"
		d.Advice = "换一种问法、去掉敏感的个股/交易指令类措辞后重试；必要时在“模型设置”里换一个模型。"
		d.Diagnostics = append(d.Diagnostics, "finish_reason=content_filter 表示正文被服务端过滤，不是超时，也不是没配模型")
		return d
	case "length":
		d.Reason = EmptyReasonTruncated
		d.Summary = "模型输出被 max_tokens 截断（finish_reason=length），正文还没开始写就被掐断了。"
		d.Advice = fmt.Sprintf("该模型很可能是推理模型，思维链先把输出预算吃满：请把单次输出预算调大（当前 %d，建议 8192 以上），或换用非推理模型。", maxOutputTokens)
		d.Diagnostics = append(d.Diagnostics, "finish_reason=length：输出预算耗尽，推理模型的 reasoning_content 会优先占用预算")
		return d
	}

	// 2) 只有推理内容、正文为空（没走到内容回退时才会到这里）。
	if strings.TrimSpace(res.Reasoning) != "" {
		d.Reason = EmptyReasonReasoningOnly
		d.Summary = "模型只产出了推理过程（reasoning_content），正文为空：推理模型的思维链占满了输出预算。"
		d.Advice = fmt.Sprintf("把单次输出预算调大（当前 %d，建议 8192 以上）；若能接受把推理过程当结论，可在报告下方展开推理原文。", maxOutputTokens)
		d.Diagnostics = append(d.Diagnostics, "检测到 reasoning_content 非空而 content 为空：该模型是推理模型，正文被推理占满")
		return d
	}

	// 3) 触顶停机：步数 / token 预算。
	if res.Exhausted {
		switch res.ExhaustKind {
		case "steps":
			d.Reason = EmptyReasonSteps
			d.Summary = "达到步数上限后停机，且没来得及写出结论。"
			d.Advice = "缩小问题范围（例如只问一个板块/一个标的），或把 Policy.MaxToolCalls 调大后重试。"
			if res.Note != "" {
				d.Diagnostics = append(d.Diagnostics, "停机说明："+res.Note)
			}
			return d
		case "budget":
			d.Reason = EmptyReasonBudget
			d.Summary = "token 预算耗尽后停机，且没来得及写出结论。"
			d.Advice = "缩小问题范围，或把 Policy.MaxTokensPerRun 调大后重试。"
			if res.Note != "" {
				d.Diagnostics = append(d.Diagnostics, "停机说明："+res.Note)
			}
			return d
		}
	}

	// 4) 只调了工具没写结论。
	if res.ToolCalls > 0 {
		d.Reason = EmptyReasonToolOnly
		d.Summary = fmt.Sprintf("模型发起了 %d 次工具调用，但没有给出正文结论（工具结果拿到了，结论没写）。", res.ToolCalls)
		d.Advice = "重试一次通常就好；若反复出现，说明该模型不擅长「先调工具再写结论」，建议在提示词里明确要求「拿到数据后必须输出结论」。"
		d.Diagnostics = append(d.Diagnostics, "轨迹里有工具调用，但最后一条 assistant 消息正文为空")
		return d
	}

	// 5) 上游返回空 chunk。
	d.Reason = EmptyReasonNoResponse
	d.Summary = "上游模型返回了空响应：既没有正文，也没有推理内容和工具调用。"
	d.Advice = "先重试一次；仍为空时检查模型名/Base URL 是否正确、账号是否欠费限流，或在“模型设置”里换一个模型。"
	d.Diagnostics = append(d.Diagnostics, "未收到任何正文、推理内容或工具调用：属于上游空 chunk，不是本地截断")
	if finish == "" {
		d.Diagnostics = append(d.Diagnostics, "上游没有返回 finish_reason（部分兼容端点会省略）")
	}
	return d
}

// describeEmptyReply 把诊断结论拼成给用户看的正文：
// 第一段是结论，第二段是建议，最后附排查线索（每条一行）。
func describeEmptyReply(d emptyReplyDiagnosis) string {
	var b strings.Builder
	b.WriteString("【本次没有产出正文】" + d.Summary)
	if strings.TrimSpace(d.Advice) != "" {
		b.WriteString(nl + nl + "建议：" + d.Advice)
	}
	if len(d.Diagnostics) > 0 {
		b.WriteString(nl + nl + "排查线索：")
		for _, line := range d.Diagnostics {
			b.WriteString(nl + "- " + line)
		}
	}
	b.WriteString(nl + nl + "（原因代码：" + d.Reason + "）")
	return b.String()
}

// diagnosticsFor 给有正文但触顶截断的运行补一条诊断（正文存在时不覆盖报告）。
func truncationDiagnostics(res ReactResult) []string {
	out := make([]string, 0, 3)
	if res.Exhausted {
		switch res.ExhaustKind {
		case "budget":
			out = append(out, "运行因 token 预算耗尽停机（Policy.MaxTokensPerRun）")
		case "steps":
			out = append(out, "运行因步数上限停机（Policy.MaxToolCalls）")
		}
		if res.Note != "" {
			out = append(out, "停机说明："+res.Note)
		}
	}
	if strings.TrimSpace(res.FinishReason) == "length" {
		out = append(out, "最后一次模型输出被 max_tokens 截断（finish_reason=length）")
	}
	return out
}

// tokenBudgetNote 给前端一段可读的 token 预算说明（触顶时写进 Diagnostics）。
func tokenBudgetNote(used, limit int) string {
	if limit <= 0 {
		return "累计 token=" + strconv.Itoa(used)
	}
	return "累计 token=" + strconv.Itoa(used) + "，预算上限=" + strconv.Itoa(limit)
}
