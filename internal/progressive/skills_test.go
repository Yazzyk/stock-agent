package progressive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, root, slug, frontmatter, body string) string {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---" + "\n" + frontmatter + "\n" + "---" + "\n" + body
	if err := os.WriteFile(filepath.Join(dir, SkillFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSkillLoadAndUserOverride(t *testing.T) {
	contentRoot := t.TempDir()
	userRoot := t.TempDir()
	writeSkillFile(t, contentRoot, "premarket-plan",
		"name: premarket-plan\ndescription: 盘前计划三件套\ntags: [plan, market]\nrisk: read\nversion: 0.1.0",
		"# 盘前计划\n1. 看消息面")
	writeSkillFile(t, contentRoot, "postmarket-review",
		"name: postmarket-review\ndescription: 收盘复盘流程",
		"# 复盘\n1. 数涨停家数")
	writeSkillFile(t, userRoot, "premarket-plan",
		"name: premarket-plan\ndescription: 用户自定义盘前计划\nrisk: network",
		"# 我的盘前\n1. 先看持仓")

	l := NewSkillLoader(nil, contentRoot, userRoot)
	skills, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("同名技能应合并，共 2 个，实际 %d: %+v", len(skills), skills)
	}
	if skills[0].Name != "postmarket-review" || skills[1].Name != "premarket-plan" {
		t.Fatalf("技能应按名称排序，实际: %s,%s", skills[0].Name, skills[1].Name)
	}
	plan := skills[1]
	if !strings.Contains(plan.Description, "用户自定义") || plan.Risk != "network" {
		t.Fatalf("用户版元信息未生效: %+v", plan)
	}
	if !strings.Contains(plan.Body, "先看持仓") {
		t.Fatalf("用户版正文未生效: %q", plan.Body)
	}
}

func TestSkillFrontmatterVariants(t *testing.T) {
	root := t.TempDir()
	// 块状列表 + 无 description + 未知字段 + 引号包裹。
	writeSkillFile(t, root, "quant-lab",
		"name: \"quant-lab\"\ntags:\n  - quant\n  - backtest\nunknownKey: whatever",
		"# 量化实验")
	writeSkillFile(t, root, "no-name",
		"description: 没有 name 时用目录名\nrisk: write",
		"# 无名")

	l := NewSkillLoader(nil, root)
	skills, err := l.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("应加载 2 个技能，实际 %d", len(skills))
	}
	if skills[0].Name != "no-name" || skills[0].Description == "" {
		t.Fatalf("缺 name 时应用目录名，缺 description 时用名称兜底: %+v", skills[0])
	}
	if skills[1].Name != "quant-lab" {
		t.Fatalf("引号应被去掉，实际 %q", skills[1].Name)
	}
	if len(skills[1].Tags) != 2 || skills[1].Tags[0] != "quant" {
		t.Fatalf("块状 tags 应被解析，实际 %+v", skills[1].Tags)
	}
	if len(skills[1].Resources) != 0 {
		t.Fatalf("只有 SKILL.md 时资源列表应为空，实际 %+v", skills[1].Resources)
	}
}

func TestSkillViewAndReadContainment(t *testing.T) {
	root := t.TempDir()
	dir := writeSkillFile(t, root, "premarket-plan",
		"name: premarket-plan\ndescription: 盘前计划\nrisk: read",
		"# 盘前计划\n1. 看消息面")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "checklist.md"), []byte("清单正文"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 技能目录之外的秘密文件 + 指向它的软链。
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("机密"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "leak.md")); err != nil {
		t.Fatal(err)
	}

	l := NewSkillLoader(nil, root)
	l.List()

	s, err := l.View("premarket-plan")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Body, "看消息面") {
		t.Fatalf("skill.view 必须返回正文，实际: %q", s.Body)
	}
	if _, err := l.View("nope"); err == nil || !strings.Contains(err.Error(), "premarket-plan") {
		t.Fatalf("未知技能应报错并给出可用列表，实际: %v", err)
	}

	buf, err := l.Read("premarket-plan", "references/checklist.md")
	if err != nil {
		t.Fatalf("目录内相对路径应允许: %v", err)
	}
	if string(buf) != "清单正文" {
		t.Fatalf("读取内容不对: %q", string(buf))
	}

	denied := []struct{ path, want string }{
		{"../secret.md", "穿越"},
		{outside, "绝对路径"},
		{"leak.md", "技能目录之外"},
		{"references/missing.md", "不存在"},
		{"", "相对路径"},
	}
	for _, c := range denied {
		_, err := l.Read("premarket-plan", c.path)
		if err == nil {
			t.Fatalf("skill.read(%q) 必须被拒绝", c.path)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("skill.read(%q) 错误信息应包含 %q，实际: %v", c.path, c.want, err)
		}
	}
}

func TestSkillCatalogBudgetAndTruncation(t *testing.T) {
	root := t.TempDir()
	longDesc := strings.Repeat("很长的描述", 20) // 100 字，超过 60 字上限
	for _, name := range []string{"a-one", "b-two", "c-three", "d-four"} {
		writeSkillFile(t, root, name, "name: "+name+"\ndescription: "+longDesc+"\nrisk: read", "# 正文")
	}
	l := NewSkillLoader(nil, root)
	cat := NewSkillCatalog(l, 40)
	res := cat.Index()
	if res.EstimatedTokens > 40 {
		t.Fatalf("技能目录超预算: %d > 40", res.EstimatedTokens)
	}
	if len(res.Included) == 0 {
		t.Fatal("预算内至少应包含一个技能")
	}
	for _, e := range res.Included {
		if len([]rune(e.Summary)) > SkillDescMaxRunes {
			t.Fatalf("L1 描述必须截断到 %d 字，实际 %d", SkillDescMaxRunes, len([]rune(e.Summary)))
		}
	}
	if !res.Truncated || !strings.Contains(res.Hint, "skill.list") {
		t.Fatalf("超预算应给出 skill.list 提示，实际 truncated=%v hint=%q", res.Truncated, res.Hint)
	}

	big := NewSkillCatalog(l, 4000)
	full := big.Index()
	if full.Truncated {
		t.Fatal("预算充足时不应裁剪")
	}
	for _, e := range full.Included {
		if !strings.HasSuffix(e.Summary, "…") {
			t.Fatalf("超长描述应带省略号，实际 %q", e.Summary)
		}
	}
}

func TestSkillToolsEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "premarket-plan",
		"name: premarket-plan\ndescription: 盘前计划\nrisk: read",
		"# 盘前\n1. 看消息")
	l := NewSkillLoader(nil, root)
	tools := SkillTools(l)
	if len(tools) != 3 {
		t.Fatalf("应有 3 个技能工具，实际 %d", len(tools))
	}
	for _, tool := range tools {
		if err := tool.Validate(); err != nil {
			t.Fatalf("%s 不合法: %v", tool.Name, err)
		}
	}
	list, err := tools[0].Run(contextBackground(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sprint(list), "premarket-plan") {
		t.Fatalf("skill.list 应列出技能，实际: %v", list)
	}
}
