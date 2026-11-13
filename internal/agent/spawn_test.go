package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// TestSessionIDPropagatesToToolContext 验证会话 id 经 ReAct → Gateway → 工具 透传到底
// （agent.spawn 靠它判断"我在哪个会话里"）。
func TestSessionIDPropagatesToToolContext(t *testing.T) {
	var got string
	host := &fakeHost{tools: []plugin.Tool{{
		Name:    "probe.session",
		Title:   "探针",
		Summary: "读取 ctx 里的会话 id",
		Tags:    []string{"agent"},
		Risk:    plugin.RiskRead,
		Run: func(ctx context.Context, _ json.RawMessage) (any, error) {
			id, _ := SessionFrom(ctx)
			got = id
			return map[string]any{"sessionId": id}, nil
		},
	}}}
	gw := progressive.NewGateway(host, nil, nil, plugin.RiskNetwork)
	llm := &fakeLLM{script: []contract.Message{
		asstToolCall("c1", "tool.call", `{"name":"probe.session","args":{}}`),
		{Role: contract.RoleAssistant, Content: "完成"},
	}}
	run, err := buildReact(context.Background(), reactConfig{LLM: llm, Gateway: gw, MaxSteps: 6})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Run(WithSession(context.Background(), "sess-probe"), "探针测试"); err != nil {
		t.Fatalf("ReAct 不应失败: %v", err)
	}
	if got != "sess-probe" {
		t.Fatalf("会话 id 必须透传到工具 ctx，实际 %q", got)
	}
	if id, ok := SessionFrom(context.Background()); ok || id != "" {
		t.Fatal("没有会话 id 的 ctx 应返回 ok=false")
	}
}

// newSpawnedRuntime 准备一个带主会话的运行环境。
func newSpawnedRuntime(t *testing.T, llm contract.LLMService) (*Runtime, *eventSink) {
	t.Helper()
	rt, _, _, sink := newTestRuntime(t, llm)
	if err := rt.sessions.Create(SessionMeta{ID: "p1", Worker: "chat", Title: "主会话", Status: StatusOK}); err != nil {
		t.Fatal(err)
	}
	return rt, sink
}

func TestSpawnRejectsWithoutSession(t *testing.T) {
	rt, _, _, _ := newTestRuntime(t, &fakeLLM{})
	if _, err := rt.Spawn(context.Background(), SpawnRequest{Task: "子任务"}); err == nil ||
		!strings.Contains(err.Error(), "只能在主会话里调用") {
		t.Fatalf("没有会话 id 时应报错，实际: %v", err)
	}
	if _, err := rt.Spawn(WithSession(context.Background(), "p1"), SpawnRequest{Task: "  "}); err == nil ||
		!strings.Contains(err.Error(), "需要 task") {
		t.Fatalf("空 task 应报错，实际: %v", err)
	}
}

