package progressive

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

type recAudit struct{ events []contract.AuditEvent }

func (a *recAudit) Record(e contract.AuditEvent) { a.events = append(a.events, e) }
func (a *recAudit) List(int) ([]contract.AuditEvent, error) {
	return append([]contract.AuditEvent{}, a.events...), nil
}

type fakeHost struct {
	tools []plugin.Tool
	calls []string
}

func (h *fakeHost) Tools() []plugin.Tool { return h.tools }

func (h *fakeHost) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	h.calls = append(h.calls, name)
	for _, t := range h.tools {
		if t.Name != name {
			continue
		}
		out, err := t.Run(ctx, args)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("工具箱里没有 %s", name)
}

func gatewayTools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name:    "market.quote",
			Title:   "实时快照",
			Summary: "批量个股/指数实时行情快照，盯盘前先看这个",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema:  json.RawMessage(`{"type":"object","properties":{"codes":{"type":"array","items":{"type":"string"}}},"required":["codes"]}`),
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Codes []string `json:"codes"`
				}
				_ = json.Unmarshal(args, &in)
				return map[string]any{"count": len(in.Codes), "codes": in.Codes}, nil
			},
		},
		{
			Name:    "kline.analyze",
			Title:   "K线分析",
			Summary: "K线均线与形态分析，判断趋势结构用",
			Tags:    []string{"kline"},
			Risk:    plugin.RiskRead,
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]any{"trend": "多头"}, nil
			},
		},
		{
			Name:    "flow.rank",
			Title:   "资金流排名",
			Summary: "板块与概念资金流排名，找主线资金去哪了",
			Tags:    []string{"flow"},
			Risk:    plugin.RiskNetwork,
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				return map[string]any{"rank": 1}, nil
			},
		},
	}
}

