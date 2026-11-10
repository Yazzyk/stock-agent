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
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	Name       string     `json:"name,omitempty"`
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
