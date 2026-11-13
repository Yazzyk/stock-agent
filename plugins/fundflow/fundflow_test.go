package fundflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// fakeData 记录收到的参数并返回预置结果。
type fakeData struct {
	mu            sync.Mutex
	flow          contract.FundFlow
	rank          []contract.FundFlow
	sectors       []contract.Sector
	lastKind      string
	lastLimit     int
	lastDirection string
	flowErr       error
	rankErr       error
	sectorsErr    error

	// 多日资金流 / 批量快照扩展
	history     contract.FundFlowHistory
	historyErr  error
	lastDays    int
	snapshot    []contract.FundFlow
	snapshotErr error
	lastCodes   []string

	// 板块/概念多日资金流扩展
	sectorHistory   contract.FundFlowHistory
	sectorHistories []contract.FundFlowHistory
	sectorErr       error
	lastSectorCode  string
	lastSectorCodes []string
	lastSectorDays  int
}

func (f *fakeData) Quote(context.Context, []string) ([]contract.Quote, error) {
	return nil, errors.New("未实现")
}

func (f *fakeData) Bars(context.Context, string, contract.Period, int, contract.Adjust) ([]contract.Bar, error) {
	return nil, errors.New("未实现")
}

func (f *fakeData) StockFundFlow(_ context.Context, code string) (contract.FundFlow, error) {
	if f.flowErr != nil {
		return contract.FundFlow{}, f.flowErr
	}
	flow := f.flow
	flow.Code = code
	return flow, nil
}

func (f *fakeData) FundFlowRank(_ context.Context, kind string, limit int) ([]contract.FundFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastKind, f.lastLimit = kind, limit
	if f.rankErr != nil {
		return nil, f.rankErr
	}
	return f.rank, nil
}

func (f *fakeData) FundFlowRankBy(_ context.Context, kind, direction string, limit int) ([]contract.FundFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastKind, f.lastDirection, f.lastLimit = kind, direction, limit
	if f.rankErr != nil {
		return nil, f.rankErr
	}
	return f.rank, nil
}

func (f *fakeData) Sectors(_ context.Context, kind string) ([]contract.Sector, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastKind = kind
	if f.sectorsErr != nil {
		return nil, f.sectorsErr
	}
	return f.sectors, nil
}

func (f *fakeData) FundFlowHistory(_ context.Context, code string, days int) (contract.FundFlowHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDays = days
	if f.historyErr != nil {
		return contract.FundFlowHistory{}, f.historyErr
	}
	out := f.history
	out.Code, out.Days = code, days
	return out, nil
}

func (f *fakeData) FundFlowSnapshot(_ context.Context, codes []string) ([]contract.FundFlow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCodes = append([]string{}, codes...)
	return f.snapshot, f.snapshotErr
}

func (f *fakeData) SectorFundFlowHistory(_ context.Context, code string, days int) (contract.FundFlowHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSectorCode, f.lastSectorDays = code, days
	if f.sectorErr != nil {
		return contract.FundFlowHistory{}, f.sectorErr
	}
	out := f.sectorHistory
	out.Code, out.Days = code, days
	return out, nil
}

func (f *fakeData) SectorFundFlowBatch(_ context.Context, codes []string, days int) ([]contract.FundFlowHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSectorCodes = append([]string{}, codes...)
	f.lastSectorDays = days
	return f.sectorHistories, f.sectorErr
}

func (f *fakeData) Watchlist(context.Context) ([]contract.WatchItem, error) { return nil, nil }

func (f *fakeData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	return contract.SyncResult{}, nil
}

func (f *fakeData) Search(context.Context, string) ([]contract.StockRef, error) { return nil, nil }

func (f *fakeData) Health(context.Context) map[string]string { return map[string]string{} }

