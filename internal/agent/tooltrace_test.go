package agent

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// toolEventView 是测试用的 agent.tool 事件镜像结构（字段名与前端契约一致）。
type toolEventView struct {
	SessionID string `json:"sessionId"`
	Worker    string `json:"worker"`
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Tool      string `json:"tool"`
	Meta      string `json:"meta"`
	Args      string `json:"args"`
	Result    string `json:"result"`
	OK        *bool  `json:"ok"`
	Error     string `json:"error"`
	MS        *int64 `json:"ms"`
	Step      int    `json:"step"`
	At        string `json:"at"`
}

// waitToolEvents 等待 agent.tool 事件到齐后按发出时刻排序返回。
// 事件总线是异步广播（每条事件一个 goroutine），所以顺序以 Publish 时刻为准，
// 不能用订阅回调的落库顺序断言。
func waitToolEvents(t *testing.T, sink *eventSink, want int) []toolEventView {
	t.Helper()
	if !waitFor(t, 2*time.Second, func() bool { return len(sink.byTopic("agent.tool")) >= want }) {
		t.Fatalf("等待 agent.tool 事件超时：期望 %d 条，实际 %d 条", want, len(sink.byTopic("agent.tool")))
	}
	events := sink.byTopic("agent.tool")
	if len(events) != want {
		t.Fatalf("agent.tool 事件条数不对：期望 %d，实际 %d", want, len(events))
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	out := make([]toolEventView, 0, len(events))
	for _, e := range events {
		var v toolEventView
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			t.Fatalf("agent.tool payload 必须是 JSON：%v", err)
		}
		out = append(out, v)
	}
	return out
}

// TestToolTraceStartEnd 验证一次成功工具调用的两条事件：start 在前、end 在后，
// tool.call 解析出内层工具名/参数，且 step/id 在同一次调用内一致。
func TestToolTraceStartEnd(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		{Role: contract.RoleAssistant, Content: "结论：600000 现价 12.34（来源：market.quote）"},
	}}
	rt, _, _, sink := newTestRuntime(t, llm)

	report, err := rt.Run(context.Background(), "postmarket", "看一下 600000", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	ev := waitToolEvents(t, sink, 2)
	start, end := ev[0], ev[1]
	if start.Phase != toolTraceStart || end.Phase != toolTraceEnd {
		t.Fatalf("两条事件必须是 start 在前、end 在后，实际: %q → %q", start.Phase, end.Phase)
	}
	if start.Tool != "market.quote" || start.Meta != "tool.call" {
		t.Fatalf("tool.call 的展示工具名应解析为内层工具，实际 tool=%q meta=%q", start.Tool, start.Meta)
	}
	if start.Args != `{"codes":["600000"]}` {
		t.Fatalf("tool.call 的 args 应是内层 args 原文，实际 %q", start.Args)
	}
	if start.SessionID != report.SessionID || start.Worker != "postmarket" {
		t.Fatalf("事件应绑定当前会话/worker，实际 %q/%q", start.SessionID, start.Worker)
	}
	if start.Step != 1 || end.Step != 1 {
		t.Fatalf("同一次调用的 start/end 必须是同一个 step=1，实际 %d/%d", start.Step, end.Step)
	}
	wantID := report.SessionID + "-1"
	if start.ID != wantID || end.ID != wantID {
		t.Fatalf("id 应为 <sessionId>-<step>=%q，实际 %q/%q", wantID, start.ID, end.ID)
	}
	// start 不带 result/ok/ms/error。
	if start.Result != "" || start.OK != nil || start.MS != nil || start.Error != "" {
		t.Fatalf("start 不应携带 result/ok/ms/error，实际: %+v", start)
	}
	// end 必须带 ok/ms 与结果预览。
	if end.OK == nil || !*end.OK {
		t.Fatalf("成功的 end 必须 ok=true，实际 %+v", end.OK)
	}
	if end.MS == nil || *end.MS < 0 {
		t.Fatalf("end 必须带非负耗时 ms，实际 %+v", end.MS)
	}
	if end.Result == "" {
		t.Fatal("成功的 end 必须有结果预览")
	}
	if _, err := time.Parse(time.RFC3339, start.At); err != nil {
		t.Fatalf("at 必须是 RFC3339 时间，实际 %q: %v", start.At, err)
	}
}

// TestToolTraceFailure 验证执行失败的工具：end 的 ok=false、error 非空、result 为空。
func TestToolTraceFailure(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.unknown","args":{}}`),
		{Role: contract.RoleAssistant, Content: "工具不存在，我改用 tool.search 检索后重试。"},
	}}
	rt, _, _, sink := newTestRuntime(t, llm)

	if _, err := rt.Run(context.Background(), "postmarket", "试一个不存在的工具", ""); err != nil {
		t.Fatalf("单次工具失败不应让整轮运行失败: %v", err)
	}
	ev := waitToolEvents(t, sink, 2)
	start, end := ev[0], ev[1]
	if start.Phase != toolTraceStart || end.Phase != toolTraceEnd {
		t.Fatalf("事件阶段顺序不对：%q → %q", start.Phase, end.Phase)
	}
	if start.Tool != "market.unknown" {
		t.Fatalf("解析失败也要展示内层工具名，实际 %q", start.Tool)
	}
	if end.OK == nil || *end.OK {
		t.Fatalf("失败的 end 必须 ok=false，实际 %+v", end.OK)
	}
	if end.Error == "" {
		t.Fatal("失败的 end 必须带 error")
	}
	if end.Result != "" {
		t.Fatalf("失败的 end 不应有结果预览，实际 %q", end.Result)
	}
}

