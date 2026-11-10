// Package progressive 实现三级渐进式披露：
// L1 工具索引（名称+摘要+标签，按域分组、按预算裁剪）、L2 schema 展开、
// L3 经 kernel 执行；以及技能目录（skills）与中文 bigram 倒排 + TF-IDF 知识库（kb）。
package progressive

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// DefaultBudgetTokens 是 L1 索引默认的 token 预算。
const DefaultBudgetTokens = 1200

// SummaryMaxRunes 是 L1 摘要的最大字数（工具作者义务：≤40 字）。
const SummaryMaxRunes = 40

// EstimateTokens 用"rune/4"粗略估算 token 数：中文与英文混排下足够稳定，且不依赖分词器。
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (utf8.RuneCountInString(s) + 3) / 4
}

// TruncateRunes 按 rune 截断并追加省略号。
func TruncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit == 1 {
		return "…"
	}
	return string(r[:limit-1]) + "…"
}

// IndexEntry 是一条 L1 索引项。
type IndexEntry struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags,omitempty"`
	Risk    string   `json:"risk"`
	Domain  string   `json:"domain"`
}

// IndexResult 是 L1 索引生成结果（Text 直接喂给模型）。
type IndexResult struct {
	Text            string       `json:"text"`
	EstimatedTokens int          `json:"estimatedTokens"`
	BudgetTokens    int          `json:"budgetTokens"`
	Included        []IndexEntry `json:"included"`
	Dropped         []IndexEntry `json:"dropped"`
	Truncated       bool         `json:"truncated"`
	Hint            string       `json:"hint,omitempty"`
}

// DescribeResult 是 L2 展开结果：完整 JSON Schema + 示例。
type DescribeResult struct {
	Name     string          `json:"name"`
	Title    string          `json:"title"`
	Summary  string          `json:"summary"`
	Tags     []string        `json:"tags,omitempty"`
	Risk     string          `json:"risk"`
	Domain   string          `json:"domain"`
	Schema   json.RawMessage `json:"schema"`
	Examples []string        `json:"examples,omitempty"`
	Notes    string          `json:"notes,omitempty"`
}

// Catalog 是工具 L1 索引生成器。source 每次调用都取最新工具快照（kernel 启动后才齐）。
type Catalog struct {
	BudgetTokens int
	source       func() []plugin.Tool
}

// NewCatalog 构造工具索引器；source 为 nil 时索引为空。
func NewCatalog(budgetTokens int, source func() []plugin.Tool) *Catalog {
	if budgetTokens <= 0 {
		budgetTokens = DefaultBudgetTokens
	}
	return &Catalog{BudgetTokens: budgetTokens, source: source}
}

func (c *Catalog) snapshot() []plugin.Tool {
	if c == nil || c.source == nil {
		return nil
	}
	return c.source()
}

func riskRank(r plugin.RiskLevel) int {
	switch r {
	case plugin.RiskRead:
		return 0
	case plugin.RiskNetwork:
		return 1
	case plugin.RiskWrite:
		return 2
	case plugin.RiskExec:
		return 3
	}
	return 4
}

// domainOf 取工具的第一个标签作为域；没有标签时用工具名前缀（market.quote → market）。
func domainOf(t plugin.Tool) string {
	if len(t.Tags) > 0 {
		if d := strings.TrimSpace(t.Tags[0]); d != "" {
			return d
		}
	}
	if i := strings.Index(t.Name, "."); i > 0 {
		return t.Name[:i]
	}
	return "其它"
}

func entryOf(t plugin.Tool) IndexEntry {
	return IndexEntry{
		Name:    t.Name,
		Summary: TruncateRunes(strings.TrimSpace(t.Summary), SummaryMaxRunes),
		Tags:    append([]string{}, t.Tags...),
		Risk:    string(t.Risk),
		Domain:  domainOf(t),
	}
}

