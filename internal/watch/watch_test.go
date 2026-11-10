package watch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// ---------- 测试替身 ----------

type memStore struct {
	mu   sync.RWMutex
	data map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.data[bucket]; ok {
		if v, ok := b[key]; ok {
			return append([]byte(nil), v...), nil
		}
	}
	return nil, contract.ErrNotFound
}
func (s *memStore) Put(bucket, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = append([]byte(nil), val...)
	return nil
}
func (s *memStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}
func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []contract.KVItem
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: append([]byte(nil), v...)})
		}
	}
	return out, nil
}
func (s *memStore) Close() error { return nil }

type fakeData struct{ quotePrice float64 }

func (f fakeData) Quote(_ context.Context, codes []string) ([]contract.Quote, error) {
	out := make([]contract.Quote, 0, len(codes))
	for _, c := range codes {
		out = append(out, contract.Quote{Code: c, Name: "测试股", Market: contract.MarketSH, Price: f.quotePrice, ChangePct: 6.5, Amount: 1e9, Volume: 200000})
	}
	return out, nil
}
func (f fakeData) Bars(_ context.Context, code string, _ contract.Period, limit int, _ contract.Adjust) ([]contract.Bar, error) {
	if limit > 130 {
		limit = 130
	}
	bars := make([]contract.Bar, 0, limit)
	price := 10.0
	for i := 0; i < limit; i++ {
		price += 0.02
		vol := 100000.0
		if i == limit-1 {
			vol = 300000 // 放量
		}
		bars = append(bars, contract.Bar{Date: "2026-01-01", Open: price * 0.99, High: price * 1.01, Low: price * 0.98, Close: price, Volume: vol})
	}
	return bars, nil
}
func (f fakeData) StockFundFlow(context.Context, string) (contract.FundFlow, error) {
	return contract.FundFlow{MainNet: 3.2e8, MainPct: 12.5}, nil
}
func (f fakeData) FundFlowRank(_ context.Context, kind string, limit int) ([]contract.FundFlow, error) {
	return []contract.FundFlow{{Name: "半导体", Kind: kind, MainNet: 3.5e9, MainPct: 4.2}}, nil
}
func (f fakeData) Sectors(_ context.Context, kind string) ([]contract.Sector, error) {
	return []contract.Sector{{Name: "半导体", Kind: kind, ChangePct: 2.8, MainNet: 3.5e9}}, nil
}
func (f fakeData) Watchlist(context.Context) ([]contract.WatchItem, error) { return nil, nil }
func (f fakeData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	return contract.SyncResult{}, nil
}
func (f fakeData) Search(context.Context, string) ([]contract.StockRef, error) { return nil, nil }
func (f fakeData) Health(context.Context) map[string]string                    { return map[string]string{"fake": "ok"} }

type okGuard struct{}

func (okGuard) AllowHost(string) bool   { return true }
func (okGuard) CheckPath(string) error  { return nil }
func (okGuard) Redact(s string) string  { return s }
func (okGuard) Policy() contract.Policy { return contract.Policy{AllowExec: false} }

type noAudit struct{}

func (noAudit) Record(contract.AuditEvent)              {}
func (noAudit) List(int) ([]contract.AuditEvent, error) { return nil, nil }

func newTestEngine(t *testing.T, trigger TriggerFunc) (*Engine, *memStore) {
	t.Helper()
	store := newMemStore()
	dir := t.TempDir()
	e, err := New(Options{
		Store:   store,
		Data:    fakeData{quotePrice: 12.3},
		Guard:   okGuard{},
		Audit:   noAudit{},
		Events:  plugin.NewBus(),
		Trigger: trigger,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataDir: dir,
		Clock:   func() time.Time { return time.Date(2026, 11, 10, 10, 0, 0, 0, time.Local) },
	})
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}
	return e, store
}

// ---------- 规则校验 ----------

func TestRuleValidation(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		want string
	}{
		{"空名称", Rule{Sources: []Source{{Type: SourceSchedule, Times: []string{"09:20"}}}}, "名称"},
		{"无触发源", Rule{Name: "x"}, "触发源"},
		{"非法代码", Rule{Name: "x", Sources: []Source{{Type: SourceQuote, Codes: []string{"abc"}}}}, "非法股票代码"},
		{"板块缺名", Rule{Name: "x", Sources: []Source{{Type: SourceSector}}}, "sector"},
		{"新闻缺词", Rule{Name: "x", Sources: []Source{{Type: SourceNews}}}, "keyword"},
		{"定时非法", Rule{Name: "x", Sources: []Source{{Type: SourceSchedule, Times: []string{"9点"}}}}, "非法时间"},
		{"条件语法错", Rule{Name: "x", Sources: []Source{{Type: SourceSchedule, Times: []string{"09:20"}}}, Condition: "a >>> b"}, "条件表达式"},
		{"未定义指标", Rule{Name: "x", Sources: []Source{{Type: SourceSchedule, Times: []string{"09:20"}}}, Condition: "foo.bar > 1"}, "未定义的指标"},
		{"未知 worker", Rule{Name: "x", Sources: []Source{{Type: SourceSchedule, Times: []string{"09:20"}}}, Action: Action{Worker: "hack"}}, "未知 worker"},
		{"冷却过短", Rule{Name: "x", Sources: []Source{{Type: SourceSchedule, Times: []string{"09:20"}}}, CooldownSeconds: 5}, "冷却"},
		{"静默窗口非法", Rule{Name: "x", Sources: []Source{{Type: SourceSchedule, Times: []string{"09:20"}}}, QuietWindows: []string{"11:30"}}, "静默时段"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.rule.Validate()
			if err == nil {
				t.Fatalf("应当校验失败")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息 = %q, 期望包含 %q", err.Error(), c.want)
			}
		})
	}

	ok := Rule{
		Name:      "合法规则",
		Sources:   []Source{{Type: SourceSector, Sector: "半导体"}, {Type: SourceQuote, Codes: []string{"600000"}}},
		Condition: "sector.mainNetYi > 20 and quote.changePct > 3",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法规则不应失败: %v", err)
	}
}

