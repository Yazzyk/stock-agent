package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/internal/market"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// TriggerFunc 把一次命中变成一次 Agent 任务；由插件接到 agent.runtime。
type TriggerFunc func(ctx context.Context, worker, prompt string) (string, error)

// Options 是引擎依赖。
type Options struct {
	Store   contract.Store
	Data    contract.DataService
	Guard   contract.Guard
	Audit   contract.Audit
	Events  *plugin.Bus
	Trigger TriggerFunc
	Clock   func() time.Time
	Logger  *slog.Logger
	DataDir string

	Tick               time.Duration
	MaxConcurrent      int
	MaxTriggersPerHour int
	TradingOnly        bool
}

const (
	rulesBucket = "watch_rules"
	runsBucket  = "watch_runs"
	maxRunLogs  = 200
)

// RunLog 是一次触发/求值记录。
type RunLog struct {
	Time      string             `json:"time"`
	RuleID    string             `json:"ruleId"`
	RuleName  string             `json:"ruleName"`
	Worker    string             `json:"worker"`
	Triggered bool               `json:"triggered"`
	Matched   bool               `json:"matched"`
	Summary   string             `json:"summary,omitempty"`
	Metrics   map[string]float64 `json:"metrics,omitempty"`
	Report    string             `json:"report,omitempty"`
	Error     string             `json:"error,omitempty"`
	ElapsedMs int64              `json:"elapsedMs,omitempty"`
}

// Scenario 是一条规则在一个标的/板块上的求值结果。
type Scenario struct {
	Code    string             `json:"code,omitempty"`
	Name    string             `json:"name,omitempty"`
	Sector  string             `json:"sector,omitempty"`
	Metrics map[string]float64 `json:"metrics"`
	Matched bool               `json:"matched"`
}

// TestResult 是"试运行"结果：只求值，不触发 Agent。
type TestResult struct {
	RuleID    string     `json:"ruleId"`
	Matched   bool       `json:"matched"`
	Scenarios []Scenario `json:"scenarios"`
	Summary   string     `json:"summary"`
	Prompt    string     `json:"prompt"`
	Error     string     `json:"error,omitempty"`
	ElapsedMs int64      `json:"elapsedMs,omitempty"`
}

type newsCacheEntry struct {
	items []NewsItem
	at    time.Time
}

// Engine 是 Watchdog 运行时：定时求值、限流触发、落盘与热加载。
type Engine struct {
	opts Options
	log  *slog.Logger

	mu        sync.RWMutex
	rules     map[string]*Rule
	modTime   map[string]time.Time
	lastFired map[string]time.Time
	fires     map[string][]time.Time
	running   map[string]bool
	runs      []RunLog
	newsCache map[string]newsCacheEntry
	started   bool

	cancel context.CancelFunc
	now    func() time.Time
}

// New 创建引擎（不启动调度）。
func New(opts Options) (*Engine, error) {
	if opts.Store == nil || opts.Data == nil {
		return nil, fmt.Errorf("watch: 需要 Store 与 Data")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Tick <= 0 {
		opts.Tick = 30 * time.Second
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 1
	}
	if opts.MaxTriggersPerHour <= 0 {
		opts.MaxTriggersPerHour = 6
	}
	if opts.DataDir == "" {
		opts.DataDir = filepath.Join(os.TempDir(), "guanlan")
	}
	e := &Engine{
		opts:      opts,
		log:       opts.Logger,
		rules:     map[string]*Rule{},
		modTime:   map[string]time.Time{},
		lastFired: map[string]time.Time{},
		fires:     map[string][]time.Time{},
		running:   map[string]bool{},
		newsCache: map[string]newsCacheEntry{},
		now:       opts.Clock,
	}
	if err := e.load(); err != nil {
		return nil, err
	}
	e.syncFiles()
	return e, nil
}

// Start 启动调度循环。
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return
	}
	e.started = true
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.mu.Unlock()

	go func() {
		t := time.NewTicker(e.opts.Tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.tick(context.Background())
			}
		}
	}()
	e.log.Info("Watchdog 已启动", "tick", e.opts.Tick, "rules", len(e.List()))
}

// Stop 停止调度。
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.started = false
}

// Running 返回是否已启动。
func (e *Engine) Running() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.started
}