// Index 生成 L1 索引文本。裁剪策略：按"域相关性（域名字母序）+ 风险等级"排序，
// 风险低的（read）先入索引，write/exec 最后；超预算的工具进 Dropped 并给出 tool.search 提示。
func (c *Catalog) Index() IndexResult {
	tools := c.snapshot()
	result := IndexResult{BudgetTokens: c.BudgetTokens, Included: []IndexEntry{}, Dropped: []IndexEntry{}}
	if len(tools) == 0 {
		result.Text = "（暂无可用工具：插件可能尚未激活）"
		result.EstimatedTokens = EstimateTokens(result.Text)
		return result
	}

	entries := make([]IndexEntry, 0, len(tools))
	for _, t := range tools {
		entries = append(entries, entryOf(t))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		ri, rj := riskRank(plugin.RiskLevel(entries[i].Risk)), riskRank(plugin.RiskLevel(entries[j].Risk))
		if entries[i].Domain != entries[j].Domain {
			return entries[i].Domain < entries[j].Domain
		}
		if ri != rj {
			return ri < rj
		}
		return entries[i].Name < entries[j].Name
	})

	domains := make([]string, 0, 8)
	byDomain := map[string][]IndexEntry{}
	for _, e := range entries {
		if _, ok := byDomain[e.Domain]; !ok {
			domains = append(domains, e.Domain)
		}
		byDomain[e.Domain] = append(byDomain[e.Domain], e)
	}
	sort.Strings(domains)

	included := make([]IndexEntry, 0, len(entries))
	for _, d := range domains {
		for _, e := range byDomain[d] {
			included = append(included, e)
		}
	}

	hintFor := func(dropped int) string {
		if dropped == 0 {
			return ""
		}
		return fmt.Sprintf("> 预算内省略 %d 个工具：先用 tool.search 按关键词检索，再用 tool.describe 展开 schema，最后 tool.call 执行。", dropped)
	}

	// 超预算时从尾部（高风险/低优先级）开始丢弃，直到连同提示一起落进预算。
	var text string
	for {
		dropped := len(entries) - len(included)
		text = renderIndex(included, hintFor(dropped))
		result.EstimatedTokens = EstimateTokens(text)
		if result.EstimatedTokens <= c.BudgetTokens || len(included) == 0 {
			break
		}
		included = included[:len(included)-1]
	}
	droppedSet := map[string]bool{}
	for _, e := range included {
		droppedSet[e.Name] = true
	}
	for _, e := range entries {
		if !droppedSet[e.Name] {
			result.Dropped = append(result.Dropped, e)
		}
	}
	result.Included = included
	result.Text = text
	result.Hint = hintFor(len(result.Dropped))
	result.Truncated = len(result.Dropped) > 0
	return result
}

func renderIndex(included []IndexEntry, hint string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 工具索引（L1，共 %d 个在册；只含名称+摘要，schema 用 tool.describe 展开，执行用 tool.call）\n", len(included))
	cur := ""
	for _, e := range included {
		if e.Domain != cur {
			cur = e.Domain
			fmt.Fprintf(&b, "\n### %s\n", cur)
		}
		fmt.Fprintf(&b, "- %s [%s] %s\n", e.Name, e.Risk, e.Summary)
	}
	if hint != "" {
		fmt.Fprintf(&b, "\n%s\n", hint)
	}
	return b.String()
}

// Describe 展开某个工具的完整 JSON Schema 与示例（L2）。
func (c *Catalog) Describe(name string) (DescribeResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DescribeResult{}, fmt.Errorf("tool.describe 需要工具名")
	}
	for _, t := range c.snapshot() {
		if t.Name != name {
			continue
		}
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		pretty, err := prettyJSON(schema)
		if err != nil {
			return DescribeResult{}, fmt.Errorf("工具 %s 的 schema 不是合法 JSON：%w", name, err)
		}
		return DescribeResult{
			Name:     t.Name,
			Title:    t.Title,
			Summary:  t.Summary,
			Tags:     append([]string{}, t.Tags...),
			Risk:     string(t.Risk),
			Domain:   domainOf(t),
			Schema:   pretty,
			Examples: append([]string{}, t.Examples...),
			Notes:    "调用方式：tool.call {\"name\":\"" + t.Name + "\",\"args\":{…}}；args 必须匹配上面的 schema。",
		}, nil
	}
	return DescribeResult{}, fmt.Errorf("工具不存在：%s（请先用 tool.search 检索可用工具，不要猜测工具名）", name)
}

func prettyJSON(raw json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