// ---------- 表达式 ----------

func TestExprEval(t *testing.T) {
	ctx := map[string]float64{"quote.changePct": 6.5, "kline.rsi": 28, "news.hits": 2, "kline.aboveMA20": 1}
	cases := map[string]bool{
		"quote.changePct > 5":                          true,
		"quote.changePct >= 6.5":                       true,
		"quote.changePct < 5":                          false,
		"quote.changePct > 5 and kline.rsi < 30":       true,
		"quote.changePct > 7 or kline.rsi < 30":        true,
		"not quote.changePct > 7":                      true,
		"(quote.changePct > 5 && kline.aboveMA20 > 0)": true,
		"news.hits > 0 and not news.hits > 5":          true,
		"missing.metric > 1":                           false,
		"true":                                         true,
		"kline.aboveMA20":                              true,
	}
	for expr, want := range cases {
		tree, err := Parse(expr)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", expr, err)
		}
		got, err := Eval(tree, ctx)
		if err != nil {
			t.Fatalf("求值 %q 失败: %v", expr, err)
		}
		if got != want {
			t.Fatalf("%q = %v, want %v", expr, got, want)
		}
	}
	if _, err := Parse("quote.changePct > "); err == nil {
		t.Fatal("缺少右操作数应报错")
	}
	if _, err := Parse("quote.changePct $ 3"); err == nil {
		t.Fatal("非法字符应报错")
	}
}

// ---------- 引擎 ----------

func TestEvaluateScenarioMatches(t *testing.T) {
	e, _ := newTestEngine(t, nil)
	rule := Rule{
		Name:      "放量上涨",
		Sources:   []Source{{Type: SourceQuote, Codes: []string{"600000"}}, {Type: SourceSector, Sector: "半导体"}},
		Condition: "quote.changePct > 5 and kline.rsi > 0 and sector.mainNetYi > 20",
		Action:    Action{Worker: "intraday", Prompt: "涨 {quote.changePct}%，板块 {sector}"},
	}
	saved, err := e.Save(rule, "test")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	res, err := e.Test(context.Background(), saved.ID)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	if !res.Matched {
		t.Fatalf("应命中: %+v", res)
	}
	if len(res.Scenarios) != 1 || res.Scenarios[0].Code != "600000" {
		t.Fatalf("场景不对: %+v", res.Scenarios)
	}
	if res.Scenarios[0].Metrics["quote.changePct"] != 6.5 {
		t.Fatalf("涨跌幅指标不对: %+v", res.Scenarios[0].Metrics)
	}
	if !strings.Contains(res.Prompt, "6.50") {
		t.Fatalf("prompt 未替换指标: %q", res.Prompt)
	}
	if res.Scenarios[0].Metrics["sector.mainNetYi"] <= 20 {
		t.Fatalf("板块指标未合并: %+v", res.Scenarios[0].Metrics)
	}
}

func TestFireRespectsCooldownAndRecordsLog(t *testing.T) {
	var calls int32
	e, _ := newTestEngine(t, func(context.Context, string, string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "已分析", nil
	})
	rule := Rule{
		Name:            "冷却测试",
		Sources:         []Source{{Type: SourceQuote, Codes: []string{"600000"}}},
		Condition:       "quote.changePct > 5",
		CooldownSeconds: 300,
		Enabled:         true,
		Action:          Action{Worker: "chat"},
	}
	saved, err := e.Save(rule, "test")
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	e.tick(context.Background())
	// 等待异步触发完成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&calls) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("应触发一次, got %d", calls)
	}
	e.tick(context.Background())
	time.Sleep(80 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("冷却期内不应重复触发, got %d", calls)
	}
	// 清掉冷却后再触发一次
	e.mu.Lock()
	e.lastFired[saved.ID] = time.Time{}
	e.mu.Unlock()
	e.tick(context.Background())
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&calls) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("冷却结束后应再次触发, got %d", calls)
	}
	logs := e.Logs(10)
	if len(logs) < 2 || !logs[0].Triggered {
		t.Fatalf("应记录触发日志: %+v", logs)
	}
}