func TestSpawnRunsChildAndBuildsTree(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: "子任务结论：主力连续 3 日净流入"},
	}}
	rt, sink := newSpawnedRuntime(t, llm)

	res, err := rt.Spawn(WithSession(context.Background(), "p1"), SpawnRequest{
		Task:   "复盘 603278 最近 5 日的资金流",
		Worker: "quant",
		Label:  "个股复盘",
	})
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if res.Status != StatusOK || res.SessionID == "" || res.ParentID != "p1" {
		t.Fatalf("派生结果不对: %+v", res)
	}
	if !strings.Contains(res.Report, "主力连续 3 日净流入") || res.Truncated {
		t.Fatalf("报告应原样返回且未截断: %+v", res)
	}
	if res.Tokens == 0 || res.Worker != string(WorkerQuant) || res.Label != "个股复盘" {
		t.Fatalf("token/worker/label 不对: %+v", res)
	}
	if !strings.Contains(res.ToolText(), "【子智能体完成】") {
		t.Fatalf("工具结果文本不对: %q", res.ToolText())
	}

	// 子会话元信息：parentId / mode / label 必须落盘。
	detail, err := rt.Session(res.SessionID)
	if err != nil {
		t.Fatalf("读子会话失败: %v", err)
	}
	if detail.Session.ParentID != "p1" || detail.Session.Mode != ModeSubagent || detail.Session.Label != "个股复盘" {
		t.Fatalf("子会话编队信息不对: %+v", detail.Session)
	}
	if detail.Session.Worker != string(WorkerQuant) {
		t.Fatalf("子会话 worker 应为 quant，实际 %s", detail.Session.Worker)
	}

	// 编队树：主会话为根，子会话挂在下面。
	tree, err := rt.Tree(res.SessionID)
	if err != nil {
		t.Fatalf("读编队树失败: %v", err)
	}
	if tree.Root.SessionID != "p1" || tree.Root.Mode != ModeMain {
		t.Fatalf("树根应为主会话: %+v", tree.Root)
	}
	if len(tree.Nodes) != 2 || tree.Nodes[1].ParentID != "p1" || tree.Nodes[1].Mode != ModeSubagent {
		t.Fatalf("后代节点不对: %+v", tree.Nodes)
	}

	// 事件：agent.child 至少 started + finished 两条。
	if !waitFor(t, 2*time.Second, func() bool {
		var started, finished bool
		for _, e := range sink.byTopic("agent.child") {
			var payload map[string]any
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				continue
			}
			switch payload["status"] {
			case StatusRunning:
				started = true
			case StatusOK, StatusFailed:
				finished = true
			}
		}
		return started && finished
	}) {
		t.Fatal("agent.child 必须发 started（running）与 finished（ok/failed）")
	}
	var payload map[string]any
	for _, e := range sink.byTopic("agent.child") {
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("agent.child payload 必须是 JSON: %v", err)
		}
		if p["status"] == StatusOK {
			payload = p
		}
	}
	for _, key := range []string{"parentSessionId", "childSessionId", "label", "status", "tokens", "elapsedMs"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("agent.child 事件缺少 %q: %+v", key, payload)
		}
	}
}

