package watchlist

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// fakeData 实现 contract.DataService，测试只用得到自选与行情两个方法。
type fakeData struct {
	mu         sync.Mutex
	items      []contract.WatchItem
	quotes     map[string]contract.Quote
	syncResult contract.SyncResult
	quoteCalls int
	listErr    error
	syncErr    error
	searchRefs []contract.StockRef

	// 同步诊断扩展（syncDiagnoser）
	check       contract.SyncCheck
	checkErr    error
	saveCount   int
	saveErr     error
	savedCookie string
	capture     string
	captureSrc  string
	captureErr  error
}

func (f *fakeData) Quote(_ context.Context, codes []string) ([]contract.Quote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quoteCalls++
	out := make([]contract.Quote, 0, len(codes))
	for _, code := range codes {
		if quote, ok := f.quotes[code]; ok {
			out = append(out, quote)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("没有行情")
	}
	return out, nil
}

func (f *fakeData) Bars(context.Context, string, contract.Period, int, contract.Adjust) ([]contract.Bar, error) {
	return nil, errors.New("未实现")
}

func (f *fakeData) StockFundFlow(context.Context, string) (contract.FundFlow, error) {
	return contract.FundFlow{}, errors.New("未实现")
}

func (f *fakeData) FundFlowRank(context.Context, string, int) ([]contract.FundFlow, error) {
	return nil, errors.New("未实现")
}

func (f *fakeData) Sectors(context.Context, string) ([]contract.Sector, error) {
	return nil, errors.New("未实现")
}

func (f *fakeData) Watchlist(context.Context) ([]contract.WatchItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]contract.WatchItem{}, f.items...), nil
}

func (f *fakeData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncResult, f.syncErr
}

func (f *fakeData) Search(context.Context, string) ([]contract.StockRef, error) {
	return f.searchRefs, nil
}

// 以下四个方法让 fakeData 满足插件内部的 syncDiagnoser（结构化断言，不需要 import datasource）。

func (f *fakeData) SyncCheck(context.Context) (contract.SyncCheck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkErr != nil {
		return contract.SyncCheck{}, f.checkErr
	}
	return f.check, nil
}

func (f *fakeData) VerifyTHSCookie(context.Context, string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saveCount, f.saveErr
}

func (f *fakeData) SaveTHSCookie(_ context.Context, cookie string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.savedCookie = cookie
	if f.saveErr != nil {
		return 0, f.saveErr
	}
	return f.saveCount, nil
}

func (f *fakeData) CaptureBrowserTHSCookie() (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.capture, f.captureSrc, f.captureErr
}

func (f *fakeData) Health(context.Context) map[string]string { return map[string]string{} }

// memStore 是内存版 contract.Store。
type memStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.data[bucket]; ok {
		if v, ok := b[key]; ok {
			return v, nil
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
	s.data[bucket][key] = append([]byte{}, val...)
	return nil
}

func (s *memStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}

func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.KVItem, 0, 8)
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, contract.KVItem{Key: k, Value: v})
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Key < out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (s *memStore) Close() error { return nil }

// newTestContext 组装一个真实的插件 Context（除 Data/Store 外都是 kernel 提供的同款实现）。
func newTestContext(t *testing.T, data contract.DataService, store contract.Store) *plugin.Context {
	t.Helper()
	return &plugin.Context{
		ID:       pluginID,
		Manifest: ManifestForTest(),
		Log:      testLogger(t),
		Clock:    time.Now,
		Tools:    plugin.NewToolRegistry(pluginID),
		UI:       plugin.NewUIRegistry(pluginID),
		Methods:  plugin.NewMethodRegistry(pluginID),
		Services: plugin.NewServiceRegistry(),
		Events:   plugin.NewBus(),
		Store:    store,
		Data:     data,
		Policy:   contract.Policy{},
	}
}

func ManifestForTest() plugin.Manifest { return (&Plugin{}).Manifest() }

func testLogger(t *testing.T) *slogLogger { return newSlogLogger(t) }

