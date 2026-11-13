package progressive

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// Gateway 是三级披露的 L3 落点：tool.search / tool.describe / tool.call 三个元工具。
// 执行一律经由 plugin.ToolHost（kernel 注入），风险等级与审计在这里强制。
// Host 为 nil 时返回可操作的中文错误，不 panic。
type Gateway struct {
	Host    plugin.ToolHost
	Guard   contract.Guard
	Audit   contract.Audit
	MaxRisk plugin.RiskLevel
	Catalog *Catalog

	// OnCall 在每次真正执行工具前回调（agent 用来发 agent.state 事件）。
	OnCall func(name string)
	// Now 便于测试注入时钟。
	Now func() time.Time
}

// NewGateway 构造网关。Host 必须是 kernel（或任何实现 plugin.ToolHost 的宿主）。
func NewGateway(host plugin.ToolHost, guard contract.Guard, audit contract.Audit, maxRisk plugin.RiskLevel) *Gateway {
	if maxRisk == "" {
		maxRisk = plugin.RiskNetwork
	}
	g := &Gateway{
		Host:    host,
		Guard:   guard,
		Audit:   audit,
		MaxRisk: maxRisk,
		Now:     time.Now,
	}
	g.Catalog = NewCatalog(DefaultBudgetTokens, g.toolList)
	return g
}

func (g *Gateway) toolList() []plugin.Tool {
	if g == nil || g.Host == nil {
		return nil
	}
	tools := g.Host.Tools()
	out := make([]plugin.Tool, 0, len(tools))
	for _, t := range tools {
		// 元工具自身不进索引，避免模型反复检索 tool.search 自己。
		if strings.HasPrefix(t.Name, "tool.") && t.Name != "tool.call" {
			continue
		}
		if t.Name == "tool.call" {
			continue
		}
		out = append(out, t)
	}
	return out
}

func (g *Gateway) redact(s string) string {
	if g.Guard == nil {
		return s
	}
	return g.Guard.Redact(s)
}

// SearchResult 是 tool.search 的返回。
type SearchResult struct {
	Query string      `json:"query"`
	Total int         `json:"total"`
	Hits  []SearchHit `json:"hits"`
	Hint  string      `json:"hint,omitempty"`
}

// SearchHit 是一条检索命中。
type SearchHit struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags,omitempty"`
	Risk    string   `json:"risk"`
	Score   int      `json:"score"`
}

// CallResult 是 tool.call 的返回。
type CallResult struct {
	Name      string          `json:"name"`
	Risk      string          `json:"risk"`
	ElapsedMs int64           `json:"elapsedMs"`
	Result    json.RawMessage `json:"result"`
}

// ToolIndexEntry 是 L1 工具索引的只读视图：只有名称/摘要/标签/风险，不含 schema。
type ToolIndexEntry struct {
	Name    string
	Summary string
	Tags    []string
	Risk    string
}

// ToolIndex 返回当前 L1 工具索引快照（已按网关规则排除元工具自身），
// 供上下文用量估算算 toolsTokens——只算"名称+摘要+标签"，不把 schema 算进去（渐进披露）。
func (g *Gateway) ToolIndex() []ToolIndexEntry {
	if g == nil {
		return nil
	}
	tools := g.toolList()
	out := make([]ToolIndexEntry, 0, len(tools))
	for _, t := range tools {
		out = append(out, ToolIndexEntry{
			Name:    t.Name,
			Summary: TruncateRunes(strings.TrimSpace(t.Summary), SummaryMaxRunes),
			Tags:    append([]string{}, t.Tags...),
			Risk:    string(t.Risk),
		})
	}
	return out
}

// Search 在工具索引里按关键词检索（L1→定位）。打分规则可复现：
// 名称完全相等 6 分、名称包含 4 分、标签命中 3 分、摘要命中 2 分、名称分词命中 1 分；同分按名称排序。
func (g *Gateway) Search(query string, limit int) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, fmt.Errorf("tool.search 需要 query（例如“资金流”“涨停”“复盘”“板块排名”）")
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}
	tools := g.toolList()
	terms := searchTerms(query)
	hits := make([]SearchHit, 0, len(tools))
	for _, t := range tools {
		score := 0
		name := strings.ToLower(t.Name)
		q := strings.ToLower(query)
		summary := strings.ToLower(t.Summary)
		switch {
		case name == q:
			score += 6
		case strings.Contains(name, q):
			score += 4
		}
		for _, tag := range t.Tags {
			lt := strings.ToLower(tag)
			if lt == q || strings.Contains(lt, q) {
				score += 3
				break
			}
		}
		if strings.Contains(summary, q) {
			score += 2
		}
		for _, term := range terms {
			if strings.Contains(name, term) || strings.Contains(strings.ToLower(strings.Join(t.Tags, " ")), term) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		hits = append(hits, SearchHit{
			Name:    t.Name,
			Summary: TruncateRunes(t.Summary, SummaryMaxRunes),
			Tags:    append([]string{}, t.Tags...),
			Risk:    string(t.Risk),
			Score:   score,
		})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Name < hits[j].Name
	})
	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	res := SearchResult{Query: query, Total: total, Hits: hits}
	if total == 0 {
		res.Hint = "没有命中：换个更短的关键词（如“行情”“资金”“K线”“复盘”），或用 tool.describe 展开你猜到的工具名确认是否存在。"
	} else if total > len(hits) {
		res.Hint = fmt.Sprintf("共命中 %d 个，已按得分截断；需要更多可提高 limit 或换个关键词。", total)
	}
	return res, nil
}