func TestSpawnRejectsSubagentChild(t *testing.T) {
	rt, _ := newSpawnedRuntime(t, &fakeLLM{})
	// 旧数据兼容：只有 parentId、没有 mode 的会话也算子智能体。
	if err := rt.sessions.Create(SessionMeta{ID: "c1", Worker: "chat", Title: "子", ParentID: "p1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Spawn(WithSession(context.Background(), "c1"), SpawnRequest{Task: "再套一层"}); err == nil ||
		!strings.Contains(err.Error(), "子智能体不能再次派生") {
		t.Fatalf("子会话再派生必须被拒，实际: %v", err)
	}
	// 显式 mode=subagent 的会话同样被拒。
	rt.sessions.Create(SessionMeta{ID: "c2", Worker: "chat", Mode: ModeSubagent, ParentID: "p1"})
	if _, err := rt.Spawn(WithSession(context.Background(), "c2"), SpawnRequest{Task: "再套一层"}); err == nil {
		t.Fatal("mode=subagent 的会话再派生必须被拒")
	}
}

func TestSpawnConcurrencyLimit(t *testing.T) {
	rt, _ := newSpawnedRuntime(t, &fakeLLM{})
	for i, id := range []string{"c1", "c2"} {
		if err := rt.sessions.Create(SessionMeta{
			ID:        id,
			Worker:    "chat",
			Title:     "在跑的子智能体",
			ParentID:  "p1",
			Mode:      ModeSubagent,
			Status:    StatusRunning,
			StartedAt: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rt.Spawn(WithSession(context.Background(), "p1"), SpawnRequest{Task: "第三个", Worker: "chat"}); err == nil ||
		!strings.Contains(err.Error(), "最多同时 2 个子智能体") {
		t.Fatalf("超过并发上限必须被拒，实际: %v", err)
	}
	// 已结束的子会话不占名额。
	if err := rt.sessions.Update(SessionMeta{ID: "c1", ParentID: "p1", Status: StatusOK}); err != nil {
		t.Fatal(err)
	}
	if n := rt.runningChildren("p1"); n != 1 {
		t.Fatalf("已结束的子会话不应占名额，实际在跑 %d", n)
	}
}

// TestSpawnCascadeCancel 验证父会话取消时子会话级联取消。
func TestSpawnCascadeCancel(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	llm := &fakeLLM{block: gate}
	rt, _ := newSpawnedRuntime(t, llm)

	ctx, cancel := context.WithCancel(WithSession(context.Background(), "p1"))
	type outcome struct {
		res SpawnResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		// 收敛后 agent.spawn 必须显式指定 worker（不再默认 chat）。
		res, err := rt.Spawn(ctx, SpawnRequest{Task: "跑一个很慢的子任务", Worker: "quant", Label: "慢任务"})
		done <- outcome{res: res, err: err}
	}()
	if !waitFor(t, 3*time.Second, func() bool { return len(rt.Running()) == 1 }) {
		t.Fatal("子会话应进入运行中")
	}

	cancel()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("取消不应把错误抛到父流程: %v", got.err)
		}
		if got.res.Status != StatusFailed {
			t.Fatalf("父会话取消后子任务应标记失败: %+v", got.res)
		}
		if !strings.Contains(got.res.Error, "取消") {
			t.Fatalf("失败原因应是可读中文，实际 %q", got.res.Error)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("父会话取消后子会话必须级联取消")
	}
	if n := len(rt.Running()); n != 0 {
		t.Fatalf("取消后不应再有运行中的会话，实际 %d", n)
	}
}

func TestSpawnTruncatesReport(t *testing.T) {
	llm := &fakeLLM{script: []contract.Message{
		{Role: contract.RoleAssistant, Content: strings.Repeat("研", maxSpawnReportRunes+800)},
	}}
	rt, _ := newSpawnedRuntime(t, llm)
	res, err := rt.Spawn(WithSession(context.Background(), "p1"), SpawnRequest{Task: "写一份长报告", Worker: "postmarket"})
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if !res.Truncated {
		t.Fatal("超长报告应标记 Truncated")
	}
	if n := len([]rune(res.Report)); n <= maxSpawnReportRunes || n > maxSpawnReportRunes+80 {
		t.Fatalf("报告应截断到前 %d 字（含标注），实际 %d 字", maxSpawnReportRunes, n)
	}
	text := res.ToolText()
	if !strings.Contains(text, "已截断") || !strings.Contains(text, "4000") {
		t.Fatalf("截断必须标注清楚，实际: %q", text[len(text)-80:])
	}
}

// TestTreeLegacySession 验证旧会话（没有 parentId/mode）兼容：单个节点也能成树。
func TestTreeLegacySession(t *testing.T) {
	rt, _, _, _ := newTestRuntime(t, &fakeLLM{})
	legacy := SessionMeta{ID: "old-1", Worker: "postmarket", Title: "旧复盘"}
	if err := rt.sessions.Create(legacy); err != nil {
		t.Fatal(err)
	}
	tree, err := rt.Tree("old-1")
	if err != nil {
		t.Fatalf("旧会话读树失败: %v", err)
	}
	if len(tree.Nodes) != 1 || tree.Root.SessionID != "old-1" || tree.Root.Mode != ModeMain || tree.Root.ParentID != "" {
		t.Fatalf("旧会话应是独立主会话节点: %+v", tree)
	}
	if tree.Root.Label != "旧复盘" {
		t.Fatalf("没有 label 时应回落到 title，实际 %q", tree.Root.Label)
	}

	// 反序列化一条没有编队字段的旧 JSON，语义必须是"主会话"。
	var meta SessionMeta
	if err := json.Unmarshal([]byte(`{"id":"old-2","worker":"chat","title":"旧会话","status":"ok"}`), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.IsSubagent() || meta.ModeOrDefault() != ModeMain {
		t.Fatalf("旧 JSON 应被当成主会话：IsSubagent=%v mode=%q", meta.IsSubagent(), meta.ModeOrDefault())
	}
	// 空 sessionId 不再报错：回退成"最近一次主会话的树"（前端首次进面板要能直接渲染）。
	tree, err = rt.Tree("")
	if err != nil {
		t.Fatalf("空 sessionId 应回退到最近主会话，实际报错: %v", err)
	}
	if tree.Root.SessionID != "old-1" || tree.SessionID != "old-1" {
		t.Fatalf("空 sessionId 应回退到最近主会话 old-1，实际 root=%q session=%q", tree.Root.SessionID, tree.SessionID)
	}
	if _, err := rt.Tree("不存在"); err == nil {
		t.Fatal("不存在的会话应报错")
	}

	// 一个会话都没有时，空 sessionId 给出可操作提示而不是空洞报错。
	empty, _, _, _ := newTestRuntime(t, &fakeLLM{})
	if _, err := empty.Tree(""); err == nil || !strings.Contains(err.Error(), "编队") {
		t.Fatalf("没有会话时应给可操作提示，实际: %v", err)
	}
}

// TestTreeShapeIsAuthoritative 验证编队树的权威形状：depth/childCount/排序/root 判定。
func TestTreeShapeIsAuthoritative(t *testing.T) {
	rt, _, _, _ := newTestRuntime(t, &fakeLLM{})
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	// 根 + 2 个同层子节点（故意让 startedAt 乱序）+ 1 个更深的后代。
	seeds := []SessionMeta{
		{ID: "root", Worker: "router", Title: "主会话", Status: StatusOK, StartedAt: base},
		{ID: "c2", Worker: "quant", Title: "后派", ParentID: "root", Mode: ModeSubagent, Status: StatusRunning, StartedAt: base.Add(2 * time.Minute)},
		{ID: "c1", Worker: "postmarket", Title: "先派", ParentID: "root", Mode: ModeSubagent, Status: StatusOK, StartedAt: base.Add(time.Minute)},
		{ID: "g1", Worker: "chat", Title: "孙", ParentID: "c1", Mode: ModeSubagent, Status: StatusOK, StartedAt: base.Add(3 * time.Minute)},
	}
	for _, m := range seeds {
		if err := rt.sessions.Create(m); err != nil {
			t.Fatal(err)
		}
	}
	// 从最深的后代反查，也要回到 root。
	tree, err := rt.Tree("g1")
	if err != nil {
		t.Fatalf("读树失败: %v", err)
	}
	if tree.Root.SessionID != "root" || tree.Root.Depth != 0 || tree.Root.ParentID != "" {
		t.Fatalf("根必须是 parentId 为空的 root，实际 %+v", tree.Root)
	}
	if tree.Total != 4 || len(tree.Nodes) != 4 {
		t.Fatalf("应有 4 个节点，实际 %d：%+v", len(tree.Nodes), tree.Nodes)
	}
	if tree.ActiveCount != 1 {
		t.Fatalf("活跃节点应为 1，实际 %d", tree.ActiveCount)
	}
	if tree.SessionID != "g1" || tree.RootSessionID != "root" {
		t.Fatalf("sessionId/rootSessionId 不对：%q/%q", tree.SessionID, tree.RootSessionID)
	}
	// 排序：(depth, startedAt, sessionId)。
	want := []string{"root", "c1", "c2", "g1"}
	for i, id := range want {
		if tree.Nodes[i].SessionID != id {
			t.Fatalf("节点顺序应为 %v，实际 %+v", want, nodeIDs(tree.Nodes))
		}
	}
	if tree.Nodes[1].Depth != 1 || tree.Nodes[3].Depth != 2 {
		t.Fatalf("depth 计算不对：%+v", tree.Nodes)
	}
	if tree.Nodes[0].ChildCount != 2 || tree.Nodes[1].ChildCount != 1 || tree.Nodes[3].ChildCount != 0 {
		t.Fatalf("childCount 计算不对：%+v", tree.Nodes)
	}
	for _, n := range tree.Nodes {
		if n.Role == "" || n.Role != n.Label {
			t.Fatalf("role 必须等于 label（给前端留的别名），实际 %+v", n)
		}
	}
}

func nodeIDs(nodes []AgentNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.SessionID)
	}
	return out
}
