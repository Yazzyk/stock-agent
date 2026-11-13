package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 本文件覆盖 A 项「模型没有返回内容」的四类场景：
// 只有 reasoning_content、完全空回复、触顶截断、正常回复。
// 断言的重点是：**真实原因不能被吞掉**，必须能从 Report 的机读字段与正文里定位。

// replyAt 返回一次运行的报告（断言失败的统一入口）。
func runOnce(t *testing.T, llm contract.LLMService, worker, input string) Report {
	t.Helper()
	rt, _, _, _ := newTestRuntime(t, llm)
	report, err := rt.Run(context.Background(), worker, input, "")
	if err != nil {
		t.Fatalf("运行失败: %v", err)
	}
	return report
}

// TestEmptyReplyReasoningOnlyFallsBackToContent 只有 reasoning_content 的回复：
// 正文必须回退成推理内容并**明确标注**，同时把原因标成 reasoning_only。
func TestEmptyReplyReasoningOnlyFallsBackToContent(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{{
		Role:             contract.RoleAssistant,
		Content:          "",
		ReasoningContent: "先确认用户问的是集合竞价，再看规则：9:15-9:20 可撤单…",
	}}}
	report := runOnce(t, llm, "chat", "什么是集合竞价")

	if strings.TrimSpace(report.Report) == "" {
		t.Fatal("推理内容必须回退成正文，不能是空报告")
	}
	if !strings.Contains(report.Report, reasoningFallbackNote) {
		t.Fatalf("回退的正文必须带明确标注 %q，实际: %q", reasoningFallbackNote, report.Report)
	}
	if !strings.Contains(report.Report, "9:15-9:20 可撤单") {
		t.Fatalf("推理内容不能被丢弃，实际: %q", report.Report)
	}
	if !report.ContentFromReasoning {
		t.Fatal("ContentFromReasoning 必须为 true，前端据此提示\"这不是正文\"")
	}
	if report.EmptyReason != EmptyReasonReasoningOnly {
		t.Fatalf("EmptyReason 应为 %s，实际 %q", EmptyReasonReasoningOnly, report.EmptyReason)
	}
	if report.Error != "" {
		t.Fatalf("有回退内容时不该报错，实际 %q", report.Error)
	}
	if len(report.Diagnostics) == 0 {
		t.Fatal("必须给出排查线索")
	}
}

// TestEmptyReplyCompletelyEmptyIsDiagnosed 完全空回复：不能用笼统文案，要能定位到上游空 chunk。
func TestEmptyReplyCompletelyEmptyIsDiagnosed(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{{Role: contract.RoleAssistant, Content: ""}}}
	report := runOnce(t, llm, "chat", "今天大盘怎么样")

	if report.EmptyReason != EmptyReasonNoResponse {
		t.Fatalf("EmptyReason 应为 %s，实际 %q", EmptyReasonNoResponse, report.EmptyReason)
	}
	// 旧的笼统文案必须消失：它把真实原因吞掉了。
	if strings.Contains(report.Report, "可能被内容过滤或超时") {
		t.Fatalf("不能再用笼统兜底文案，实际: %q", report.Report)
	}
	for _, want := range []string{"上游模型返回了空响应", "建议：", "排查线索：", EmptyReasonNoResponse} {
		if !strings.Contains(report.Report, want) {
			t.Fatalf("诊断正文缺少 %q，实际: %q", want, report.Report)
		}
	}
	if len(report.Diagnostics) < 2 {
		t.Fatalf("至少要给出 2 条排查线索，实际 %v", report.Diagnostics)
	}
}

// TestEmptyReplyTruncatedByMaxTokens 触顶截断（finish_reason=length）：
// 必须点明"输出预算被推理吃掉"并给出调大 maxOutputTokens 的建议。
func TestEmptyReplyTruncatedByMaxTokens(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{{
		Role:         contract.RoleAssistant,
		Content:      "",
		FinishReason: "length",
	}}}
	report := runOnce(t, llm, "quant", "回测首板次日溢价")

	if report.EmptyReason != EmptyReasonTruncated {
		t.Fatalf("EmptyReason 应为 %s，实际 %q", EmptyReasonTruncated, report.EmptyReason)
	}
	if report.FinishReason != "length" {
		t.Fatalf("FinishReason 必须透传，实际 %q", report.FinishReason)
	}
	for _, want := range []string{"finish_reason=length", "max_tokens", "推理模型"} {
		if !strings.Contains(report.Report, want) {
			t.Fatalf("截断诊断缺少 %q，实际: %q", want, report.Report)
		}
	}
	// 建议里必须带得动预算的东西：maxOutputTokens 或 8192。
	if !strings.Contains(report.Report, "8192") {
		t.Fatalf("截断建议应给出具体预算（8192），实际: %q", report.Report)
	}
}

// TestEmptyReplyContentFilter 上游内容过滤：要明确说是过滤，不是超时。
func TestEmptyReplyContentFilter(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{{
		Role:         contract.RoleAssistant,
		Content:      "",
		FinishReason: "content_filter",
	}}}
	report := runOnce(t, llm, "chat", "给我一个必涨股票")

	if report.EmptyReason != EmptyReasonContentFilter {
		t.Fatalf("EmptyReason 应为 %s，实际 %q", EmptyReasonContentFilter, report.EmptyReason)
	}
	if !strings.Contains(report.Report, "内容过滤") || strings.Contains(report.Report, "可能被内容过滤或超时") {
		t.Fatalf("过滤要与超时区分开，实际: %q", report.Report)
	}
}