func newTestContext(t *testing.T, data contract.DataService) *plugin.Context {
	t.Helper()
	return &plugin.Context{
		ID:       pluginID,
		Manifest: (&Plugin{}).Manifest(),
		Log:      slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:    time.Now,
		Tools:    plugin.NewToolRegistry(pluginID),
		UI:       plugin.NewUIRegistry(pluginID),
		Methods:  plugin.NewMethodRegistry(pluginID),
		Services: plugin.NewServiceRegistry(),
		Events:   plugin.NewBus(),
		Data:     data,
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func TestManifestAndActivate(t *testing.T) {
	p := New()
	if p.Manifest().ID != "fundflow" {
		t.Fatalf("插件 ID = %q", p.Manifest().ID)
	}
	ctx := newTestContext(t, &fakeData{})
	if err := p.Activate(ctx); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	if len(ctx.Methods.List()) != 7 {
		t.Fatalf("方法数 = %d，期望 7", len(ctx.Methods.List()))
	}
	for _, want := range []string{"stock", "rank", "sectors", "history", "batch", "sectorHistory", "sectorBatch"} {
		if _, ok := ctx.Methods.Get(want); !ok {
			t.Fatalf("缺少方法 %q", want)
		}
	}
	tools := ctx.Tools.List()
	if len(tools) != 5 {
		t.Fatalf("工具数 = %d，期望 5", len(tools))
	}
	for _, tool := range tools {
		if runes := []rune(tool.Summary); len(runes) > 40 {
			t.Fatalf("工具 %s 的 Summary 超过 40 字（%d）", tool.Name, len(runes))
		}
		if len(tool.Tags) == 0 || !json.Valid(tool.Schema) {
			t.Fatalf("工具 %s 元数据不完整", tool.Name)
		}
		if tool.Name == "flow.rank" && !strings.Contains(string(tool.Schema), `"direction"`) {
			t.Fatal("flow.rank 的 schema 应包含 direction 枚举")
		}
		if tool.Risk != plugin.RiskNetwork && tool.Risk != plugin.RiskRead {
			t.Fatalf("工具 %s 风险等级不实: %s", tool.Name, tool.Risk)
		}
	}
	slots := ctx.UI.List()
	if len(slots) != 1 || slots[0].Component != "fundflow" || slots[0].Order != 20 {
		t.Fatalf("UI slot = %+v", slots)
	}
}

func TestRankDefaultsOnEmptyArgs(t *testing.T) {
	data := &fakeData{rank: []contract.FundFlow{{Code: "BK1201", Name: "电子", MainNet: 9.2e9}}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	rank, _ := ctx.Methods.Get("rank")
	// product 要求：空 payload 也不许报错
	if _, err := rank.Fn(context.Background(), json.RawMessage("")); err != nil {
		t.Fatalf("空参数应使用默认值: %v", err)
	}
	if data.lastKind != "sector" || data.lastLimit != 10 {
		t.Fatalf("默认参数 = %q/%d，期望 sector/10", data.lastKind, data.lastLimit)
	}
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("空对象应使用默认值: %v", err)
	}
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{"kind":"concept","limit":5}`)); err != nil {
		t.Fatal(err)
	}
	if data.lastKind != "concept" || data.lastLimit != 5 {
		t.Fatalf("显式参数未生效: %q/%d", data.lastKind, data.lastLimit)
	}
	// limit 上限收敛
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{"limit":9999}`)); err != nil {
		t.Fatal(err)
	}
	if data.lastLimit != maxRankLimit {
		t.Fatalf("limit 上限 = %d，期望 %d", data.lastLimit, maxRankLimit)
	}
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{"kind":"unknown"}`)); err == nil {
		t.Fatal("非法 kind 应报错")
	}
}

// rankLegacyData 是只实现旧 FundFlowRank 的数据层，用于验证插件对旧接口的降级。
type rankLegacyData struct {
	contract.DataService
}

// TestRankDirection 验证 direction 透传、默认值与非法值报错。
func TestRankDirection(t *testing.T) {
	data := &fakeData{rank: []contract.FundFlow{{Code: "BK0438", Name: "酒店餐饮", MainNet: -5.2e9}}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	rank, _ := ctx.Methods.Get("rank")

	if _, err := rank.Fn(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if data.lastDirection != "in" {
		t.Fatalf("默认 direction = %q，期望 in", data.lastDirection)
	}
	out, err := rank.Fn(context.Background(), json.RawMessage(`{"kind":"concept","direction":"out","limit":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if data.lastDirection != "out" || data.lastKind != "concept" || data.lastLimit != 3 {
		t.Fatalf("参数未透传: %q/%q/%d", data.lastKind, data.lastDirection, data.lastLimit)
	}
	if items := out.([]contract.FundFlow); len(items) != 1 || items[0].Code != "BK0438" {
		t.Fatalf("净流出榜 = %+v", items)
	}
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{"direction":"ALL"}`)); err != nil {
		t.Fatalf("direction 应大小写不敏感: %v", err)
	}
	if data.lastDirection != "all" {
		t.Fatalf("direction = %q，期望 all", data.lastDirection)
	}
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{"direction":"sideways"}`)); err == nil {
		t.Fatal("非法 direction 应报错")
	}
}

