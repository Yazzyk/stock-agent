package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// RouteInput 是路由输入。
type RouteInput struct {
	Worker    string
	Input     string
	SessionID string
}

// RouteDecision 是路由结论。
type RouteDecision struct {
	Worker  WorkerID `json:"worker"`
	Intent  string   `json:"intent,omitempty"`
	Fast    bool     `json:"fast"`
	Deduped bool     `json:"deduped"`
	Phase   string   `json:"phase,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// Router 是快慢双路径路由器：
// Fast Path 用规则（显式 worker / 关键词 / 交易时段 / 去重 / 限频），
// Slow Path 用轻量模型判意图，模型失败或不可用时回落到规则结果。
type Router struct {
	clock func() time.Time
	llm   contract.LLMService

	// DedupeWindow 内相同请求视为重复触发（默认 60s）。
	DedupeWindow time.Duration
	// MaxRunsPerMin 是每个 worker 每分钟最多触发次数（默认 6）。
	MaxRunsPerMin int

	mu   sync.Mutex
	last map[string]time.Time
	runs map[string][]time.Time
}

// NewRouter 构造路由器。
func NewRouter(clock func() time.Time, llm contract.LLMService) *Router {
	if clock == nil {
		clock = time.Now
	}
	return &Router{
		clock:         clock,
		llm:           llm,
		DedupeWindow:  60 * time.Second,
		MaxRunsPerMin: 6,
		last:          map[string]time.Time{},
		runs:          map[string][]time.Time{},
	}
}

// 关键词规则：按顺序匹配，先命中先用（越长越具体的放前面）。
var routeRules = []struct {
	worker   WorkerID
	keywords []string
}{
	{WorkerQuant, []string{"回测", "量化", "因子", "统计口径", "胜率", "期望收益", "代码实现", "选股模型"}},
	{WorkerPostmarket, []string{"复盘", "回顾", "收盘", "总结今天", "今天怎么样", "涨停家数", "连板梯队", "炸板"}},
	{WorkerPremarket, []string{"盘前", "计划", "今天怎么做", "开盘", "早盘", "观察清单", "预案"}},
	{WorkerIntraday, []string{"盯盘", "监控", "异动", "提醒", "现在", "盘中", "拉升", "跳水"}},
}

// Plan 产出路由结论。重复触发不报错，而是标记 Deduped 让上层回放上次结论；
// 触发过频返回错误，避免模型被循环调用烧钱。
func (r *Router) Plan(ctx context.Context, in RouteInput) (RouteDecision, error) {
	decision := RouteDecision{Phase: TradingPhase(r.clock())}
	raw := strings.TrimSpace(in.Worker)
	input := strings.TrimSpace(in.Input)

	if raw != "" {
		decision.Worker = NormalizeWorker(raw)
		decision.Fast = true
		if !isKnownWorkerAlias(raw) {
			decision.Note = fmt.Sprintf("未识别的 worker %q，已回落到 %s", raw, decision.Worker)
		}
	} else if ruleWorker, ok := matchRules(input); ok {
		decision.Worker = ruleWorker
		decision.Fast = true
		decision.Note = "规则命中"
	} else if r.llm != nil {
		// Slow Path：让轻量模型判意图；失败回落到关键词/闲聊。
		worker, intent, err := r.classify(ctx, input)
		if err == nil {
			decision.Worker = worker
			decision.Intent = intent
			decision.Note = "模型判意图"
		} else if ruleWorker, ok := matchRules(input); ok {
			decision.Worker = ruleWorker
			decision.Fast = true
			decision.Note = "模型判意图失败，回落到规则"
		} else {
			decision.Worker = WorkerChat
			decision.Fast = true
			decision.Note = "模型判意图失败，回落到聊天"
		}
	} else {
		decision.Worker = WorkerChat
		decision.Fast = true
	}
	if decision.Intent == "" {
		decision.Intent = truncateRunes(input, 40)
	}

	now := r.clock()
	key := string(decision.Worker) + "|" + normalizeInput(input)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	if prev, ok := r.last[key]; ok && now.Sub(prev) < r.DedupeWindow {
		decision.Deduped = true
		return decision, nil
	}
	if len(r.runs[string(decision.Worker)]) >= r.MaxRunsPerMin {
		return decision, fmt.Errorf("触发过于频繁：%s 每分钟最多 %d 次，请稍后再试", decision.Worker, r.MaxRunsPerMin)
	}
	r.last[key] = now
	r.runs[string(decision.Worker)] = append(r.runs[string(decision.Worker)], now)
	return decision, nil
}

func (r *Router) pruneLocked(now time.Time) {
	cutoff := now.Add(-time.Minute)
	for worker, stamps := range r.runs {
		kept := stamps[:0]
		for _, s := range stamps {
			if s.After(cutoff) {
				kept = append(kept, s)
			}
		}
		r.runs[worker] = kept
	}
	for key, at := range r.last {
		if now.Sub(at) > 10*time.Minute {
			delete(r.last, key)
		}
	}
}

func (r *Router) classify(ctx context.Context, input string) (WorkerID, string, error) {
	system := "你是意图分类器。把用户消息归到下面之一，只输出 JSON：{\"worker\":\"premarket|intraday|postmarket|quant|chat\",\"intent\":\"不超过20字的意图\"}。" +
		nl + "premarket=盘前计划，intraday=盘中盯盘，postmarket=收盘复盘，quant=量化研究，chat=普通概念问答。"
	temp := 0.0
	reply, err := r.llm.Chat(ctx, contract.ChatRequest{
		System:      system,
		Messages:    []contract.Message{{Role: contract.RoleUser, Content: input}},
		Temperature: &temp,
		MaxTokens:   80,
	})
	if err != nil {
		return "", "", err
	}
	body := extractJSON(reply.Message.Content)
	var parsed struct {
		Worker string `json:"worker"`
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "", "", fmt.Errorf("意图分类结果不是 JSON：%w", err)
	}
	return NormalizeWorker(parsed.Worker), strings.TrimSpace(parsed.Intent), nil
}

func matchRules(input string) (WorkerID, bool) {
	text := strings.ToLower(input)
	for _, rule := range routeRules {
		for _, kw := range rule.keywords {
			if strings.Contains(text, strings.ToLower(kw)) {
				return rule.worker, true
			}
		}
	}
	return "", false
}

func isKnownWorkerAlias(raw string) bool {
	switch NormalizeWorker(raw) {
	case WorkerPremarket:
		return containsAny(raw, "premarket", "plan", "pre", "morning")
	case WorkerIntraday:
		return containsAny(raw, "intraday", "monitor", "watch", "tick")
	case WorkerPostmarket:
		return containsAny(raw, "postmarket", "review", "post", "close")
	case WorkerQuant:
		return containsAny(raw, "quant", "backtest", "research")
	case WorkerChat:
		return containsAny(raw, "chat", "", "ask", "qa")
	}
	return false
}

func containsAny(raw string, candidates ...string) bool {
	v := strings.ToLower(strings.TrimSpace(raw))
	for _, c := range candidates {
		if v == c {
			return true
		}
	}
	return false
}

func normalizeInput(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

// TradingPhase 返回 A 股交易时段：premarket / intraday / lunch / postmarket / closed。
func TradingPhase(t time.Time) string {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}
	t = t.In(loc)
	switch t.Weekday() {
	case time.Saturday, time.Sunday:
		return "closed"
	}
	minutes := t.Hour()*60 + t.Minute()
	switch {
	case minutes < 9*60+15:
		return "premarket"
	case minutes <= 11*60+30:
		return "intraday"
	case minutes < 13*60:
		return "lunch"
	case minutes <= 15*60:
		return "intraday"
	default:
		return "postmarket"
	}
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 1 {
		return string(r[:1])
	}
	return string(r[:limit-1]) + "…"
}