// TestNormalReplyKeepsEmptyFieldsClean 正常回复：不许误报诊断，正文原样返回。
func TestNormalReplyKeepsEmptyFieldsClean(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{{
		Role:         contract.RoleAssistant,
		Content:      "结论：情绪回暖，关注连板高度（来源：kb.search）",
		FinishReason: "stop",
	}}}
	report := runOnce(t, llm, "postmarket", "复盘今天")

	if !strings.Contains(report.Report, "情绪回暖") {
		t.Fatalf("正文应原样返回，实际: %q", report.Report)
	}
	if report.EmptyReason != "" {
		t.Fatalf("正常回复不该有 EmptyReason，实际 %q", report.EmptyReason)
	}
	if report.ContentFromReasoning {
		t.Fatal("正常回复不该被标成推理回退")
	}
	if report.FinishReason != "stop" {
		t.Fatalf("FinishReason 应为 stop，实际 %q", report.FinishReason)
	}
	if strings.Contains(report.Report, reasoningFallbackNote) {
		t.Fatalf("正常正文不该带推理回退标注，实际: %q", report.Report)
	}
}

// TestOutputTokenBudgetDefaultIsRaised 输出预算默认值必须是 8192（推理模型的正文才不会被挤空）。
func TestOutputTokenBudgetDefaultIsRaised(t *testing.T) {
	if defaultMaxOutputTokens != 8192 {
		t.Fatalf("默认输出预算必须是 8192，实际 %d", defaultMaxOutputTokens)
	}
	// 不配策略时用默认值。
	rt := &Runtime{}
	if got := rt.maxOutputTokens(); got != 8192 {
		t.Fatalf("未配策略时应取 8192，实际 %d", got)
	}
	// Policy.MaxTokensPerRun 更小时以它为准（不放大成本上限）。
	rt = &Runtime{opts: RuntimeOptions{Policy: contract.Policy{MaxTokensPerRun: 4096}}}
	if got := rt.maxOutputTokens(); got != 4096 {
		t.Fatalf("策略更小时应取 4096，实际 %d", got)
	}
	// 策略更大时不跟着放大（单次输出仍按 8192）。
	rt = &Runtime{opts: RuntimeOptions{Policy: contract.Policy{MaxTokensPerRun: 200000}}}
	if got := rt.maxOutputTokens(); got != 8192 {
		t.Fatalf("策略更大时单次输出仍应是 8192，实际 %d", got)
	}
}

// TestEmptyReplyToolOnlyIsDiagnosed 只调工具没写结论：要能定位到「结论没写」。
func TestEmptyReplyToolOnlyIsDiagnosed(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		{Role: contract.RoleAssistant, Content: ""},
	}}
	report := runOnce(t, llm, "intraday", "看下 600000 现在多少钱")

	if report.EmptyReason != EmptyReasonToolOnly {
		t.Fatalf("EmptyReason 应为 %s，实际 %q", EmptyReasonToolOnly, report.EmptyReason)
	}
	if !strings.Contains(report.Report, "工具调用") {
		t.Fatalf("诊断应说明「调了工具但没写结论」，实际: %q", report.Report)
	}
}

// TestDiagnosticsJSONShape 诊断字段的 JSON 名必须稳定（前端按此读）。
func TestDiagnosticsJSONShape(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{{
		Role:         contract.RoleAssistant,
		Content:      "",
		FinishReason: "length",
	}}}
	report := runOnce(t, llm, "chat", "随便问问")
	buf, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"finishReason", "emptyReason", "diagnostics", "report"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("Report JSON 缺少字段 %q: %s", key, string(buf))
		}
	}
	// report 字段必须始终存在（空串也不能省掉）。
	normal := runOnce(t, &fakeLLM{script: []contract.Message{{Role: contract.RoleAssistant, Content: "有内容"}}}, "chat", "hi")
	buf, _ = json.Marshal(normal)
	if !strings.Contains(string(buf), `"report":`) {
		t.Fatalf("report 字段必须始终存在: %s", string(buf))
	}
}

// TestReasoningFallbackSkipsToolCallMessages 带工具调用的消息不能做正文回退：
// 那种消息正文本来就该为空，把思维链灌进 content 会污染每一步的上下文。
func TestReasoningFallbackSkipsToolCallMessages(t *testing.T) {
	msg := fromContractMessage(contract.Message{
		Role:             contract.RoleAssistant,
		Content:          "",
		ReasoningContent: "我要先查行情，再算溢价",
		ToolCalls:        []contract.ToolCall{{ID: "c1", Name: "tool.call", Arguments: "{}"}},
	})
	if strings.TrimSpace(msg.Content) != "" {
		t.Fatalf("带工具调用的消息不能回退正文，实际 %q", msg.Content)
	}
	if msg.ReasoningContent != "我要先查行情，再算溢价" {
		t.Fatalf("推理原文仍要保留（诊断用），实际 %q", msg.ReasoningContent)
	}

	// 最终答复（无工具调用）才回退，并带标注。
	final := fromContractMessage(contract.Message{
		Role:             contract.RoleAssistant,
		Content:          "",
		ReasoningContent: "规则是 9:15-9:20 可撤单",
	})
	if !strings.HasPrefix(final.Content, reasoningFallbackNote) {
		t.Fatalf("最终答复必须回退并带标注，实际 %q", final.Content)
	}
}
