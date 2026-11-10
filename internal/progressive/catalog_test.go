package progressive

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/plugin"
)

func fakeTools(n int) []plugin.Tool {
	domains := []string{"market", "flow", "watch", "agent", "kb", "quant"}
	out := make([]plugin.Tool, 0, n)
	for i := 0; i < n; i++ {
		d := domains[i%len(domains)]
		risk := plugin.RiskRead
		if i%4 == 3 {
			risk = plugin.RiskWrite
		}
		out = append(out, plugin.Tool{
			Name:     d + ".tool" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Summary:  "用于测试的摘要：说明什么时候该用这个工具，长度需要被截断到四十字以内否则索引会膨胀导致预算浪费",
			Tags:     []string{d},
			Risk:     risk,
			Schema:   json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}},"required":["code"]}`),
			Examples: []string{`{"code":"600000"}`},
			Run:      func(ctx context.Context, args json.RawMessage) (any, error) { return "ok", nil },
		})
	}
	return out
}

func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},
		{"abcde", 2},
		{"中文摘要", 1},
	}
	for _, c := range cases {
		if got := EstimateTokens(c.in); got != c.want {
			t.Fatalf("EstimateTokens(%q)=%d, want %d", c.in, got, c.want)
		}
	}
}

func TestIndexRespectsBudget(t *testing.T) {
	tools := fakeTools(60)
	c := NewCatalog(120, func() []plugin.Tool { return tools })
	res := c.Index()
	if res.EstimatedTokens > 120 {
		t.Fatalf("L1 索引超预算: %d > 120", res.EstimatedTokens)
	}
	if len(res.Included) == 0 {
		t.Fatal("预算内至少应包含一个工具")
	}
	if len(res.Dropped) == 0 {
		t.Fatal("60 个工具在 120 token 预算下必须裁剪")
	}
	if !res.Truncated {
		t.Fatal("Truncated 应为 true")
	}
	if !strings.Contains(res.Hint, "tool.search") {
		t.Fatalf("裁剪后必须提示 tool.search，实际: %q", res.Hint)
	}
	if !strings.Contains(res.Text, res.Hint) {
		t.Fatal("提示必须写进给模型看的索引文本")
	}
	for _, e := range res.Included {
		if len([]rune(e.Summary)) > SummaryMaxRunes+1 {
			t.Fatalf("摘要必须截断到 %d 字，实际 %d: %q", SummaryMaxRunes, len([]rune(e.Summary)), e.Summary)
		}
	}
}

func TestIndexGroupsByDomainAndRisk(t *testing.T) {
	tools := []plugin.Tool{
		{Name: "flow.rank", Summary: "板块资金流排名", Tags: []string{"flow"}, Risk: plugin.RiskNetwork},
		{Name: "market.quote", Summary: "批量个股快照", Tags: []string{"market"}, Risk: plugin.RiskNetwork},
		{Name: "market.bars", Summary: "K线序列", Tags: []string{"market"}, Risk: plugin.RiskRead},
		{Name: "watch.add", Summary: "写入自选", Tags: []string{"watch"}, Risk: plugin.RiskWrite},
	}
	c := NewCatalog(2000, func() []plugin.Tool { return tools })
	res := c.Index()
	for _, domain := range []string{"### flow", "### market", "### watch"} {
		if !strings.Contains(res.Text, domain) {
			t.Fatalf("索引必须按域分组，缺少 %q；当前索引=%s", domain, res.Text)
		}
	}
	// 同域内 read 必须排在 network 之前。
	iBars := strings.Index(res.Text, "market.bars")
	iQuote := strings.Index(res.Text, "market.quote")
	if iBars < 0 || iQuote < 0 || iBars > iQuote {
		t.Fatalf("同域内 read 风险工具应排在 network 之前；当前索引=%s", res.Text)
	}
}

func TestDescribeExpandsSchema(t *testing.T) {
	tools := fakeTools(3)
	c := NewCatalog(500, func() []plugin.Tool { return tools })
	res, err := c.Describe(tools[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != tools[0].Name || res.Risk != string(plugin.RiskRead) {
		t.Fatalf("describe 元信息不对: %+v", res)
	}
	if !strings.Contains(string(res.Schema), "properties") || !strings.Contains(string(res.Schema), "\n") {
		t.Fatalf("schema 必须是展开(缩进)后的 JSON: %s", string(res.Schema))
	}
	if len(res.Examples) == 0 {
		t.Fatal("describe 必须带示例")
	}
	if _, err := c.Describe("nope.tool"); err == nil || !strings.Contains(err.Error(), "tool.search") {
		t.Fatalf("未知工具应报错并提示 tool.search，实际: %v", err)
	}
	if _, err := c.Describe(""); err == nil {
		t.Fatal("空工具名必须报错")
	}
}