// Describe 展开工具 schema（L2）。
func (g *Gateway) Describe(name string) (DescribeResult, error) {
	return g.Catalog.Describe(name)
}

// Call 执行工具（L3）。工具名必须已注册；风险等级不得超过 MaxRisk；每次都写审计。
func (g *Gateway) Call(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	if g.Host == nil {
		return CallResult{}, fmt.Errorf("工具网关未接入宿主：无法执行 %s（需要 kernel 注入 ToolHost）", name)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return CallResult{}, fmt.Errorf("tool.call 需要 name（先用 tool.search/tool.describe 确认工具名，不要猜测）")
	}
	var target *plugin.Tool
	for _, t := range g.Host.Tools() {
		if t.Name == name {
			tt := t
			target = &tt
			break
		}
	}
	if target == nil {
		g.record(name, "", false, "工具未注册")
		return CallResult{}, fmt.Errorf("工具未注册：%s（先用 tool.search 检索，禁止猜测工具名）", name)
	}
	if !target.Risk.AtMost(g.MaxRisk) {
		g.record(name, string(target.Risk), false, "风险等级超出网关上限")
		return CallResult{}, fmt.Errorf("工具 %s 的风险等级 %s 超出当前上限 %s，已拒绝执行", name, target.Risk, g.MaxRisk)
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if g.OnCall != nil {
		g.OnCall(name)
	}
	start := g.now()
	raw, err := g.Host.CallTool(ctx, name, args)
	elapsed := g.now().Sub(start).Milliseconds()
	if err != nil {
		g.record(name, string(target.Risk), false, err.Error())
		return CallResult{Name: name, Risk: string(target.Risk), ElapsedMs: elapsed}, fmt.Errorf("工具 %s 执行失败：%s", name, g.redact(err.Error()))
	}
	g.record(name, string(target.Risk), true, "")
	return CallResult{Name: name, Risk: string(target.Risk), ElapsedMs: elapsed, Result: raw}, nil
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gateway) record(action, risk string, ok bool, detail string) {
	if g.Audit == nil {
		return
	}
	g.Audit.Record(contract.AuditEvent{
		Time:   g.now(),
		Plugin: "progressive",
		Kind:   "gateway",
		Action: action,
		Risk:   risk,
		OK:     ok,
		Detail: g.redact(detail),
	})
}

// searchTerms 把查询拆成检索词：ASCII 词 + 中文单字（保证“涨停”“资金”这类短查询也能命中）。
func searchTerms(query string) []string {
	q := strings.ToLower(query)
	var out []string
	var buf []rune
	flush := func() {
		if len(buf) >= 2 {
			out = append(out, string(buf))
		}
		buf = buf[:0]
	}
	for _, r := range q {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			buf = append(buf, r)
			continue
		}
		flush()
		if unicode.Is(unicode.Han, r) {
			out = append(out, string(r))
		}
	}
	flush()
	return out
}

// MetaTools 把三个元工具包装成 plugin.Tool（由插件注册，Risk=read：它们不直接触网）。
func (g *Gateway) MetaTools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name:     "tool.search",
			Title:    "检索工具",
			Summary:  "按关键词找工具：先用它定位名称，再用 tool.describe 展开",
			Tags:     []string{"agent"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(toolSearchSchema),
			Examples: []string{`{"query":"资金流","limit":5}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("tool.search 参数不是合法 JSON：%w", err)
					}
				}
				return g.Search(in.Query, in.Limit)
			},
		},
		{
			Name:     "tool.describe",
			Title:    "展开工具 schema",
			Summary:  "查看某个工具的完整参数 schema 与示例（调用前必做）",
			Tags:     []string{"agent"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(toolDescribeSchema),
			Examples: []string{`{"name":"market.quote"}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Name string `json:"name"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("tool.describe 参数不是合法 JSON：%w", err)
					}
				}
				return g.Describe(in.Name)
			},
		},
		{
			Name:     "tool.call",
			Title:    "执行工具",
			Summary:  "按名称执行已注册工具，args 需匹配 describe 的 schema",
			Tags:     []string{"agent"},
			Risk:     plugin.RiskRead,
			Schema:   json.RawMessage(toolCallSchema),
			Examples: []string{`{"name":"market.quote","args":{"codes":["600000"]}}`},
			Run: func(ctx context.Context, args json.RawMessage) (any, error) {
				var in struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				}
				if len(args) > 0 {
					if err := json.Unmarshal(args, &in); err != nil {
						return nil, fmt.Errorf("tool.call 参数不是合法 JSON：%w", err)
					}
				}
				return g.Call(ctx, in.Name, in.Args)
			},
		},
	}
}

const toolSearchSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "关键词，2-8 字，如：资金流 / 涨停 / K线 / 复盘 / 板块"},
    "limit": {"type": "integer", "description": "返回条数，默认 8，上限 20"}
  },
  "required": ["query"]
}`

const toolDescribeSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "工具名，必须来自 tool.search 的结果，如 market.quote"}
  },
  "required": ["name"]
}`

const toolCallSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "已注册的工具名，必须先用 tool.describe 确认参数"},
    "args": {"type": "object", "description": "工具参数对象，字段与 tool.describe 的 schema 一致"}
  },
  "required": ["name", "args"]
}`