// ---------- 规则 CRUD ----------

// List 返回全部规则（按启用/更新时间排序）。
func (e *Engine) List() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Rule, 0, len(e.rules))
	for _, r := range e.rules {
		out = append(out, *r)
	}
	SortRules(out)
	return out
}

// Get 按 ID 取规则。
func (e *Engine) Get(id string) (Rule, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r, ok := e.rules[id]
	if !ok {
		return Rule{}, false
	}
	return *r, true
}

// Save 校验并保存规则（同时写库与文件，文件是"插件"形态）。
func (e *Engine) Save(r Rule, by string) (Rule, error) {
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	r.Normalize(by, e.now())

	// 存进 map 的是独立副本：调度 goroutine 会通过 setError 改写 map 里的对象，
	// 若与本地 r 共享同一份内存，就会和下面的持久化/审计读发生数据竞争。
	stored := r
	e.mu.Lock()
	e.rules[r.ID] = &stored
	e.mu.Unlock()

	if err := e.persistRule(r); err != nil {
		return Rule{}, err
	}
	if err := e.writeFile(r); err != nil {
		e.log.Warn("规则文件写入失败（仅入库）", "rule", r.ID, "error", err)
	}
	e.audit("watch", "save_rule", r.ID, true, r.Summary())
	e.publish("watch.rules.changed", map[string]string{"id": r.ID, "action": "save"})
	return r, nil
}

// Delete 删除规则（库 + 文件）。
func (e *Engine) Delete(id string) error {
	e.mu.Lock()
	_, ok := e.rules[id]
	delete(e.rules, id)
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("规则 %s 不存在", id)
	}
	_ = e.opts.Store.Delete(rulesBucket, id)
	_ = os.Remove(e.ruleFile(id))
	e.audit("watch", "delete_rule", id, true, "")
	e.publish("watch.rules.changed", map[string]string{"id": id, "action": "delete"})
	return nil
}

// Toggle 开关规则。
func (e *Engine) Toggle(id string, enabled bool) (Rule, error) {
	e.mu.Lock()
	r, ok := e.rules[id]
	e.mu.Unlock()
	if !ok {
		return Rule{}, fmt.Errorf("规则 %s 不存在", id)
	}
	r.Enabled = enabled
	return e.Save(*r, "user")
}

// Logs 返回最近的触发记录（倒序）。
func (e *Engine) Logs(limit int) []RunLog {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if limit <= 0 || limit > len(e.runs) {
		limit = len(e.runs)
	}
	out := make([]RunLog, 0, limit)
	for i := len(e.runs) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, e.runs[i])
	}
	return out
}

// ---------- 求值 ----------

// Test 只求值不触发，用于面板"试运行"与 Agent 自查。
func (e *Engine) Test(ctx context.Context, id string) (TestResult, error) {
	r, ok := e.Get(id)
	if !ok {
		return TestResult{}, fmt.Errorf("规则 %s 不存在", id)
	}
	start := e.now()
	scenarios, summary, prompt, err := e.evaluate(ctx, r)
	res := TestResult{RuleID: id, Scenarios: scenarios, Summary: summary, Prompt: prompt}
	for _, s := range scenarios {
		if s.Matched {
			res.Matched = true
			break
		}
	}
	if err != nil {
		res.Error = err.Error()
	}
	res.ElapsedMs = e.now().Sub(start).Milliseconds()
	return res, nil
}

