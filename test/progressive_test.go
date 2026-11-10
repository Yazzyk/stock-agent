package test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/internal/progressive"
	"github.com/yazzyk/stock-agent/internal/security"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- L1 工具索引：token 预算与裁剪 ----------

func catalogTools(readN, netN, writeN int) []plugin.Tool {
	name := func(kind string, i int) string { return fmt.Sprintf("market.%s%02d", kind, i) }
	run := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	var out []plugin.Tool
	for i := 1; i <= readN; i++ {
		out = append(out, plugin.Tool{Name: name("r", i), Summary: "取个股行情快照与涨跌幅", Tags: []string{"market"}, Risk: plugin.RiskRead, Run: run})
	}
	for i := 1; i <= netN; i++ {
		out = append(out, plugin.Tool{Name: name("n", i), Summary: "取个股行情快照与涨跌幅", Tags: []string{"market"}, Risk: plugin.RiskNetwork, Run: run})
	}
	for i := 1; i <= writeN; i++ {
		out = append(out, plugin.Tool{Name: name("w", i), Summary: "写入本地自选分组配置", Tags: []string{"market"}, Risk: plugin.RiskWrite, Run: run})
	}
	return out
}

// 超预算时必须裁剪：低风险优先保留，剩余进 Dropped 并给出检索提示。
func TestCatalogIndexBudget(t *testing.T) {
	tools := catalogTools(4, 4, 4)
	const budget = 100
	cat := progressive.NewCatalog(budget, func() []plugin.Tool { return tools })
	res := cat.Index()

	if res.BudgetTokens != budget {
		t.Fatalf("预算应为 %d，实际 %d", budget, res.BudgetTokens)
	}
	if res.EstimatedTokens > budget {
		t.Fatalf("索引超出 token 预算：%d > %d\n%s", res.EstimatedTokens, budget, res.Text)
	}
	if len(res.Included)+len(res.Dropped) != len(tools) {
		t.Fatalf("收录+丢弃应等于工具总数：%d+%d != %d", len(res.Included), len(res.Dropped), len(tools))
	}
	if !res.Truncated {
		t.Fatal("预算不足时应标记 Truncated")
	}
	if len(res.Dropped) == 0 {
		t.Fatal("预算不足时应有工具被丢弃")
	}
	if res.Hint == "" || !strings.Contains(res.Hint, "tool.search") {
		t.Fatalf("裁剪后应提示用 tool.search 检索，实际: %q", res.Hint)
	}
	// 低风险优先：被收录的都不应是 write（排序时 write 排最后）
	for _, e := range res.Included {
		if e.Risk == string(plugin.RiskWrite) {
			t.Fatalf("预算紧张时应优先保留低风险工具，却收录了 %s[%s]", e.Name, e.Risk)
		}
	}
	// 文本里出现的工具必须在 Included 内（不能出现"文本有但没记进 Included"）
	for _, e := range res.Dropped {
		if strings.Contains(res.Text, "- "+e.Name+" ") {
			t.Fatalf("被丢弃的工具 %s 仍出现在索引文本里", e.Name)
		}
	}
}

// 预算充足时不裁剪，文本包含名称、风险等级与域名分组。
func TestCatalogIndexNoTruncation(t *testing.T) {
	tools := catalogTools(2, 1, 0)
	cat := progressive.NewCatalog(0, func() []plugin.Tool { return tools })
	res := cat.Index()

	if res.BudgetTokens != progressive.DefaultBudgetTokens {
		t.Fatalf("预算缺省应为 %d，实际 %d", progressive.DefaultBudgetTokens, res.BudgetTokens)
	}
	if res.Truncated || len(res.Dropped) != 0 {
		t.Fatalf("预算充足不应裁剪: truncated=%v dropped=%d", res.Truncated, len(res.Dropped))
	}
	if len(res.Included) != len(tools) {
		t.Fatalf("应全部收录：%d != %d", len(res.Included), len(tools))
	}
	for _, want := range []string{"market.r01", "[read]", "[network]", "### market"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("索引文本缺少 %q:\n%s", want, res.Text)
		}
	}
	if res.EstimatedTokens <= 0 {
		t.Fatal("应给出 token 估算")
	}
}