func TestDisabledRuleDoesNotFire(t *testing.T) {
	var calls int32
	e, _ := newTestEngine(t, func(context.Context, string, string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", nil
	})
	if _, err := e.Save(Rule{
		Name: "关闭的规则", Sources: []Source{{Type: SourceQuote, Codes: []string{"600000"}}},
		Condition: "quote.changePct > 1", Enabled: false,
	}, "test"); err != nil {
		t.Fatal(err)
	}
	e.tick(context.Background())
	time.Sleep(60 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("关闭的规则不应触发")
	}
}

func TestQuietWindowSkips(t *testing.T) {
	var calls int32
	e, _ := newTestEngine(t, func(context.Context, string, string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "", nil
	})
	// 引擎时钟固定在 10:00，设置 09:00-11:00 静默
	if _, err := e.Save(Rule{
		Name: "静默", Sources: []Source{{Type: SourceQuote, Codes: []string{"600000"}}},
		Condition: "quote.changePct > 1", Enabled: true, QuietWindows: []string{"09:00-11:00"},
	}, "test"); err != nil {
		t.Fatal(err)
	}
	e.tick(context.Background())
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("静默时段不应触发")
	}
}

func TestFileHotReload(t *testing.T) {
	e, _ := newTestEngine(t, nil)
	dir := filepath.Join(e.opts.DataDir, "watches")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		ID: "w-hot", Name: "文件规则", Enabled: true,
		Sources:   []Source{{Type: SourceSchedule, Times: []string{"09:30"}}},
		Condition: "time.trading > 0",
	}
	raw, _ := json.Marshal(rule)
	if err := os.WriteFile(filepath.Join(dir, "w-hot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e.syncFiles()
	got, ok := e.Get("w-hot")
	if !ok {
		t.Fatal("热加载未生效")
	}
	if !got.Enabled || got.Name != "文件规则" {
		t.Fatalf("热加载内容不对: %+v", got)
	}
	// 关闭文件里的规则后再热加载，应跟随
	rule.Enabled = false
	raw, _ = json.Marshal(rule)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "w-hot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(filepath.Join(dir, "w-hot.json"), time.Now().Add(time.Second), time.Now().Add(time.Second))
	e.syncFiles()
	got, _ = e.Get("w-hot")
	if got.Enabled {
		t.Fatalf("文件改动未生效: %+v", got)
	}
}

func TestToggleAndDelete(t *testing.T) {
	e, _ := newTestEngine(t, nil)
	saved, err := e.Save(Rule{
		Name: "开关测试", Sources: []Source{{Type: SourceSchedule, Times: []string{"09:20"}}}, Enabled: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	off, err := e.Toggle(saved.ID, false)
	if err != nil || off.Enabled {
		t.Fatalf("关闭失败: %+v %v", off, err)
	}
	if _, err := e.Toggle("missing", true); err == nil {
		t.Fatal("不存在的规则应报错")
	}
	if err := e.Delete(saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Get(saved.ID); ok {
		t.Fatal("删除后仍能读到")
	}
	if err := e.Delete(saved.ID); err == nil {
		t.Fatal("重复删除应报错")
	}
}

func TestTriggerErrorIsRecorded(t *testing.T) {
	e, _ := newTestEngine(t, func(context.Context, string, string) (string, error) {
		return "", errors.New("模型未配置")
	})
	saved, err := e.Save(Rule{
		Name: "错误记录", Sources: []Source{{Type: SourceQuote, Codes: []string{"600000"}}},
		Condition: "quote.changePct > 1", Enabled: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	e.tick(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	var logs []RunLog
	for time.Now().Before(deadline) {
		logs = e.Logs(5)
		if len(logs) > 0 && logs[0].Error != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(logs) == 0 || !strings.Contains(logs[0].Error, "模型未配置") {
		t.Fatalf("应记录触发错误: %+v", logs)
	}
	got, _ := e.Get(saved.ID)
	if !strings.Contains(got.LastError, "模型未配置") {
		t.Fatalf("规则应带 LastError: %+v", got)
	}
}

func TestTradingTime(t *testing.T) {
	loc := time.Local
	cases := []struct {
		t    time.Time
		want bool
	}{
		{time.Date(2026, 11, 10, 9, 14, 0, 0, loc), false},
		{time.Date(2026, 11, 10, 9, 20, 0, 0, loc), true},
		{time.Date(2026, 11, 10, 12, 0, 0, 0, loc), false},
		{time.Date(2026, 11, 10, 14, 59, 0, 0, loc), true},
		{time.Date(2026, 11, 10, 15, 10, 0, 0, loc), false},
		{time.Date(2026, 11, 7, 10, 0, 0, 0, loc), false}, // 周六
	}
	for _, c := range cases {
		if got := isTradingTime(c.t); got != c.want {
			t.Fatalf("isTradingTime(%s) = %v, want %v", c.t, got, c.want)
		}
	}
}
