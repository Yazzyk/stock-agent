// Package contract 定义跨层契约：DTO、Service 接口、安全策略。
// 依赖方向：contract 不依赖本仓库任何其它包。
package contract

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ---------- 市场 DTO ----------

type Period string

const (
	PeriodDay   Period = "day"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
	PeriodMin60 Period = "min60"
	PeriodMin30 Period = "min30"
	PeriodMin15 Period = "min15"
	PeriodMin5  Period = "min5"
	PeriodMin1  Period = "min1"
)

// Adjust 复权方式：空 = 不复权，qfq 前复权，hfq 后复权。
type Adjust string

const (
	AdjustNone Adjust = ""
	AdjustQfq  Adjust = "qfq"
	AdjustHfq  Adjust = "hfq"
)

// Market 交易所前缀。
type Market string

const (
	MarketSH Market = "sh"
	MarketSZ Market = "sz"
	MarketBJ Market = "bj"
)

// Quote 是某一时刻的个股/指数快照。
type Quote struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Market    Market  `json:"market"`
	Price     float64 `json:"price"`
	PrevClose float64 `json:"prevClose"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Change    float64 `json:"change"`
	ChangePct float64 `json:"changePct"`
	Volume    float64 `json:"volume"`
	Amount    float64 `json:"amount"`
	Turnover  float64 `json:"turnover"`
	Amplitude float64 `json:"amplitude"`
	Time      string  `json:"time"`
	Source    string  `json:"source"`
	Stale     bool    `json:"stale,omitempty"`
}

// Bar 是一根 K 线。
type Bar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
	Amount float64 `json:"amount"`
}

// FundFlow 是资金流（个股 / 板块 / 概念）。
type FundFlow struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Date      string  `json:"date,omitempty"`
	MainNet   float64 `json:"mainNet"`
	SuperNet  float64 `json:"superNet,omitempty"`
	LargeNet  float64 `json:"largeNet,omitempty"`
	MediumNet float64 `json:"mediumNet,omitempty"`
	SmallNet  float64 `json:"smallNet,omitempty"`
	MainPct   float64 `json:"mainPct,omitempty"`
	ChangePct float64 `json:"changePct,omitempty"`
	Rank      int     `json:"rank,omitempty"`
	Source    string  `json:"source,omitempty"`
}

// Sector 是板块 / 概念条目。
type Sector struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	ChangePct float64 `json:"changePct"`
	MainNet   float64 `json:"mainNet,omitempty"`
	Count     int     `json:"count,omitempty"`
}

// WatchItem 是自选股条目。
type WatchItem struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Market Market `json:"market"`
	Group  string `json:"group"`
	Order  int    `json:"order"`
	Source string `json:"source,omitempty"`
}

// StockRef 是搜索结果。
type StockRef struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Market Market `json:"market"`
	Kind   string `json:"kind,omitempty"`
}

// SyncResult 是自选同步结果。
type SyncResult struct {
	Success            bool     `json:"success"`
	Message            string   `json:"message"`
	Groups             []string `json:"groups"`
	Added              int      `json:"added"`
	Removed            int      `json:"removed"`
	NeedWebLogin       bool     `json:"needWebLogin"`
	NeedFullDiskAccess bool     `json:"needFullDiskAccess"`
	Warnings           []string `json:"warnings"`
	Source             string   `json:"source,omitempty"`
	// Check 是同步完成后回填的最新前置诊断，前端据此继续引导或收起向导。
	Check *SyncCheck `json:"check,omitempty"`
}

// SyncSource 是三级同步来源之一的探测结果（只读诊断）。
type SyncSource struct {
	ID        string `json:"id"` // ths-local | ths-web | manual
	Label     string `json:"label"`
	Available bool   `json:"available"`
	State     string `json:"state"` // ok | denied | no-login | missing | empty
	Detail    string `json:"detail,omitempty"`
	Groups    int    `json:"groups,omitempty"`
	Items     int    `json:"items,omitempty"`
}

// SyncAction 是向导某一步的可执行动作；前端按 Kind 决定交互方式。
type SyncAction struct {
	Kind  string `json:"kind"` // open-url | open-settings | copy | capture-cookie | paste-cookie | retry-sync | none
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
}

// SyncStep 是同步向导的一步。
type SyncStep struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Detail string     `json:"detail"`
	Done   bool       `json:"done"`
	Action SyncAction `json:"action"`
}

// SyncCheck 描述同步前置条件（只读诊断，不触发同步）。
type SyncCheck struct {
	Local       SyncSource `json:"local"`
	Web         SyncSource `json:"web"`
	Manual      SyncSource `json:"manual"`
	Recommended string     `json:"recommended"` // ths-local | ths-web | manual | none
	Ready       bool       `json:"ready"`
	Message     string     `json:"message"`
	Steps       []SyncStep `json:"steps"`
	CheckedAt   string     `json:"checkedAt"`
}

// ---------- 数据服务 ----------

// DataService 是所有 A 股数据的唯一入口。实现见 internal/datasource。
type DataService interface {
	Quote(ctx context.Context, codes []string) ([]Quote, error)
	Bars(ctx context.Context, code string, p Period, limit int, adj Adjust) ([]Bar, error)
	StockFundFlow(ctx context.Context, code string) (FundFlow, error)
	FundFlowRank(ctx context.Context, kind string, limit int) ([]FundFlow, error)
	Sectors(ctx context.Context, kind string) ([]Sector, error)
	Watchlist(ctx context.Context) ([]WatchItem, error)
	SyncTHSWatchlist(ctx context.Context) (SyncResult, error)
	Search(ctx context.Context, keyword string) ([]StockRef, error)
	Health(ctx context.Context) map[string]string
}

// FundFlowPoint 是个股某一交易日的资金流明细（单位：元）。
type FundFlowPoint struct {
	Date      string  `json:"date"`
	MainNet   float64 `json:"mainNet"`
	SuperNet  float64 `json:"superNet"`
	LargeNet  float64 `json:"largeNet"`
	MediumNet float64 `json:"mediumNet"`
	SmallNet  float64 `json:"smallNet"`
	MainPct   float64 `json:"mainPct,omitempty"`
	Close     float64 `json:"close,omitempty"`
	ChangePct float64 `json:"changePct,omitempty"`
}

// FundFlowHistory 是个股多日资金流序列。
type FundFlowHistory struct {
	Code   string          `json:"code"`
	Name   string          `json:"name"`
	Days   int             `json:"days"`
	Source string          `json:"source,omitempty"`
	Points []FundFlowPoint `json:"points"`
}

// FundFlowHistoryService 是 DataService 的可选扩展：实现方（datasource）提供
// 多日资金流与批量快照；消费者用类型断言，避免扩大 DataService 接口导致所有 stub 连锁改动。
type FundFlowHistoryService interface {
	FundFlowHistory(ctx context.Context, code string, days int) (FundFlowHistory, error)
	FundFlowSnapshot(ctx context.Context, codes []string) ([]FundFlow, error)
}

// MinutePoint 是分时序列里的一个分钟点。成交量单位是「手」，成交额单位是「元」。
type MinutePoint struct {
	Time   string  `json:"time"`
	Price  float64 `json:"price"`
	Avg    float64 `json:"avg,omitempty"`
	Volume float64 `json:"volume,omitempty"`
	Amount float64 `json:"amount,omitempty"`
}

// MinuteSeries 是个股/指数某个交易日的分时序列（09:30~15:00，价格按当日实际成交价，不做复权）。
type MinuteSeries struct {
	Code      string        `json:"code"`
	Name      string        `json:"name"`
	Date      string        `json:"date"`
	PrevClose float64       `json:"prevClose"`
	Points    []MinutePoint `json:"points"`
	Source    string        `json:"source,omitempty"`
}

// ---------- 持久化 ----------

var ErrNotFound = errors.New("contract: key not found")

type KVItem struct {
	Key   string `json:"key"`
	Value []byte `json:"value"`
}

// Store 是本地持久化（sqlite，见 internal/store）。
type Store interface {
	Get(bucket, key string) ([]byte, error)
	Put(bucket, key string, val []byte) error
	Delete(bucket, key string) error
	List(bucket, prefix string) ([]KVItem, error)
	Close() error
}

// ---------- 模型服务 ----------

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
	// ReasoningContent 是推理模型的思维链（DeepSeek V4 等把正文放 reasoning_content）。
	// 正文为空而它非空时，调用方必须如实回退到它，而不是当成"模型没返回内容"。
	ReasoningContent string     `json:"reasoningContent,omitempty"`
	ToolCalls        []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID       string     `json:"toolCallId,omitempty"`
	Name             string     `json:"name,omitempty"`
	// FinishReason 是上游的结束原因（stop / length / tool_calls / content_filter …）。
	// 为空表示上游没给；"length" 说明输出被 max_tokens 截断，是空正文的常见根因。
	FinishReason string `json:"finishReason,omitempty"`
}

type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	// CachedTokens 是提示词命中的缓存 token 数（提供方上报；
	// DeepSeek 的 prompt_cache_hit_tokens 与 OpenAI 的 prompt_tokens_details.cached_tokens
	// 都会被适配层归一到这里；上游没给时为 0，不要自行估算）。
	CachedTokens int `json:"cachedTokens,omitempty"`
}

type ChatRequest struct {
	Model       string     `json:"model,omitempty"`
	System      string     `json:"system,omitempty"`
	Messages    []Message  `json:"messages"`
	Tools       []ToolInfo `json:"tools,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
	MaxTokens   int        `json:"maxTokens,omitempty"`
	Stream      bool       `json:"stream,omitempty"`
}