// summary 超 40 字必须截断（L1 只放得下一行）。
func TestCatalogSummaryTruncated(t *testing.T) {
	long := strings.Repeat("很长的工具摘要", 20)
	cat := progressive.NewCatalog(0, func() []plugin.Tool {
		return []plugin.Tool{{Name: "x.long", Summary: long, Tags: []string{"x"}, Risk: plugin.RiskRead}}
	})
	res := cat.Index()
	if len(res.Included) != 1 {
		t.Fatalf("应收录 1 个工具，实际 %d", len(res.Included))
	}
	got := res.Included[0].Summary
	runes := []rune(got)
	if len(runes) > progressive.SummaryMaxRunes+1 {
		t.Fatalf("summary 应截断到 %d 字（含省略号），实际 %d 字: %q", progressive.SummaryMaxRunes, len(runes), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断应带省略号，实际: %q", got)
	}
}

// 没有工具时给出可读提示，而不是空串。
func TestCatalogIndexEmpty(t *testing.T) {
	res := progressive.NewCatalog(0, nil).Index()
	if !strings.Contains(res.Text, "暂无可用工具") {
		t.Fatalf("空索引应给出提示，实际: %q", res.Text)
	}
	if len(res.Included) != 0 {
		t.Fatalf("空索引不应有 Included，实际 %d", len(res.Included))
	}
}

// ---------- L2 describe 展开 ----------

func TestCatalogDescribe(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"codes":{"type":"array","items":{"type":"string"}}},"required":["codes"]}`)
	tool := plugin.Tool{
		Name: "market.quote", Title: "行情快照", Summary: "取个股实时快照",
		Tags: []string{"market"}, Risk: plugin.RiskNetwork, Schema: schema,
		Examples: []string{`{"codes":["600000"]}`},
		Run:      func(context.Context, json.RawMessage) (any, error) { return nil, nil },
	}
	noSchema := plugin.Tool{Name: "watch.list", Summary: "列出自选", Tags: []string{"watch"}, Risk: plugin.RiskRead,
		Run: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}
	badSchema := plugin.Tool{Name: "bad.tool", Summary: "非法 schema", Tags: []string{"x"}, Risk: plugin.RiskRead,
		Schema: json.RawMessage("{不是 JSON"), Run: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}

	cat := progressive.NewCatalog(0, func() []plugin.Tool { return []plugin.Tool{tool, noSchema, badSchema} })

	t.Run("展开 schema", func(t *testing.T) {
		res, err := cat.Describe("market.quote")
		if err != nil {
			t.Fatalf("Describe 失败: %v", err)
		}
		if res.Risk != string(plugin.RiskNetwork) || res.Domain != "market" {
			t.Fatalf("风险/域不符: %+v", res)
		}
		for _, want := range []string{"codes", "required", "properties"} {
			if !strings.Contains(string(res.Schema), want) {
				t.Fatalf("展开的 schema 缺少 %q: %s", want, res.Schema)
			}
		}
		if !strings.Contains(res.Notes, "tool.call") {
			t.Fatalf("Notes 应告诉模型怎么调，实际: %q", res.Notes)
		}
		if len(res.Examples) != 1 {
			t.Fatalf("示例应原样带出，实际 %d 条", len(res.Examples))
		}
	})

	t.Run("无 schema 给空对象", func(t *testing.T) {
		res, err := cat.Describe("watch.list")
		if err != nil {
			t.Fatalf("Describe 失败: %v", err)
		}
		if !strings.Contains(string(res.Schema), "object") {
			t.Fatalf("无 schema 时应给 object 兜底，实际: %s", res.Schema)
		}
	})

	t.Run("非法 schema 报错", func(t *testing.T) {
		if _, err := cat.Describe("bad.tool"); err == nil || !strings.Contains(err.Error(), "合法 JSON") {
			t.Fatalf("非法 schema 应报错并说明原因，实际: %v", err)
		}
	})

	t.Run("未知工具报错", func(t *testing.T) {
		_, err := cat.Describe("nope.tool")
		if err == nil || !strings.Contains(err.Error(), "工具不存在") {
			t.Fatalf("未知工具应报错，实际: %v", err)
		}
		if !strings.Contains(err.Error(), "tool.search") {
			t.Fatalf("错误应引导先用 tool.search，实际: %v", err)
		}
	})

	t.Run("空名称报错", func(t *testing.T) {
		if _, err := cat.Describe("  "); err == nil {
			t.Fatal("空名称应报错")
		}
	})
}

// ---------- 技能：加载、路径越界 ----------

func writeSkillDir(t *testing.T, root, name, desc, risk, body string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建技能目录失败: %v", err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\nrisk: " + risk + "\nversion: 1.0.0\ntags: [test]\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, progressive.SkillFileName), []byte(md), 0o644); err != nil {
		t.Fatalf("写 SKILL.md 失败: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("创建引用目录失败: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("写引用文件失败: %v", err)
		}
	}
	return dir
}

func TestSkillLoaderAndTraversal(t *testing.T) {
	root := t.TempDir()
	alpha := writeSkillDir(t, root, "alpha", "盘前计划：生成当日交易计划与触发条件", "read",
		"# Alpha\nALPHA_BODY 正文第一段。\n", map[string]string{
			"references/checklist.md": "# 清单\n- 检查项 A\n",
		})
	writeSkillDir(t, root, "beta", "盘后复盘：对照计划与实际并写回记忆", "write",
		"# Beta\nBETA_BODY 正文。\n", nil)

	guard := security.NewGuard(allowlistPolicy(root), []string{root})
	loader := progressive.NewSkillLoader(guard, root)

	list, err := loader.List()
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "beta" {
		t.Fatalf("技能应按名称排序: %+v", list)
	}
	if list[0].Source != "content" {
		t.Fatalf("仓库技能 Source 应为 content，实际 %q", list[0].Source)
	}
	if list[1].Risk != "write" {
		t.Fatalf("风险等级应来自 frontmatter，实际 %q", list[1].Risk)
	}

	view, err := loader.View("alpha")
	if err != nil {
		t.Fatalf("View 失败: %v", err)
	}
	if !strings.Contains(view.Body, "ALPHA_BODY") {
		t.Fatalf("View 应返回正文，实际: %q", view.Body)
	}
	if _, err := loader.View("gamma"); err == nil || !strings.Contains(err.Error(), "技能不存在") {
		t.Fatalf("未知技能应报错，实际: %v", err)
	}

	// 正常读取技能目录内的引用文件
	buf, err := loader.Read("alpha", "references/checklist.md")
	if err != nil {
		t.Fatalf("技能内正常文件应可读: %v", err)
	}
	if !strings.Contains(string(buf), "检查项 A") {
		t.Fatalf("读取内容不符: %q", string(buf))
	}

	// 越界一律拒绝
	denied := []struct {
		name string
		rel  string
		want string
	}{
		{"同级技能文件", "../beta/SKILL.md", "穿越"},
		{"多级穿越", "references/../../x.md", "穿越"},
		{"绝对路径", "/etc/passwd", "绝对路径"},
		{"空路径", "   ", "相对路径"},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loader.Read("alpha", tc.rel); err == nil {
				t.Fatalf("越界读取应被拒绝: %q", tc.rel)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("拒绝理由应含 %q，实际: %v", tc.want, err)
			}
		})
	}

	// 软链接逃逸：技能目录内的链接指向目录外，必须按真实路径拒绝
	t.Run("软链接逃逸", func(t *testing.T) {
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("SECRET"), 0o644); err != nil {
			t.Fatalf("准备外部文件失败: %v", err)
		}
		link := filepath.Join(alpha, "references", "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("当前文件系统不支持软链接，跳过: %v", err)
		}
		if _, err := loader.Read("alpha", "references/escape/secret.md"); err == nil {
			t.Fatal("经软链接读到技能目录外文件应被拒绝")
		}
	})

	// 缺失文件给出明确错误而不是静默空
	if _, err := loader.Read("alpha", "references/missing.md"); err == nil || !strings.Contains(err.Error(), "不存在文件") {
		t.Fatalf("缺失文件应明确报错，实际: %v", err)
	}

	// 技能目录 L1：默认预算下两个技能都在，description 截断到 60 字
	cat := progressive.NewSkillCatalog(loader, 0)
	idx := cat.Index()
	if idx.BudgetTokens != 400 {
		t.Fatalf("技能目录默认预算应 400，实际 %d", idx.BudgetTokens)
	}
	for _, want := range []string{"alpha", "beta", "[read]", "[write]"} {
		if !strings.Contains(idx.Text, want) {
			t.Fatalf("技能目录缺少 %q:\n%s", want, idx.Text)
		}
	}
	for _, e := range idx.Included {
		if n := len([]rune(e.Summary)); n > progressive.SkillDescMaxRunes+1 {
			t.Fatalf("description 应截断到 %d 字，%s 实际 %d 字", progressive.SkillDescMaxRunes, e.Name, n)
		}
	}
}

// ---------- 知识库：top1 命中与分数稳定性 ----------

func TestKBSearchTopHitAndStability(t *testing.T) {
	kb := progressive.NewKB(newFakeStore())
	ctx := context.Background()

	funds, err := kb.Import(ctx, progressive.ImportInput{
		Title: "主力资金流判读",
		Content: "# 主力资金流判读\n\n主力净流入由超大单与大单的主动成交构成。" +
			"\n\n## 分档口径\n超大单单笔成交额最大，其次是大单、中单、小单；分档看的是单笔金额而不是账户。" +
			"\n\n## 用法\n连续三日主力净流入且股价横盘，说明有资金在吸筹。",
		Tags:   []string{"fundflow"},
		Source: "content/kb/fundflow.md",
	})
	if err != nil {
		t.Fatalf("导入资金流文档失败: %v", err)
	}
	if funds.ID == "" || funds.Chunks == 0 {
		t.Fatalf("导入结果不完整: %+v", funds)
	}
	if _, err := kb.Import(ctx, progressive.ImportInput{
		Title:   "K线形态速查",
		Content: "# K线形态速查\n\n阳线表示收盘高于开盘，阴线相反，十字星说明多空平衡。\n\n长上影线代表上方压力。",
		Tags:    []string{"kline"},
	}); err != nil {
		t.Fatalf("导入 K 线文档失败: %v", err)
	}
	if _, err := kb.Import(ctx, progressive.ImportInput{
		Title:   "涨停板复盘法",
		Content: "# 涨停板复盘法\n\n涨停后次日要看分歧与一致：高开抢筹是强势，低开走弱要减仓。\n\n封单额决定承接强度。",
		Tags:    []string{"limitup"},
	}); err != nil {
		t.Fatalf("导入涨停文档失败: %v", err)
	}

	res, err := kb.Search(ctx, "主力资金", 5)
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatalf("应命中资金流文档，实际 0 条（query=%q）", res.Query)
	}
	top := res.Hits[0]
	if top.DocID != funds.ID {
		t.Fatalf("top1 应命中《主力资金流判读》，实际命中 %q(%s)", top.Title, top.DocID)
	}
	if top.Score <= 0 {
		t.Fatalf("top1 分数应大于 0，实际 %v", top.Score)
	}
	if top.Snippet == "" {
		t.Fatal("命中应带片段")
	}

	// 分数与排序必须可复现（同一 query 两次检索逐位相同）
	again, err := kb.Search(ctx, "主力资金", 5)
	if err != nil {
		t.Fatalf("二次检索失败: %v", err)
	}
	if len(again.Hits) != len(res.Hits) {
		t.Fatalf("两次检索命中数不一致: %d vs %d", len(res.Hits), len(again.Hits))
	}
	for i := range res.Hits {
		if res.Hits[i].ID != again.Hits[i].ID || res.Hits[i].Score != again.Hits[i].Score {
			t.Fatalf("第 %d 条命中不稳定: %s/%v vs %s/%v",
				i, res.Hits[i].ID, res.Hits[i].Score, again.Hits[i].ID, again.Hits[i].Score)
		}
	}

	opened, err := kb.Open(ctx, top.ID)
	if err != nil {
		t.Fatalf("打开命中块失败: %v", err)
	}
	if !strings.Contains(opened.Content, "超大单") {
		t.Fatalf("打开后应能读到原文，实际前 80 字: %.80s", opened.Content)
	}

	stats, err := kb.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if stats.Docs != 3 {
		t.Fatalf("文档数应为 3，实际 %d", stats.Docs)
	}
	list, err := kb.List(ctx, 10)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if list.Total != 3 || len(list.Docs) != 3 {
		t.Fatalf("文档列表应为 3 条，实际 total=%d len=%d", list.Total, len(list.Docs))
	}

	// 未命中：不报错，但要给出改写建议
	miss, err := kb.Search(ctx, "量子计算", 5)
	if err != nil {
		t.Fatalf("未命中不应报错: %v", err)
	}
	if miss.Total != 0 || len(miss.Hits) != 0 {
		t.Fatalf("无关查询不应命中: total=%d hits=%d", miss.Total, len(miss.Hits))
	}
	if miss.Hint == "" {
		t.Fatal("未命中应给出改写提示")
	}

	// 空 query：不报错，但要给出可操作的提示（模型常传 {} 进来）
	empty, err := kb.Search(ctx, "   ", 5)
	if err != nil {
		t.Fatalf("空 query 不应报错（应给提示）: %v", err)
	}
	if len(empty.Hits) != 0 || empty.Hint == "" {
		t.Fatalf("空 query 应返回空列表 + 提示，实际: hits=%d hint=%q", len(empty.Hits), empty.Hint)
	}
	// 空内容导入报错
	if _, err := kb.Import(ctx, progressive.ImportInput{Title: "空文档"}); err == nil {
		t.Fatal("空内容导入应报错")
	}
}

// 空知识库：检索不 panic，返回"知识库为空"的提示。
func TestKBEmptySearchHint(t *testing.T) {
	kb := progressive.NewKB(newFakeStore())
	res, err := kb.Search(context.Background(), "涨停", 5)
	if err != nil {
		t.Fatalf("空库检索不应报错: %v", err)
	}
	if res.Total != 0 || len(res.Hits) != 0 {
		t.Fatalf("空库不应有命中: %+v", res)
	}
	if !strings.Contains(res.Hint, "知识库为空") {
		t.Fatalf("空库应提示如何导入，实际: %q", res.Hint)
	}
}

// ---------- 网关：未知工具、风险上限、元工具 ----------

func TestGatewayCallAndRisk(t *testing.T) {
	f := newKernel(t)
	run := func(context.Context, json.RawMessage) (any, error) { return map[string]string{"ok": "1"}, nil }
	a := withTools(newPlugin("market"),
		plugin.Tool{
			Name: "market.quote", Title: "行情", Summary: "取个股实时快照",
			Tags: []string{"market"}, Risk: plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{"codes":{"type":"array"}}}`),
			Run:    run,
		},
	)
	b := withTools(newPlugin("watch"),
		plugin.Tool{Name: "watch.add", Summary: "加自选", Tags: []string{"watch"}, Risk: plugin.RiskWrite, Run: run},
	)
	c := withTools(newPlugin("danger"),
		plugin.Tool{Name: "danger.exec", Summary: "执行外部命令", Tags: []string{"danger"}, Risk: plugin.RiskExec, Run: run},
	)
	f.register(t, a, b, c)
	f.start(t)

	gw := progressive.NewGateway(f.k, f.guard, f.audit, plugin.RiskNetwork)
	ctx := context.Background()

	t.Run("正常执行", func(t *testing.T) {
		res, err := gw.Call(ctx, "market.quote", json.RawMessage(`{"codes":["600000"]}`))
		if err != nil {
			t.Fatalf("Call 失败: %v", err)
		}
		if res.Name != "market.quote" || res.Risk != string(plugin.RiskNetwork) {
			t.Fatalf("返回元信息不符: %+v", res)
		}
		if !strings.Contains(string(res.Result), "ok") {
			t.Fatalf("结果应透传执行输出，实际: %s", res.Result)
		}
		if e := f.audit.lastOf(t, "gateway", "market.quote"); !e.OK {
			t.Fatal("网关执行成功应落 OK=true 审计")
		}
	})

	t.Run("未知工具", func(t *testing.T) {
		_, err := gw.Call(ctx, "nope.tool", nil)
		if err == nil || !strings.Contains(err.Error(), "未注册") {
			t.Fatalf("未知工具应报错，实际: %v", err)
		}
		if e := f.audit.lastOf(t, "gateway", "nope.tool"); e.OK {
			t.Fatal("未知工具应落 OK=false 审计")
		}
	})

	t.Run("超过风险上限", func(t *testing.T) {
		for _, name := range []string{"watch.add", "danger.exec"} {
			if _, err := gw.Call(ctx, name, nil); err == nil || !strings.Contains(err.Error(), "超出") {
				t.Fatalf("%s（风险高于 network）应被网关拒绝，实际: %v", name, err)
			}
		}
	})

	t.Run("空名称", func(t *testing.T) {
		if _, err := gw.Call(ctx, "  ", nil); err == nil {
			t.Fatal("空工具名应报错")
		}
	})

	t.Run("describe", func(t *testing.T) {
		res, err := gw.Describe("market.quote")
		if err != nil {
			t.Fatalf("Describe 失败: %v", err)
		}
		if !strings.Contains(string(res.Schema), "codes") {
			t.Fatalf("schema 应带 codes，实际: %s", res.Schema)
		}
		if _, err := gw.Describe("nope"); err == nil {
			t.Fatal("未知工具 describe 应报错")
		}
	})

	t.Run("search", func(t *testing.T) {
		res, err := gw.Search("快照", 5)
		if err != nil {
			t.Fatalf("Search 失败: %v", err)
		}
		if len(res.Hits) == 0 || res.Hits[0].Name != "market.quote" {
			t.Fatalf("应命中 market.quote，实际: %+v", res.Hits)
		}
		if _, err := gw.Search("  ", 5); err == nil {
			t.Fatal("空 query 应报错")
		}
	})

	t.Run("元工具", func(t *testing.T) {
		metas := gw.MetaTools()
		if len(metas) != 3 {
			t.Fatalf("应有 3 个元工具，实际 %d", len(metas))
		}
		want := map[string]bool{"tool.search": true, "tool.describe": true, "tool.call": true}
		for _, m := range metas {
			if !want[m.Name] {
				t.Fatalf("意外的元工具名: %s", m.Name)
			}
			if m.Risk != plugin.RiskRead {
				t.Fatalf("元工具应为 read 风险（不直接触网），%s 实际 %s", m.Name, m.Risk)
			}
			if m.Run == nil {
				t.Fatalf("元工具 %s 缺少 Run", m.Name)
			}
			if len(m.Schema) == 0 {
				t.Fatalf("元工具 %s 缺少 schema", m.Name)
			}
		}

		// 通过 tool.call 调用未知工具：错误必须显式，而不是返回空结果
		var callTool plugin.Tool
		for _, m := range metas {
			if m.Name == "tool.call" {
				callTool = m
			}
		}
		_, err := callTool.Run(ctx, json.RawMessage(`{"name":"nope.tool","args":{}}`))
		if err == nil || !strings.Contains(err.Error(), "未注册") {
			t.Fatalf("tool.call 未知工具应报错，实际: %v", err)
		}
	})
}
