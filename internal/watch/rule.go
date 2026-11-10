// Package watch 实现"用户自定义 Watchdog"：用户声明要盯什么（行情/资金流/板块/K线/新闻/时间），
// 条件命中后触发一次 Agent 任务；规则以文件为插件形态存在（~/.guanlan/watches/*.json），
// 可热加载、可开关、可由 Agent 生成。
//
// 安全边界（本包的设计前提）：
//   - 规则是**声明式**的：条件只能求值本包白名单里的数值指标，不能调用函数、不能访问网络、不能执行代码；
//   - 触发动作只是"给 Agent 派一次带模板的任务"，不直接下单、不直接写任意文件；
//   - 每个规则有冷却时间与全局限流，避免把模型调用打爆。
package watch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SourceType 是触发源类型。
type SourceType string

const (
	SourceQuote    SourceType = "quote"    // 个股快照
	SourceSector   SourceType = "sector"   // 板块/概念
	SourceKline    SourceType = "kline"    // 单个标的的K线指标
	SourceNews     SourceType = "news"     // 新闻/公告关键词
	SourceSchedule SourceType = "schedule" // 纯时间触发
)

// Source 描述"盯什么"。
type Source struct {
	Type    SourceType `json:"type"`
	Codes   []string   `json:"codes,omitempty"`   // quote/kline 用
	Sector  string     `json:"sector,omitempty"`  // sector 用（板块或概念名）
	Kind    string     `json:"kind,omitempty"`    // sector: sector|concept；news: roll|notice
	Keyword string     `json:"keyword,omitempty"` // news 用
	Period  string     `json:"period,omitempty"`  // kline 周期，默认 day
	Times   []string   `json:"times,omitempty"`   // schedule 用，如 ["09:20","14:50"]
}

// Action 描述条件命中后做什么。
type Action struct {
	Worker string `json:"worker"` // agentlab 的 worker：chat/intraday/premarket/postmarket/quant
	Prompt string `json:"prompt"` // 模板，可用 {metric}、{code}、{sector} 占位
}

// Rule 是一条 Watch 规则（文件即插件）。
type Rule struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	Enabled         bool     `json:"enabled"`
	Sources         []Source `json:"sources"`
	Condition       string   `json:"condition"`
	Action          Action   `json:"action"`
	CooldownSeconds int      `json:"cooldownSeconds,omitempty"`
	QuietWindows    []string `json:"quietWindows,omitempty"` // 如 ["11:30-13:00"]
	CreatedBy       string   `json:"createdBy,omitempty"`    // user | agent
	CreatedAt       string   `json:"createdAt,omitempty"`
	UpdatedAt       string   `json:"updatedAt,omitempty"`
	// 运行时状态（不入库）
	LastTriggeredAt string `json:"lastTriggeredAt,omitempty"`
	TriggerCount    int    `json:"triggerCount,omitempty"`
	LastError       string `json:"lastError,omitempty"`
}

// Validate 在保存前做严格校验：规则是会被自动执行的，宁严勿松。
func (r *Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("规则名称不能为空")
	}
	if len(r.Name) > 60 {
		return errors.New("规则名称过长（≤60 字）")
	}
	if len(r.Sources) == 0 {
		return errors.New("至少需要一个触发源")
	}
	if len(r.Sources) > 6 {
		return errors.New("触发源最多 6 个")
	}
	for i := range r.Sources {
		s := &r.Sources[i]
		switch s.Type {
		case SourceQuote, SourceKline:
			if len(s.Codes) == 0 {
				return fmt.Errorf("第 %d 个触发源缺少 codes", i+1)
			}
			if len(s.Codes) > 20 {
				return fmt.Errorf("第 %d 个触发源标的过多（≤20）", i+1)
			}
			for _, c := range s.Codes {
				if !validCode(c) {
					return fmt.Errorf("非法股票代码: %q（示例 600000 / sh600000）", c)
				}
			}
		case SourceSector:
			if strings.TrimSpace(s.Sector) == "" {
				return fmt.Errorf("第 %d 个触发源缺少 sector（板块/概念名）", i+1)
			}
		case SourceNews:
			if strings.TrimSpace(s.Keyword) == "" {
				return fmt.Errorf("第 %d 个触发源缺少 keyword", i+1)
			}
		case SourceSchedule:
			if len(s.Times) == 0 {
				return fmt.Errorf("第 %d 个触发源缺少 times（如 09:20）", i+1)
			}
			for _, t := range s.Times {
				if _, err := time.Parse("15:04", t); err != nil {
					return fmt.Errorf("非法时间 %q（格式 HH:MM）", t)
				}
			}
		default:
			return fmt.Errorf("未知触发源类型 %q（可选 quote/sector/kline/news/schedule）", s.Type)
		}
	}
	if strings.TrimSpace(r.Condition) == "" {
		r.Condition = "true"
	}
	if _, err := Parse(r.Condition); err != nil {
		return fmt.Errorf("条件表达式非法: %w", err)
	}
	if engines := referencedMetrics(r.Condition); len(engines) > 0 {
		bad := ""
		for _, m := range engines {
			if _, ok := MetricSpecs[m]; !ok {
				bad = m
				break
			}
		}
		if bad != "" {
			return fmt.Errorf("条件里用了未定义的指标 %q（可用指标见 watch.templates 或面板）", bad)
		}
	}
	if r.Action.Worker == "" {
		r.Action.Worker = "intraday"
	}
	switch r.Action.Worker {
	case "chat", "intraday", "premarket", "postmarket", "quant":
	default:
		return fmt.Errorf("未知 worker %q（可选 chat/intraday/premarket/postmarket/quant）", r.Action.Worker)
	}
	if strings.TrimSpace(r.Action.Prompt) == "" {
		r.Action.Prompt = "请根据以下信号做一次简短的盘面判断，并给出观察要点：{summary}"
	}
	if len(r.Action.Prompt) > 2000 {
		return errors.New("动作提示过长（≤2000 字）")
	}
	if r.CooldownSeconds <= 0 {
		r.CooldownSeconds = 300
	}
	if r.CooldownSeconds < 30 {
		return errors.New("冷却时间不得低于 30 秒（避免频繁触发模型调用）")
	}
	for _, w := range r.QuietWindows {
		if _, _, err := parseWindow(w); err != nil {
			return err
		}
	}
	return nil
}