func TestGatewaySearchByKeyword(t *testing.T) {
	host := &fakeHost{tools: gatewayTools()}
	gw := NewGateway(host, nil, &recAudit{}, plugin.RiskNetwork)

	res, err := gw.Search("kline", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 || res.Hits[0].Name != "kline.analyze" {
		t.Fatalf("关键词 kline 的首条命中应为 kline.analyze，实际: %+v", res.Hits)
	}
	res2, err := gw.Search("资金", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Hits) == 0 || res2.Hits[0].Name != "flow.rank" {
		t.Fatalf("关键词 资金 的首条命中应为 flow.rank，实际: %+v", res2.Hits)
	}
	if _, err := gw.Search("   ", 0); err == nil {
		t.Fatal("空 query 必须报错")
	}
}

func TestGatewayCallUnknownTool(t *testing.T) {
	host := &fakeHost{tools: gatewayTools()}
	audit := &recAudit{}
	gw := NewGateway(host, nil, audit, plugin.RiskNetwork)
	_, err := gw.Call(context.Background(), "market.fake", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("未注册工具必须报错并提示 tool.search，实际: %v", err)
	}
	if len(host.calls) != 0 {
		t.Fatal("未注册工具不得执行")
	}
	if len(audit.events) != 1 || audit.events[0].OK {
		t.Fatalf("拒绝执行必须写审计且 OK=false，实际: %+v", audit.events)
	}
}

func TestGatewayCallRiskDenied(t *testing.T) {
	host := &fakeHost{tools: gatewayTools()}
	audit := &recAudit{}
	gw := NewGateway(host, nil, audit, plugin.RiskRead) // 只允许 read

	_, err := gw.Call(context.Background(), "market.quote", json.RawMessage(`{"codes":["600000"]}`))
	if err == nil || !strings.Contains(err.Error(), "风险等级") {
		t.Fatalf("network 工具在只读网关下必须被拒，实际: %v", err)
	}
	if len(host.calls) != 0 {
		t.Fatal("被拒工具不得执行")
	}
	if len(audit.events) != 1 || audit.events[0].OK {
		t.Fatalf("风险拒绝必须写审计，实际: %+v", audit.events)
	}

	// read 工具应放行。
	res, err := gw.Call(context.Background(), "kline.analyze", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("read 工具应放行: %v", err)
	}
	if !strings.Contains(string(res.Result), "trend") {
		t.Fatalf("调用结果必须回传工具输出: %s", string(res.Result))
	}
	if len(audit.events) != 2 || !audit.events[1].OK {
		t.Fatalf("成功执行必须写审计 OK=true，实际: %+v", audit.events)
	}
}

func TestGatewayNilHost(t *testing.T) {
	gw := NewGateway(nil, nil, nil, plugin.RiskRead)
	if _, err := gw.Call(context.Background(), "market.quote", nil); err == nil || !strings.Contains(err.Error(), "ToolHost") {
		t.Fatalf("Host 为 nil 时应返回可操作的中文错误，实际: %v", err)
	}
	res := gw.Catalog.Index()
	if !strings.Contains(res.Text, "暂无可用工具") {
		t.Fatalf("Host 为 nil 时索引应为空提示，实际: %s", res.Text)
	}
}

func TestMetaToolsCallThroughGateway(t *testing.T) {
	host := &fakeHost{tools: gatewayTools()}
	gw := NewGateway(host, nil, &recAudit{}, plugin.RiskNetwork)
	metas := gw.MetaTools()
	if len(metas) != 3 {
		t.Fatalf("元工具应为 3 个，实际 %d", len(metas))
	}
	byName := map[string]plugin.Tool{}
	for _, m := range metas {
		if err := m.Validate(); err != nil {
			t.Fatalf("元工具不合法: %v", err)
		}
		if m.Risk != plugin.RiskRead {
			t.Fatalf("元工具风险必须是 read，实际 %s", m.Risk)
		}
		byName[m.Name] = m
	}
	// tool.search → tool.call 全链路。
	search, err := byName["tool.search"].Run(context.Background(), json.RawMessage(`{"query":"quote"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(search), "market.quote") {
		t.Fatalf("tool.search 应命中 market.quote，实际: %v", search)
	}
	out, err := byName["tool.call"].Run(context.Background(), json.RawMessage(`{"name":"market.quote","args":{"codes":["600000","000001"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	call, ok := out.(CallResult)
	if !ok {
		t.Fatalf("tool.call 应返回 CallResult，实际 %T", out)
	}
	if !strings.Contains(string(call.Result), "600000") {
		t.Fatalf("tool.call 应把参数透传给目标工具，实际: %s", string(call.Result))
	}
	if len(host.calls) != 1 || host.calls[0] != "market.quote" {
		t.Fatalf("tool.call 必须经宿主执行目标工具，实际记录: %v", host.calls)
	}
	if _, err := byName["tool.describe"].Run(context.Background(), json.RawMessage(`{"name":"market.quote"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestAllToolSummariesWithinLimit(t *testing.T) {
	kb := NewKB(newMemStore())
	loader := NewSkillLoader(nil, t.TempDir())
	host := &fakeHost{tools: gatewayTools()}
	gw := NewGateway(host, nil, nil, plugin.RiskNetwork)

	groups := map[string][]plugin.Tool{
		"kb":    KBTools(kb),
		"skill": SkillTools(loader),
		"meta":  gw.MetaTools(),
	}
	for group, tools := range groups {
		if len(tools) == 0 {
			t.Fatalf("%s 工具组为空", group)
		}
		for _, tool := range tools {
			if err := tool.Validate(); err != nil {
				t.Fatalf("%s/%s 不合法: %v", group, tool.Name, err)
			}
			n := len([]rune(tool.Summary))
			if n == 0 {
				t.Fatalf("%s/%s 缺少 summary", group, tool.Name)
			}
			if n > SummaryMaxRunes {
				t.Fatalf("%s/%s 的 summary 有 %d 字，超过作者义务上限 %d 字：%q", group, tool.Name, n, SummaryMaxRunes, tool.Summary)
			}
			if len(tool.Tags) == 0 {
				t.Fatalf("%s/%s 缺少域标签", group, tool.Name)
			}
		}
	}
}

func TestMetaToolsExcludedFromIndex(t *testing.T) {
	host := &fakeHost{tools: gatewayTools()}
	gw := NewGateway(host, nil, nil, plugin.RiskNetwork)
	metas := gw.MetaTools()
	host.tools = append(host.tools, metas...)
	idx := gw.Catalog.Index()
	if strings.Contains(idx.Text, "- tool.call") || strings.Contains(idx.Text, "- tool.search") {
		t.Fatalf("元工具不应进 L1 索引（避免模型原地打转），实际: %s", idx.Text)
	}
	if !strings.Contains(idx.Text, "market.quote") {
		t.Fatalf("普通工具必须进索引，实际: %s", idx.Text)
	}
}
