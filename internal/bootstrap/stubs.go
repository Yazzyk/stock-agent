package bootstrap

// 过渡实现（stub）：保证 kernel 与 Wails 骨架在数据层/模型层/存储层交付前就能编译运行。
// 交付后由 internal/store、internal/security、internal/datasource、internal/llm 顶替，
// 只需改上面 5 个变量，不动调用方。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	bucketSettings = "settings"
)

var errStubNotReady = errors.New("尚未接入实现（骨架阶段）")

// ---------- Store ----------

type stubStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

func stubOpenStore(cfg Config, logger *slog.Logger) (contract.Store, error) {
	logger.Warn("使用内存 Store（过渡实现），重启后数据丢失")
	return &stubStore{data: map[string]map[string][]byte{}}, nil
}

func (s *stubStore) Get(bucket, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.data[bucket]; ok {
		if v, ok := b[key]; ok {
			return v, nil
		}
	}
	return nil, contract.ErrNotFound
}

func (s *stubStore) Put(bucket, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = val
	return nil
}

func (s *stubStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.data[bucket]; ok {
		delete(b, key)
	}
	return nil
}

func (s *stubStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []contract.KVItem
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: v})
		}
	}
	return out, nil
}

func (s *stubStore) Close() error { return nil }

// ---------- Guard ----------

type stubGuard struct {
	policy contract.Policy
	roots  []string
}

func defaultPolicy(cfg Config) contract.Policy {
	return contract.Policy{
		// 只放行真正取数/推理的主机：同花顺、腾讯、新浪、DeepSeek、OpenAI。
		// 东方财富（eastmoney.com 全系）已从数据层移除，白名单同步收紧——
		// 留着域名等于给"以后又接回去"留后门。
		NetworkAllowlist: []string{
			"10jqka.com.cn", "gtimg.cn",
			"sinajs.cn", "sina.com.cn", "sina.cn", "hexun.com",
			// 同花顺官方金融数据 API（fuyao.aicubes.cn）：官方源优先、现有链路兜底。
			"aicubes.cn",
			"deepseek.com", "openai.com",
		},
		PathRoots:    []string{cfg.DataDir},
		AllowExec:    false,
		MaxReqPerMin: 120,
		MaxToolCalls: 30,
		// Token 不设预算上限：0 = 不限制（用户要求）。
		// 停机只由「单次运行工具调用上限」和模型自身的输出上限决定；
		// 如果确实需要成本闸门，在设置中心的内核策略里显式写一个 >0 的值即可。
		MaxTokensPerRun: 0,
		// 主控 Agent 可以调节观澜自身（调度/模型/盯盘/插件/知识库），见 app.method。
		AllowMethodControl: true,
	}
}

func stubNewGuard(cfg Config, store contract.Store, _ *slog.Logger) contract.Guard {
	policy := defaultPolicy(cfg)
	if raw, err := store.Get(bucketSettings, "policy"); err == nil && len(raw) > 0 {
		var p contract.Policy
		if json.Unmarshal(raw, &p) == nil {
			if hosts := stripRetiredHosts(p.NetworkAllowlist); len(hosts) > 0 {
				p.NetworkAllowlist = hosts
				if !jsonHasKey(raw, "allowMethodControl") {
					p.AllowMethodControl = policy.AllowMethodControl
				}
				policy = p
			}
		}
	}
	roots := policy.PathRoots
	if len(roots) == 0 {
		roots = []string{cfg.DataDir}
	}
	for i := range roots {
		if abs, err := filepath.Abs(roots[i]); err == nil {
			roots[i] = abs
		}
	}
	return &stubGuard{policy: policy, roots: roots}
}

func (g *stubGuard) AllowHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.Index(h, ":"); i > 0 {
		h = h[:i]
	}
	for _, allowed := range g.policy.NetworkAllowlist {
		a := strings.ToLower(allowed)
		if h == a || strings.HasSuffix(h, "."+a) {
			return true
		}
	}
	return false
}

func (g *stubGuard) CheckPath(p string) error {
	abs, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	for _, root := range g.roots {
		if abs == root || strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			return nil
		}
	}
	return fmt.Errorf("安全策略：路径 %s 不在允许目录内", p)
}

func (g *stubGuard) Redact(s string) string { return redactSecrets(s) }

func (g *stubGuard) Policy() contract.Policy { return g.policy }

var secretPatterns = []string{
	"sk-", "Bearer ", "api_key=", "apikey=", "token=", "password=", "Authorization:",
}

func redactSecrets(s string) string {
	out := s
	for _, p := range secretPatterns {
		if idx := strings.Index(out, p); idx >= 0 {
			end := idx + len(p)
			for end < len(out) && !strings.ContainsRune(" \t\n\r,;\"'", rune(out[end])) {
				end++
			}
			out = out[:idx] + p + "***" + out[end:]
		}
	}
	return out
}

// ---------- Audit ----------

type stubAudit struct {
	mu     sync.Mutex
	events []contract.AuditEvent
	max    int
}

func stubNewAudit(store contract.Store, _ *slog.Logger) contract.Audit {
	return &stubAudit{max: 2000, events: make([]contract.AuditEvent, 0, 256)}
}

func (a *stubAudit) Record(e contract.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	if len(a.events) > a.max {
		a.events = a.events[len(a.events)-a.max:]
	}
}

func (a *stubAudit) List(limit int) ([]contract.AuditEvent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if limit <= 0 || limit > len(a.events) {
		limit = len(a.events)
	}
	out := make([]contract.AuditEvent, 0, limit)
	for i := len(a.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, a.events[i])
	}
	return out, nil
}

// ---------- Data ----------

type stubData struct{}

func stubNewDataService(cfg Config, _ contract.Store, g contract.Guard, logger *slog.Logger) contract.DataService {
	return &stubData{}
}

func (d *stubData) Quote(context.Context, []string) ([]contract.Quote, error) {
	return nil, errStubNotReady
}
func (d *stubData) Bars(context.Context, string, contract.Period, int, contract.Adjust) ([]contract.Bar, error) {
	return nil, errStubNotReady
}
func (d *stubData) StockFundFlow(context.Context, string) (contract.FundFlow, error) {
	return contract.FundFlow{}, errStubNotReady
}
func (d *stubData) FundFlowRank(context.Context, string, int) ([]contract.FundFlow, error) {
	return nil, errStubNotReady
}
func (d *stubData) Sectors(context.Context, string) ([]contract.Sector, error) {
	return nil, errStubNotReady
}
func (d *stubData) Watchlist(context.Context) ([]contract.WatchItem, error) {
	return nil, errStubNotReady
}
func (d *stubData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	return contract.SyncResult{Success: false, Message: errStubNotReady.Error()}, nil
}
func (d *stubData) Search(context.Context, string) ([]contract.StockRef, error) {
	return nil, errStubNotReady
}
func (d *stubData) Health(context.Context) map[string]string {
	return map[string]string{"datasource": "stub"}
}

var _ = url.Parse
var _ = time.Now