func TestManifestAndActivate(t *testing.T) {
	p := New()
	if p.Manifest().ID != "watchlist" {
		t.Fatalf("插件 ID = %q", p.Manifest().ID)
	}
	ctx := newTestContext(t, &fakeData{}, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	methods := ctx.Methods.List()
	if len(methods) != 9 {
		t.Fatalf("方法数 = %d，期望 9", len(methods))
	}
	for _, want := range []string{"list", "sync", "add", "remove", "groups", "check", "saveCookie", "captureCookie", "privacy"} {
		if _, ok := ctx.Methods.Get(want); !ok {
			t.Fatalf("缺少方法 %q", want)
		}
	}
	tools := ctx.Tools.List()
	if len(tools) != 4 {
		t.Fatalf("工具数 = %d，期望 4", len(tools))
	}
	for _, tool := range tools {
		if runes := []rune(tool.Summary); len(runes) > 40 {
			t.Fatalf("工具 %s 的 Summary 超过 40 字（%d）", tool.Name, len(runes))
		}
		if len(tool.Tags) == 0 {
			t.Fatalf("工具 %s 缺少 Tags", tool.Name)
		}
		if !json.Valid(tool.Schema) {
			t.Fatalf("工具 %s 的 Schema 不是合法 JSON", tool.Name)
		}
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(tool.Schema, &schema); err != nil || schema.Type != "object" {
			t.Fatalf("工具 %s 的 Schema 必须是 object", tool.Name)
		}
		if tool.Risk != plugin.RiskRead && tool.Risk != plugin.RiskNetwork {
			t.Fatalf("工具 %s 风险等级不实: %s", tool.Name, tool.Risk)
		}
	}
	slots := ctx.UI.List()
	if len(slots) != 1 {
		t.Fatalf("UI slot 数 = %d，期望 1", len(slots))
	}
	if slots[0].Component != "watchlist" || slots[0].Order != 10 || slots[0].Group != "行情" {
		t.Fatalf("UI slot = %+v", slots[0])
	}
}

func TestListEnrichesNames(t *testing.T) {
	data := &fakeData{
		items: []contract.WatchItem{
			{Code: "600000", Market: contract.MarketSH, Group: "自选股", Order: 1},
			{Code: "000001", Market: contract.MarketSZ, Group: "自选股", Order: 2, Name: "平安银行"},
		},
		quotes: map[string]contract.Quote{"sh600000": {Code: "600000", Market: contract.MarketSH, Name: "浦发银行"}},
	}
	p := New()
	ctx := newTestContext(t, data, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	method, ok := ctx.Methods.Get("list")
	if !ok {
		t.Fatal("缺少 list 方法")
	}
	out, err := method.Fn(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	reply := out.(listReply)
	if reply.Items[0].Name != "浦发银行" {
		t.Fatalf("缺名字的条目应被补齐，实际 %+v", reply.Items[0])
	}
	if reply.Items[1].Name != "平安银行" {
		t.Fatalf("已有名字不应被覆盖，实际 %+v", reply.Items[1])
	}
	if len(reply.Groups) != 1 || reply.Groups[0] != "自选股" {
		t.Fatalf("分组 = %v", reply.Groups)
	}
}

func TestListEnrichesIndexName(t *testing.T) {
	// 上证指数与平安银行同为 000001，补名字时必须按「市场+代码」区分
	data := &fakeData{
		items: []contract.WatchItem{
			{Code: "000001", Market: contract.MarketSH, Group: "指数", Order: 1},
			{Code: "000001", Market: contract.MarketSZ, Group: "个股", Order: 2},
		},
		quotes: map[string]contract.Quote{
			"sh000001": {Code: "000001", Market: contract.MarketSH, Name: "上证指数"},
			"sz000001": {Code: "000001", Market: contract.MarketSZ, Name: "平安银行"},
		},
	}
	p := New()
	ctx := newTestContext(t, data, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := ctx.Methods.Get("list")
	out, err := list.Fn(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	items := out.(listReply).Items
	for _, item := range items {
		switch item.Market {
		case contract.MarketSH:
			if item.Name != "上证指数" {
				t.Fatalf("指数名称 = %q，期望 上证指数", item.Name)
			}
		case contract.MarketSZ:
			if item.Name != "平安银行" {
				t.Fatalf("个股名称 = %q，期望 平安银行", item.Name)
			}
		}
	}
}

func TestAddAndRemove(t *testing.T) {
	data := &fakeData{items: []contract.WatchItem{}}
	store := newMemStore()
	p := New()
	ctx := newTestContext(t, data, store)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}

	add, _ := ctx.Methods.Get("add")
	out, err := add.Fn(context.Background(), json.RawMessage(`{"code":"sh600000","name":"浦发银行","group":"自选股"}`))
	if err != nil {
		t.Fatalf("add 失败: %v", err)
	}
	items := out.(addReply).Items
	if len(items) != 1 || items[0].Code != "600000" || items[0].Market != contract.MarketSH {
		t.Fatalf("add 结果 = %+v", items)
	}
	// 已经落盘
	stored, err := store.List(bucketWatchlist, prefixWatchItem)
	if err != nil || len(stored) != 1 {
		t.Fatalf("store 条目 = %d，err=%v", len(stored), err)
	}
	// 重复添加不产生第二条
	out, err = add.Fn(context.Background(), json.RawMessage(`{"code":"600000"}`))
	if err != nil {
		t.Fatal(err)
	}
	if items = out.(addReply).Items; len(items) != 1 {
		t.Fatalf("重复添加后条目数 = %d，期望 1", len(items))
	}

	remove, _ := ctx.Methods.Get("remove")
	out, err = remove.Fn(context.Background(), json.RawMessage(`{"code":"600000"}`))
	if err != nil {
		t.Fatalf("remove 失败: %v", err)
	}
	if items = out.(addReply).Items; len(items) != 0 {
		t.Fatalf("移除后条目数 = %d，期望 0", len(items))
	}
	stored, _ = store.List(bucketWatchlist, prefixWatchItem)
	if len(stored) != 0 {
		t.Fatalf("移除后 store 仍残留 %d 条", len(stored))
	}
}

func TestAddRejectsBadCode(t *testing.T) {
	p := New()
	ctx := newTestContext(t, &fakeData{}, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	add, _ := ctx.Methods.Get("add")
	for _, payload := range []string{`{"code":""}`, `{"code":"abc"}`, `{"code":"60000"}`} {
		if _, err := add.Fn(context.Background(), json.RawMessage(payload)); err == nil {
			t.Fatalf("%s 应报错", payload)
		}
	}
}

func TestSyncPublishesEvent(t *testing.T) {
	data := &fakeData{syncResult: contract.SyncResult{Success: true, Source: "ths-local", Added: 3, Groups: []string{"自选股"}}}
	p := New()
	ctx := newTestContext(t, data, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	got := make(chan plugin.Event, 1)
	unsubscribe := ctx.Events.Subscribe("watch.updated", func(e plugin.Event) { got <- e })
	defer unsubscribe()

	syncMethod, _ := ctx.Methods.Get("sync")
	out, err := syncMethod.Fn(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("sync 失败: %v", err)
	}
	if result := out.(contract.SyncResult); !result.Success {
		t.Fatalf("同步结果 = %+v", result)
	}
	select {
	case event := <-got:
		if len(event.Payload) == 0 {
			t.Fatal("事件载荷为空")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 watch.updated 事件")
	}
}

func TestToolsQuoteAndSearch(t *testing.T) {
	data := &fakeData{
		quotes:     map[string]contract.Quote{"600000": {Code: "600000", Market: contract.MarketSH, Name: "浦发银行", Price: 10.2}},
		searchRefs: []contract.StockRef{{Code: "600000", Name: "浦发银行", Market: contract.MarketSH}},
	}
	p := New()
	ctx := newTestContext(t, data, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	quoteTool, _ := ctx.Tools.Get("market.quote")
	out, err := quoteTool.Run(context.Background(), json.RawMessage(`{"codes":["600000"]}`))
	if err != nil {
		t.Fatalf("market.quote 失败: %v", err)
	}
	quotes := out.([]contract.Quote)
	if len(quotes) != 1 || quotes[0].Price != 10.2 {
		t.Fatalf("行情结果 = %+v", quotes)
	}
	if _, err := quoteTool.Run(context.Background(), json.RawMessage(`{"codes":[]}`)); err == nil {
		t.Fatal("空 codes 应报错")
	}

	searchTool, _ := ctx.Tools.Get("market.search")
	out, err = searchTool.Run(context.Background(), json.RawMessage(`{"keyword":"浦发"}`))
	if err != nil {
		t.Fatalf("market.search 失败: %v", err)
	}
	refs := out.([]contract.StockRef)
	if len(refs) != 1 || refs[0].Code != "600000" {
		t.Fatalf("搜索结果 = %+v", refs)
	}
	if _, err := searchTool.Run(context.Background(), json.RawMessage(`{"keyword":" "}`)); err == nil {
		t.Fatal("空关键词应报错")
	}
}

func TestListPropagatesDataError(t *testing.T) {
	p := New()
	ctx := newTestContext(t, &fakeData{listErr: errors.New("磁盘炸了")}, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := ctx.Methods.Get("list")
	if _, err := list.Fn(context.Background(), json.RawMessage("{}")); err == nil {
		t.Fatal("数据层报错时应向上返回")
	}
}