// TestToolTraceStepIncrements 验证同一次运行里的第二次工具调用：step=2 且 id 与第一次不同。
func TestToolTraceStepIncrements(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		asstToolCall("c2", "tool.call", `{"name":"kline.analyze","args":{"code":"600000"}}`),
		{Role: contract.RoleAssistant, Content: "结论：行情与 K 线都看多。"},
	}}
	rt, _, _, sink := newTestRuntime(t, llm)

	report, err := rt.Run(context.Background(), "postmarket", "行情和 K 线一起看", "")
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	ev := waitToolEvents(t, sink, 4)
	wantPhases := []string{toolTraceStart, toolTraceEnd, toolTraceStart, toolTraceEnd}
	wantSteps := []int{1, 1, 2, 2}
	for i := range ev {
		if ev[i].Phase != wantPhases[i] || ev[i].Step != wantSteps[i] {
			t.Fatalf("第 %d 条事件 phase/step 不对：期望 %q/%d，实际 %q/%d",
				i, wantPhases[i], wantSteps[i], ev[i].Phase, ev[i].Step)
		}
	}
	if ev[0].ID != report.SessionID+"-1" || ev[2].ID != report.SessionID+"-2" {
		t.Fatalf("两次调用的 id 应分别是 <sessionId>-1 与 <sessionId>-2，实际 %q/%q", ev[0].ID, ev[2].ID)
	}
	if ev[0].ID == ev[2].ID {
		t.Fatalf("第二次调用的 id 必须与第一次不同，实际都是 %q", ev[0].ID)
	}
	if ev[2].Tool != "kline.analyze" {
		t.Fatalf("第二次调用应展示内层工具 kline.analyze，实际 %q", ev[2].Tool)
	}
}

// TestToolTraceHookDisabled 验证关闭钩子（OnTool=nil）时工具照常执行且不发 agent.tool。
func TestToolTraceHookDisabled(t *testing.T) {
	// ReAct 层：不设置 OnTool 时工具正常执行、返回给模型的文本不变。
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"market.quote","args":{"codes":["600000"]}}`),
		{Role: contract.RoleAssistant, Content: "结论：600000 现价 12.34。"},
	}}
	run, err := buildReact(context.Background(), reactConfig{LLM: llm, Gateway: gw, MaxSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	res, err := run.Run(context.Background(), "看一下 600000")
	if err != nil {
		t.Fatalf("关闭轨迹钩子不应影响 ReAct 循环: %v", err)
	}
	if res.ToolCalls != 1 {
		t.Fatalf("工具应照常执行，实际 ToolCalls=%d", res.ToolCalls)
	}
	if names := host.callNames(); len(names) != 1 || names[0] != "market.quote" {
		t.Fatalf("tool.call 应照常经宿主执行，实际 %v", names)
	}

	// Runtime 层：没有任何工具调用的运行不应产生 agent.tool 噪声。
	rt, _, _, sink := newTestRuntime(t, &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "结论：空仓等待。"},
	}})
	if _, err := rt.Run(context.Background(), "postmarket", "复盘今天", ""); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	waitForTopic(t, sink, "agent.state")
	if got := len(sink.byTopic("agent.tool")); got != 0 {
		t.Fatalf("没有工具调用时不应发 agent.tool，实际 %d 条", got)
	}
}

// TestToolTraceFallsBackAndCoversMetaTools 验证两条边界：
// tool.call 参数解析失败时 tool 保留为 tool.call；非 tool.call 的元工具本身就是展示工具名。
func TestToolTraceFallsBackAndCoversMetaTools(t *testing.T) {
	host := testHost()
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	var traces []ToolTrace
	rec := func(tr ToolTrace) { traces = append(traces, tr) }

	var callTool, searchTool *invokableTool
	for _, b := range einoTools(gw, rec, nil) {
		it := b.(*invokableTool)
		switch it.def.Name {
		case "tool.call":
			callTool = it
		case "tool.search":
			searchTool = it
		}
	}
	if callTool == nil || searchTool == nil {
		t.Fatal("网关应提供 tool.call 与 tool.search 两个元工具")
	}

	// tool.call 参数不是合法 JSON：轨迹照发，但解析失败时 tool 留成元工具自身。
	if _, err := callTool.InvokableRun(context.Background(), `{"name":`); err != nil {
		t.Fatalf("轨迹解析失败不应让工具返回 error: %v", err)
	}
	// tool.search 不是 tool.call：tool 与 meta 相同，args 用原始参数。
	if _, err := searchTool.InvokableRun(context.Background(), `{"query":"quote"}`); err != nil {
		t.Fatalf("tool.search 执行失败: %v", err)
	}

	if len(traces) != 4 {
		t.Fatalf("两次工具执行应产生 4 条轨迹，实际 %d", len(traces))
	}
	for i, tr := range traces {
		if tr.Phase != []string{toolTraceStart, toolTraceEnd, toolTraceStart, toolTraceEnd}[i] {
			t.Fatalf("第 %d 条轨迹阶段不对：%q", i, tr.Phase)
		}
	}
	if traces[0].Tool != "tool.call" || traces[0].Meta != "tool.call" {
		t.Fatalf("解析失败应保留 tool.call，实际 tool=%q meta=%q", traces[0].Tool, traces[0].Meta)
	}
	if traces[1].OK || traces[1].Error == "" {
		t.Fatalf("参数非法时 end 应是失败轨迹，实际: %+v", traces[1])
	}
	if traces[2].Tool != "tool.search" || traces[2].Meta != "tool.search" || traces[2].Args != `{"query":"quote"}` {
		t.Fatalf("非 tool.call 元工具的展示名/参数不对，实际: %+v", traces[2])
	}
	if !traces[3].OK || traces[3].Result == "" {
		t.Fatalf("tool.search 成功时 end 必须有结果，实际: %+v", traces[3])
	}
}