// TestRankLegacyDataLayer 验证旧数据层：净流入可降级，净流出给出中文提示。
func TestRankLegacyDataLayer(t *testing.T) {
	data := &fakeData{rank: []contract.FundFlow{{Code: "BK1201", Name: "电子"}}}
	p := New()
	ctx := newTestContext(t, rankLegacyData{DataService: data})
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	rank, _ := ctx.Methods.Get("rank")
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{"direction":"in"}`)); err != nil {
		t.Fatalf("旧数据层净流入榜应可用: %v", err)
	}
	if data.lastKind != "sector" || data.lastLimit != 10 {
		t.Fatalf("旧接口参数 = %q/%d", data.lastKind, data.lastLimit)
	}
	if _, err := rank.Fn(context.Background(), json.RawMessage(`{"direction":"out"}`)); err == nil {
		t.Fatal("旧数据层没有净流出榜，应返回可操作错误")
	}
}

// TestRankEmptyResultIsArray 保证没有数据时返回空数组而不是 null。
func TestRankEmptyResultIsArray(t *testing.T) {
	p := New()
	ctx := newTestContext(t, &fakeData{})
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	rank, _ := ctx.Methods.Get("rank")
	out, err := rank.Fn(context.Background(), json.RawMessage(`{"direction":"out"}`))
	if err != nil {
		t.Fatal(err)
	}
	items, ok := out.([]contract.FundFlow)
	if !ok || items == nil {
		t.Fatalf("空结果应为非 nil 数组，实际 %#v", out)
	}
}

func TestSectorsDefaults(t *testing.T) {
	data := &fakeData{sectors: []contract.Sector{{Code: "BK1201", Name: "电子", ChangePct: 3.29}}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	sectors, _ := ctx.Methods.Get("sectors")
	out, err := sectors.Fn(context.Background(), json.RawMessage(""))
	if err != nil {
		t.Fatalf("空参数应使用默认值: %v", err)
	}
	if data.lastKind != "sector" {
		t.Fatalf("默认 kind = %q", data.lastKind)
	}
	if items := out.([]contract.Sector); len(items) != 1 || items[0].Code != "BK1201" {
		t.Fatalf("板块列表 = %+v", items)
	}
}

func TestStockFlowAndErrors(t *testing.T) {
	data := &fakeData{flow: contract.FundFlow{MainNet: 26922753, SuperNet: 9640132, LargeNet: 17282621}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	stock, _ := ctx.Methods.Get("stock")
	out, err := stock.Fn(context.Background(), json.RawMessage(`{"code":"600000"}`))
	if err != nil {
		t.Fatalf("stock 失败: %v", err)
	}
	flow := out.(contract.FundFlow)
	if flow.Code != "600000" || flow.MainNet != flow.LargeNet+flow.SuperNet {
		t.Fatalf("资金流 = %+v", flow)
	}
	if _, err := stock.Fn(context.Background(), json.RawMessage(`{"code":""}`)); err == nil {
		t.Fatal("空代码应报错")
	}

	data.flowErr = errors.New("上游 502")
	if _, err := stock.Fn(context.Background(), json.RawMessage(`{"code":"600000"}`)); err == nil {
		t.Fatal("数据层报错应向上返回")
	}
}

func TestToolWrappers(t *testing.T) {
	data := &fakeData{
		rank:    []contract.FundFlow{{Code: "BK1201", Name: "电子"}},
		sectors: []contract.Sector{{Code: "BK1201", Name: "电子"}},
		flow:    contract.FundFlow{MainNet: 1},
	}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	rankTool, _ := ctx.Tools.Get("flow.rank")
	out, err := rankTool.Run(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	payload := out.(map[string]any)
	if payload["count"].(int) != 1 {
		t.Fatalf("工具返回 = %+v", payload)
	}
	sectorTool, _ := ctx.Tools.Get("sector.list")
	if _, err := sectorTool.Run(context.Background(), json.RawMessage(`{"kind":"concept"}`)); err != nil {
		t.Fatal(err)
	}
	if data.lastKind != "concept" {
		t.Fatalf("工具参数未透传: %q", data.lastKind)
	}
	stockTool, _ := ctx.Tools.Get("flow.stock")
	if _, err := stockTool.Run(context.Background(), json.RawMessage(`{"code":"600000"}`)); err != nil {
		t.Fatal(err)
	}
}

// TestSectorHistoryArgs 覆盖板块多日资金流方法：默认天数、上限收敛、空代码与数据层缺失。
func TestSectorHistoryArgs(t *testing.T) {
	data := &fakeData{sectorHistory: contract.FundFlowHistory{
		Name:   "电子",
		Source: "sina",
		Points: []contract.FundFlowPoint{{Date: "2026-11-10", MainNet: 1e8}},
	}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	sectorHistory, _ := ctx.Methods.Get("sectorHistory")

	out, err := sectorHistory.Fn(context.Background(), json.RawMessage(`{"code":"BK1201"}`))
	if err != nil {
		t.Fatalf("取板块资金流失败: %v", err)
	}
	history := out.(contract.FundFlowHistory)
	if history.Code != "BK1201" || history.Days != defaultHistoryDays || history.Name != "电子" {
		t.Fatalf("返回值 = %+v", history)
	}
	if data.lastSectorDays != defaultHistoryDays {
		t.Fatalf("默认天数未透传: %d", data.lastSectorDays)
	}

	if _, err := sectorHistory.Fn(context.Background(), json.RawMessage(`{"code":"BK1201","days":9999}`)); err != nil {
		t.Fatal(err)
	}
	if data.lastSectorDays != maxHistoryDays {
		t.Fatalf("天数上限 = %d，期望 %d", data.lastSectorDays, maxHistoryDays)
	}
	if _, err := sectorHistory.Fn(context.Background(), json.RawMessage(`{"code":""}`)); err == nil {
		t.Fatal("空代码应报错")
	}
	if _, err := sectorHistory.Fn(context.Background(), json.RawMessage(`{"code":"BK1201","days":5}`)); err != nil {
		t.Fatal(err)
	}
	if data.lastSectorDays != 5 {
		t.Fatalf("显式天数未透传: %d", data.lastSectorDays)
	}
}

// TestSectorBatchArgs 覆盖批量板块资金流：上限 20、去重、缺省天数、空结果为空数组。
func TestSectorBatchArgs(t *testing.T) {
	data := &fakeData{sectorHistories: []contract.FundFlowHistory{{Code: "BK1201", Name: "电子"}}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	sectorBatch, _ := ctx.Methods.Get("sectorBatch")

	out, err := sectorBatch.Fn(context.Background(), json.RawMessage(`{"codes":["BK1201"," BK1201 ","BK1202"],"days":10}`))
	if err != nil {
		t.Fatalf("批量取板块资金流失败: %v", err)
	}
	reply := out.(sectorBatchReply)
	if len(reply.Items) != 1 || reply.Items[0].Code != "BK1201" {
		t.Fatalf("批量返回值 = %+v", reply.Items)
	}
	if len(data.lastSectorCodes) != 2 || data.lastSectorDays != 10 {
		t.Fatalf("去重/天数未透传: %v/%d", data.lastSectorCodes, data.lastSectorDays)
	}

	if _, err := sectorBatch.Fn(context.Background(), json.RawMessage(`{"codes":[]}`)); err == nil {
		t.Fatal("空 codes 应报错")
	}
	many := make([]string, 0, maxSectorBatchCodes+1)
	for i := 0; i <= maxSectorBatchCodes; i++ {
		many = append(many, "BK"+string(rune('A'+i%26))+string(rune('0'+i%10)))
	}
	payload, _ := json.Marshal(map[string]any{"codes": many})
	if _, err := sectorBatch.Fn(context.Background(), payload); err == nil {
		t.Fatal("超过 20 个应报错")
	}

	empty := &fakeData{}
	ctxEmpty := newTestContext(t, empty)
	if err := p.Activate(ctxEmpty); err != nil {
		t.Fatal(err)
	}
	emptyBatch, _ := ctxEmpty.Methods.Get("sectorBatch")
	out, err = emptyBatch.Fn(context.Background(), json.RawMessage(`{"codes":["BK1201"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if items := out.(sectorBatchReply).Items; items == nil || len(items) != 0 {
		t.Fatalf("空结果应为非 nil 空数组，实际 %#v", items)
	}
}

// TestSectorFlowLegacyDataLayer 验证旧数据层（无板块扩展）给出可操作的中文提示。
func TestSectorFlowLegacyDataLayer(t *testing.T) {
	p := New()
	ctx := newTestContext(t, rankLegacyData{DataService: &fakeData{}})
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	sectorHistory, _ := ctx.Methods.Get("sectorHistory")
	if _, err := sectorHistory.Fn(context.Background(), json.RawMessage(`{"code":"BK1201"}`)); err == nil {
		t.Fatal("旧数据层应返回可操作错误")
	} else if !strings.Contains(err.Error(), "不支持板块多日资金流") {
		t.Fatalf("错误文案应说明原因，实际 %v", err)
	}
	sectorBatch, _ := ctx.Methods.Get("sectorBatch")
	if _, err := sectorBatch.Fn(context.Background(), json.RawMessage(`{"codes":["BK1201"]}`)); err == nil {
		t.Fatal("旧数据层批量应返回可操作错误")
	}
}

// TestSectorFlowErrors 验证上游错误的透传与 tool 包装。
func TestSectorFlowErrors(t *testing.T) {
	data := &fakeData{sectorErr: errors.New("上游 502")}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	sectorHistory, _ := ctx.Methods.Get("sectorHistory")
	if _, err := sectorHistory.Fn(context.Background(), json.RawMessage(`{"code":"BK1201"}`)); err == nil {
		t.Fatal("上游报错应向上返回")
	}
	sectorBatch, _ := ctx.Methods.Get("sectorBatch")
	if _, err := sectorBatch.Fn(context.Background(), json.RawMessage(`{"codes":["BK1201"]}`)); err == nil {
		t.Fatal("批量全失败应向上返回错误")
	}

	tool, ok := ctx.Tools.Get("sector.history")
	if !ok {
		t.Fatal("缺少工具 sector.history")
	}
	if tool.Risk != plugin.RiskNetwork {
		t.Fatalf("风险等级 = %s", tool.Risk)
	}
	data.sectorErr = nil
	data.sectorHistory = contract.FundFlowHistory{Name: "电子"}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"code":"BK1201","days":10}`)); err != nil {
		t.Fatalf("工具调用失败: %v", err)
	}
	if data.lastSectorDays != 10 {
		t.Fatalf("工具参数未透传: %d", data.lastSectorDays)
	}
}