type Reply struct {
	Message Message `json:"message"`
	Usage   Usage   `json:"usage"`
	Model   string  `json:"model"`
}

type ModelInfo struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	DisplayName string `json:"displayName,omitempty"`
	BaseURL     string `json:"baseUrl,omitempty"`
	HasKey      bool   `json:"hasKey"`
	IsDefault   bool   `json:"isDefault"`
	MaxTokens   int    `json:"maxTokens,omitempty"`
	// ContextWindow 是模型上下文窗口（token），来自 provider 预设；未知为 0。
	ContextWindow int `json:"contextWindow,omitempty"`
}

// LLMService 由 internal/llm 实现（Eino 模型适配）。
type LLMService interface {
	Chat(ctx context.Context, req ChatRequest) (Reply, error)
	Stream(ctx context.Context, req ChatRequest, onDelta func(string)) (Reply, error)
	Models(ctx context.Context) ([]ModelInfo, error)
	Default(ctx context.Context) (ModelInfo, error)
}

// ---------- 安全 ----------

// Policy 是安全策略的唯一真源，由 settings 写入、Guard 执行。
type Policy struct {
	NetworkAllowlist []string `json:"networkAllowlist"`
	PathRoots        []string `json:"pathRoots"`
	AllowExec        bool     `json:"allowExec"`
	MaxReqPerMin     int      `json:"maxReqPerMin"`
	MaxToolCalls     int      `json:"maxToolCalls"`
	MaxTokensPerRun  int      `json:"maxTokensPerRun"`
	// AllowMethodControl 允许主控 Agent 通过 app.method 调节观澜自身
	// （改值班调度、模型设置、盯盘规则、启停插件、导入知识库…）。
	// 默认 true；它只放开"插件方法"，不放任意代码执行（那是 AllowExec）。
	AllowMethodControl bool `json:"allowMethodControl"`
}

type Guard interface {
	AllowHost(host string) bool
	CheckPath(p string) error
	Redact(s string) string
	Policy() Policy
}

type AuditEvent struct {
	Time    time.Time `json:"time"`
	Plugin  string    `json:"plugin"`
	Kind    string    `json:"kind"`
	Action  string    `json:"action"`
	Detail  string    `json:"detail,omitempty"`
	Risk    string    `json:"risk,omitempty"`
	OK      bool      `json:"ok"`
	Elapsed int64     `json:"elapsedMs,omitempty"`
}

// Audit 是 append-only 审计日志。
type Audit interface {
	Record(e AuditEvent)
	List(limit int) ([]AuditEvent, error)
}