// Normalize 补齐 ID/时间戳等默认值。
func (r *Rule) Normalize(by string, now time.Time) {
	if r.ID == "" {
		r.ID = NewID()
	}
	if r.CreatedBy == "" {
		r.CreatedBy = by
	}
	if r.CreatedAt == "" {
		r.CreatedAt = now.Format(time.RFC3339)
	}
	r.UpdatedAt = now.Format(time.RFC3339)
	if r.Action.Worker == "" {
		r.Action.Worker = "intraday"
	}
}

// NewID 生成规则 ID。
func NewID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("w%d", time.Now().UnixNano())
	}
	return "w" + hex.EncodeToString(b)
}

// Summary 生成规则的一句话描述（面板与审计用）。
func (r Rule) Summary() string {
	parts := make([]string, 0, len(r.Sources))
	for _, s := range r.Sources {
		switch s.Type {
		case SourceQuote:
			parts = append(parts, "行情:"+strings.Join(s.Codes, ","))
		case SourceSector:
			parts = append(parts, "板块:"+s.Sector)
		case SourceKline:
			parts = append(parts, "K线:"+strings.Join(s.Codes, ","))
		case SourceNews:
			parts = append(parts, "新闻:"+s.Keyword)
		case SourceSchedule:
			parts = append(parts, "定时:"+strings.Join(s.Times, ","))
		}
	}
	return strings.Join(parts, " | ") + " ⇒ " + r.Condition
}

func validCode(c string) bool {
	c = strings.TrimSpace(strings.ToLower(c))
	if strings.HasPrefix(c, "sh") || strings.HasPrefix(c, "sz") || strings.HasPrefix(c, "bj") {
		c = c[2:]
	}
	if len(c) != 6 {
		return false
	}
	for _, r := range c {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseWindow 解析 "11:30-13:00"。
func parseWindow(w string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(w), "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("静默时段格式错误 %q（应为 HH:MM-HH:MM）", w)
	}
	toMin := func(s string) (int, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Errorf("静默时段时间非法 %q", s)
		}
		return t.Hour()*60 + t.Minute(), nil
	}
	start, err := toMin(parts[0])
	if err != nil {
		return 0, 0, err
	}
	end, err := toMin(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

// inQuietWindow 判断时间是否落在静默时段（支持跨零点）。
func inQuietWindow(now time.Time, windows []string) bool {
	m := now.Hour()*60 + now.Minute()
	for _, w := range windows {
		start, end, err := parseWindow(w)
		if err != nil {
			continue
		}
		if start <= end {
			if m >= start && m < end {
				return true
			}
		} else if m >= start || m < end {
			return true
		}
	}
	return false
}

// ExampleRules 返回三条可直接用的模板（面板"从模板新建"用）。
func ExampleRules() []Rule {
	return []Rule{
		{
			Name:            "半导体板块资金异动",
			Sources:         []Source{{Type: SourceSector, Sector: "半导体", Kind: "sector"}},
			Condition:       "sector.mainNetYi > 20",
			Action:          Action{Worker: "intraday", Prompt: "板块 {sector} 主力净流入 {sector.mainNetYi} 亿元，涨跌 {sector.changePct}%。请判断资金持续性，并给出 3 条观察要点（不构成投资建议）。"},
			CooldownSeconds: 600,
			// 模板默认关闭：规则会真的触发模型调用，默认只当"示例"摆着，由用户显式开启。
			Enabled:     false,
			CreatedBy:   "template",
			Description: "板块主力净流入超过 20 亿时让 Agent 判断持续性",
		},
		{
			Name:            "自选股放量突破",
			Sources:         []Source{{Type: SourceKline, Codes: []string{"600000"}, Period: "day"}},
			Condition:       "kline.volumeRatio > 2 and kline.aboveMA20 > 0",
			Action:          Action{Worker: "intraday", Prompt: "标的出现放量突破（量比 {kline.volumeRatio}，距 MA20 {kline.ma20dev}%），请结合形态判断是真突破还是诱多。"},
			CooldownSeconds: 900,
			Enabled:         false,
			CreatedBy:       "template",
			Description:     "量比 > 2 且站上 MA20 时触发",
		},
		{
			Name:            "政策/公告关键词监控",
			Sources:         []Source{{Type: SourceNews, Keyword: "并购重组", Kind: "notice"}},
			Condition:       "news.hits > 0",
			Action:          Action{Worker: "chat", Prompt: "出现了 {news.hits} 条与「{keyword}」相关的公告/新闻（示例：{summary}）。请评估可能受益的方向与需要验证的数据。"},
			CooldownSeconds: 1800,
			Enabled:         false,
			CreatedBy:       "template",
			Description:     "公告出现关键词时让 Agent 做方向研究",
		},
	}
}

// SortRules 按启用状态、更新时间排序（面板展示用）。
func SortRules(rules []Rule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Enabled != rules[j].Enabled {
			return rules[i].Enabled
		}
		return rules[i].UpdatedAt > rules[j].UpdatedAt
	})
}