// tick 遍历启用的规则。
func (e *Engine) tick(ctx context.Context) {
	e.syncFiles()
	for _, r := range e.List() {
		if !r.Enabled {
			continue
		}
		if e.shouldSkip(r) {
			continue
		}
		scenarios, summary, prompt, err := e.evaluate(ctx, r)
		if err != nil {
			e.record(RunLog{Time: e.now().Format(time.RFC3339), RuleID: r.ID, RuleName: r.Name, Worker: r.Action.Worker, Error: err.Error()})
			e.setError(r.ID, err.Error())
			continue
		}
		matched := false
		for _, s := range scenarios {
			if s.Matched {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		e.fire(ctx, r, summary, prompt, scenarios)
	}
}

// shouldSkip 处理静默时段、冷却、限频与交易时段。
func (e *Engine) shouldSkip(r Rule) bool {
	now := e.now()
	if inQuietWindow(now, r.QuietWindows) {
		return true
	}
	if e.opts.TradingOnly && hasMarketSource(r) && !isTradingTime(now) {
		return true
	}
	e.mu.RLock()
	last := e.lastFired[r.ID]
	times := append([]time.Time{}, e.fires[r.ID]...)
	running := e.running[r.ID]
	active := 0
	for _, v := range e.running {
		if v {
			active++
		}
	}
	e.mu.RUnlock()
	if running {
		return true
	}
	if !last.IsZero() && now.Sub(last) < time.Duration(r.CooldownSeconds)*time.Second {
		return true
	}
	cutoff := now.Add(-time.Hour)
	count := 0
	for _, t := range times {
		if t.After(cutoff) {
			count++
		}
	}
	if count >= e.opts.MaxTriggersPerHour {
		return true
	}
	return active >= e.opts.MaxConcurrent
}

func hasMarketSource(r Rule) bool {
	for _, s := range r.Sources {
		switch s.Type {
		case SourceQuote, SourceSector, SourceKline:
			return true
		}
	}
	return false
}

// evaluate 组装指标上下文并逐个场景求值。
func (e *Engine) evaluate(ctx context.Context, r Rule) ([]Scenario, string, string, error) {
	tree, err := Parse(r.Condition)
	if err != nil {
		return nil, "", "", err
	}
	shared, err := e.sharedMetrics(ctx, r)
	if err != nil {
		return nil, "", "", err
	}

	var scenarios []Scenario
	var firstErr error
	for _, src := range r.Sources {
		if src.Type != SourceQuote && src.Type != SourceKline {
			continue
		}
		for _, code := range src.Codes {
			m, name := e.codeMetrics(ctx, code)
			merged := mergeMetrics(shared, m)
			ok, evalErr := Eval(tree, merged)
			if evalErr != nil {
				firstErr = evalErr
			}
			scenarios = append(scenarios, Scenario{Code: code, Name: name, Metrics: merged, Matched: ok})
		}
	}
	if len(scenarios) == 0 {
		// 没有行情类触发源：用共享指标求值一次。
		ok, evalErr := Eval(tree, shared)
		if evalErr != nil {
			firstErr = evalErr
		}
		scenarios = append(scenarios, Scenario{Sector: sectorOf(r), Metrics: shared, Matched: ok})
	}

	summary := buildSummary(r, scenarios, shared)
	prompt := renderPrompt(r.Action.Prompt, scenarios, shared)
	return scenarios, summary, prompt, firstErr
}

// sharedMetrics 取板块/新闻/时间指标。
func (e *Engine) sharedMetrics(ctx context.Context, r Rule) (map[string]float64, error) {
	m := map[string]float64{}
	now := e.now()
	m["time.hour"] = float64(now.Hour())
	m["time.minute"] = float64(now.Minute())
	if isTradingTime(now) {
		m["time.trading"] = 1
	}

	for _, src := range r.Sources {
		switch src.Type {
		case SourceSector:
			kind := src.Kind
			if kind == "" {
				kind = "sector"
			}
			sectors, err := e.sectors(ctx, kind)
			if err != nil {
				e.log.Warn("取板块数据失败", "rule", r.ID, "error", err)
				continue
			}
			for _, s := range sectors {
				if s.Name == src.Sector || strings.Contains(s.Name, src.Sector) {
					m["sector.changePct"] = s.ChangePct
					m["sector.mainNet"] = s.MainNet
					m["sector.mainNetYi"] = round2(s.MainNet / 1e8)
					break
				}
			}
		case SourceNews:
			items, err := e.newsFor(ctx, r.ID, src)
			if err != nil {
				e.log.Warn("取新闻失败", "rule", r.ID, "error", err)
				continue
			}
			hits, _ := keywordHits(items, src.Keyword)
			m["news.hits"] = float64(hits)
			if hasNegative(items) {
				m["news.negative"] = 1
			}
		}
	}
	return m, nil
}

func (e *Engine) codeMetrics(ctx context.Context, code string) (map[string]float64, string) {
	m := map[string]float64{}
	name := ""
	quotes, err := e.opts.Data.Quote(ctx, []string{code})
	if err == nil && len(quotes) > 0 {
		q := quotes[0]
		name = q.Name
		m["quote.price"] = q.Price
		m["quote.changePct"] = q.ChangePct
		m["quote.turnover"] = q.Turnover
		m["quote.amount"] = q.Amount
		if q.Volume > 0 {
			m["quote.volumeRatio"] = 0
		}
	}
	if flow, ferr := e.opts.Data.StockFundFlow(ctx, code); ferr == nil {
		m["flow.mainNet"] = flow.MainNet
		m["flow.mainNetYi"] = round2(flow.MainNet / 1e8)
		if flow.MainPct != 0 {
			m["flow.mainNetPct"] = round2(flow.MainPct)
		} else if q, qerr := e.opts.Data.Quote(ctx, []string{code}); qerr == nil && len(q) > 0 && q[0].Amount > 0 {
			m["flow.mainNetPct"] = round2(flow.MainNet / q[0].Amount * 100)
		}
	}
	bars, err := e.opts.Data.Bars(ctx, code, contract.PeriodDay, 120, contract.AdjustNone)
	if err == nil && len(bars) >= 21 {
		closes := market.Closes(bars)
		last := bars[len(bars)-1]
		if len(bars) >= 5 {
			m["kline.volumeRatio"] = round2(last.Volume / avgVolume(bars, 5))
		}
		ma20 := lastValid(market.SMA(closes, 20))
		if ma20 > 0 {
			m["kline.ma20dev"] = round2((last.Close - ma20) / ma20 * 100)
			if last.Close > ma20 {
				m["kline.aboveMA20"] = 1
			}
		}
		rsi := lastValid(market.RSI(closes, 14))
		m["kline.rsi"] = round2(rsi)
		dif, dea, hist := market.MACD(closes, 12, 26, 9)
		_ = dif
		_ = dea
		m["kline.macd"] = round4(lastValid(hist))
		atr := lastValid(market.ATR(bars, 14))
		if last.Close > 0 {
			m["kline.atrPct"] = round2(atr / last.Close * 100)
		}
	}
	if name == "" && len(bars) > 0 {
		name = ""
	}
	return m, name
}

func (e *Engine) sectors(ctx context.Context, kind string) ([]contract.Sector, error) {
	if kind == "" {
		kind = "sector"
	}
	if cached, ok := e.cachedSectors(kind); ok {
		return cached, nil
	}
	out, err := e.opts.Data.Sectors(ctx, kind)
	if err != nil {
		return nil, err
	}
	e.cacheSectors(kind, out)
	return out, nil
}

func (e *Engine) newsFor(ctx context.Context, ruleID string, src Source) ([]NewsItem, error) {
	key := src.Kind + "|" + src.Keyword
	e.mu.RLock()
	entry, ok := e.newsCache[key]
	e.mu.RUnlock()
	if ok && e.now().Sub(entry.at) < 2*time.Minute {
		return entry.items, nil
	}
	items, err := e.fetchNews(ctx, src)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.newsCache[key] = newsCacheEntry{items: items, at: e.now()}
	e.mu.Unlock()
	return items, nil
}

// fire 异步触发一次 Agent 任务。
func (e *Engine) fire(ctx context.Context, r Rule, summary, prompt string, scenarios []Scenario) {
	if e.opts.Trigger == nil {
		e.record(RunLog{Time: e.now().Format(time.RFC3339), RuleID: r.ID, RuleName: r.Name, Matched: true, Summary: summary, Error: "未配置 Agent 触发器"})
		return
	}
	e.mu.Lock()
	e.running[r.ID] = true
	e.lastFired[r.ID] = e.now()
	e.fires[r.ID] = append(e.fires[r.ID], e.now())
	e.mu.Unlock()

	metrics := map[string]float64{}
	for _, s := range scenarios {
		if s.Matched {
			for k, v := range s.Metrics {
				metrics[k] = v
			}
			break
		}
	}
	e.publish("watch.triggered", map[string]any{"ruleId": r.ID, "name": r.Name, "worker": r.Action.Worker, "summary": summary, "metrics": metrics})
	e.audit("watch", "trigger", r.ID, true, r.Action.Worker+": "+summary)

	go func() {
		start := e.now()
		report, err := e.opts.Trigger(context.Background(), r.Action.Worker, prompt)
		entry := RunLog{
			Time: e.now().Format(time.RFC3339), RuleID: r.ID, RuleName: r.Name, Worker: r.Action.Worker,
			Triggered: true, Matched: true, Summary: summary, Metrics: metrics,
			ElapsedMs: e.now().Sub(start).Milliseconds(),
		}
		if err != nil {
			entry.Error = err.Error()
			e.setError(r.ID, err.Error())
		} else {
			entry.Report = truncate(report, 2000)
			e.setError(r.ID, "")
		}
		e.record(entry)
		e.publish("watch.run", map[string]any{"ruleId": r.ID, "ok": err == nil, "report": entry.Report, "error": entry.Error})
		e.mu.Lock()
		delete(e.running, r.ID)
		e.mu.Unlock()
	}()
}

// ---------- 持久化 ----------

func (e *Engine) load() error {
	items, err := e.opts.Store.List(rulesBucket, "")
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, it := range items {
		var r Rule
		if err := json.Unmarshal(it.Value, &r); err != nil {
			e.log.Warn("规则反序列化失败", "key", it.Key, "error", err)
			continue
		}
		e.rules[r.ID] = &r
	}
	return nil
}

func (e *Engine) persistRule(r Rule) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return e.opts.Store.Put(rulesBucket, r.ID, raw)
}

func (e *Engine) ruleFile(id string) string {
	return filepath.Join(e.opts.DataDir, "watches", id+".json")
}

func (e *Engine) writeFile(r Rule) error {
	dir := filepath.Join(e.opts.DataDir, "watches")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(e.ruleFile(r.ID), raw, 0o600)
}

// syncFiles 热加载 ~/.guanlan/watches/*.json：文件是"插件"形态，改文件即生效。
func (e *Engine) syncFiles() {
	dir := filepath.Join(e.opts.DataDir, "watches")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		info, err := ent.Info()
		if err != nil {
			continue
		}
		e.mu.RLock()
		prev, seen := e.modTime[path]
		e.mu.RUnlock()
		if seen && !info.ModTime().After(prev) {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var r Rule
		if err := json.Unmarshal(raw, &r); err != nil {
			e.log.Warn("规则文件格式非法", "file", path, "error", err)
			continue
		}
		if r.ID == "" {
			r.ID = strings.TrimSuffix(ent.Name(), ".json")
		}
		if err := r.Validate(); err != nil {
			e.log.Warn("规则文件校验失败", "file", path, "error", err)
			continue
		}
		e.mu.Lock()
		e.rules[r.ID] = &r
		e.modTime[path] = info.ModTime()
		e.mu.Unlock()
		_ = e.persistRule(r)
		e.log.Info("热加载 Watch 规则", "id", r.ID, "name", r.Name, "enabled", r.Enabled)
	}
}

func (e *Engine) record(entry RunLog) {
	e.mu.Lock()
	e.runs = append(e.runs, entry)
	if len(e.runs) > maxRunLogs {
		e.runs = e.runs[len(e.runs)-maxRunLogs:]
	}
	e.mu.Unlock()
	raw, _ := json.Marshal(entry)
	_ = e.opts.Store.Put(runsBucket, fmt.Sprintf("%020d", e.now().UnixNano()), raw)
}

func (e *Engine) setError(id, msg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.rules[id]; ok {
		r.LastError = msg
		if msg == "" {
			r.TriggerCount++
			r.LastTriggeredAt = e.now().Format(time.RFC3339)
		}
	}
}

func (e *Engine) audit(plugin, action, target string, ok bool, detail string) {
	if e.opts.Audit == nil {
		return
	}
	e.opts.Audit.Record(contract.AuditEvent{Time: e.now(), Plugin: plugin, Kind: "watch", Action: action, OK: ok, Detail: target + " " + detail})
}

func (e *Engine) publish(topic string, payload any) {
	if e.opts.Events == nil {
		return
	}
	raw, _ := json.Marshal(payload)
	e.opts.Events.Publish(plugin.Event{Topic: topic, Time: e.now(), Payload: raw})
}

var (
	sectorCacheMu sync.RWMutex
	sectorCache   = map[string]cachedSectors{}
)

type cachedSectors struct {
	items []contract.Sector
	at    time.Time
}

func (e *Engine) cachedSectors(kind string) ([]contract.Sector, bool) {
	sectorCacheMu.RLock()
	defer sectorCacheMu.RUnlock()
	c, ok := sectorCache[kind]
	if !ok || e.now().Sub(c.at) > 60*time.Second {
		return nil, false
	}
	return c.items, true
}

func (e *Engine) cacheSectors(kind string, items []contract.Sector) {
	sectorCacheMu.Lock()
	sectorCache[kind] = cachedSectors{items: items, at: e.now()}
	sectorCacheMu.Unlock()
}

// ---------- 工具函数 ----------

func mergeMetrics(a, b map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func sectorOf(r Rule) string {
	for _, s := range r.Sources {
		if s.Type == SourceSector {
			return s.Sector
		}
	}
	return ""
}

// isTradingTime 判断 A 股连续竞价时段（含集合竞价与尾盘，不含午休）。
func isTradingTime(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	return (m >= 9*60+15 && m <= 11*60+30) || (m >= 13*60 && m <= 15*60+5)
}

func avgVolume(bars []contract.Bar, n int) float64 {
	if len(bars) < n || n <= 0 {
		return 0
	}
	var sum float64
	for _, b := range bars[len(bars)-n:] {
		sum += b.Volume
	}
	return sum / float64(n)
}

func lastValid(s []float64) float64 {
	for i := len(s) - 1; i >= 0; i-- {
		if v := s[i]; v == v {
			return v
		}
	}
	return 0
}

func round2(v float64) float64 { return float64(int64(v*100+sign(v)*0.5)) / 100 }
func round4(v float64) float64 { return float64(int64(v*10000+sign(v)*0.5)) / 10000 }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// buildSummary 生成给 Agent 与面板看的一句话摘要。
func buildSummary(r Rule, scenarios []Scenario, shared map[string]float64) string {
	parts := make([]string, 0, 3)
	for _, s := range scenarios {
		if !s.Matched {
			continue
		}
		if s.Code != "" {
			parts = append(parts, fmt.Sprintf("%s(%s) 命中：涨跌 %.2f%%、量比 %.2f", s.Name, s.Code, s.Metrics["quote.changePct"], s.Metrics["kline.volumeRatio"]))
		} else if s.Sector != "" {
			parts = append(parts, fmt.Sprintf("板块 %s 命中：涨跌 %.2f%%、主力净额 %.2f 亿", s.Sector, shared["sector.changePct"], shared["sector.mainNet"]/1e8))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "条件命中")
	}
	return strings.Join(parts, "；")
}

// renderPrompt 用指标替换 {metric} 占位，并补 {summary}/{code}/{sector}/{keyword}。
func renderPrompt(tpl string, scenarios []Scenario, shared map[string]float64) string {
	if tpl == "" {
		tpl = "请根据以下信号做一次简短判断：{summary}"
	}
	values := mergeMetrics(shared, map[string]float64{})
	var code, name string
	for _, s := range scenarios {
		if s.Matched {
			code, name = s.Code, s.Name
			values = mergeMetrics(values, s.Metrics)
			break
		}
	}
	out := tpl
	for k, v := range values {
		out = strings.ReplaceAll(out, "{"+k+"}", fmt.Sprintf("%.2f", v))
	}
	out = strings.ReplaceAll(out, "{summary}", buildSummaryFrom(scenarios, shared))
	out = strings.ReplaceAll(out, "{code}", code)
	out = strings.ReplaceAll(out, "{name}", name)
	out = strings.ReplaceAll(out, "{sector}", sectorFromScenarios(scenarios, shared))
	return out
}

func buildSummaryFrom(scenarios []Scenario, shared map[string]float64) string {
	return buildSummary(Rule{}, scenarios, shared)
}

func sectorFromScenarios(scenarios []Scenario, shared map[string]float64) string {
	for _, s := range scenarios {
		if s.Sector != "" {
			return s.Sector
		}
	}
	if v, ok := shared["sector.changePct"]; ok {
		_ = v
	}
	return ""
}

// sortScenarios 稳定排序，保证测试与展示可复现。
func sortScenarios(list []Scenario) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].Code < list[j].Code })
}
