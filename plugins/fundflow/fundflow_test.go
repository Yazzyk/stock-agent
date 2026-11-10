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
	mu         sync.Mutex
	flow       contract.FundFlow
	rank       []contract.FundFlow
	sectors    []contract.Sector
	lastKind   string
	lastLimit  int
	flowErr    error
	rankErr    error
	sectorsErr error
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

func (f *fakeData) Sectors(_ context.Context, kind string) ([]contract.Sector, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastKind = kind
	if f.sectorsErr != nil {
		return nil, f.sectorsErr
	}
	return f.sectors, nil
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
	if len(ctx.Methods.List()) != 3 {
		t.Fatalf("方法数 = %d，期望 3", len(ctx.Methods.List()))
	}
	tools := ctx.Tools.List()
	if len(tools) != 3 {
		t.Fatalf("工具数 = %d，期望 3", len(tools))
	}
	for _, tool := range tools {
		if runes := []rune(tool.Summary); len(runes) > 40 {
			t.Fatalf("工具 %s 的 Summary 超过 40 字（%d）", tool.Name, len(runes))
		}
		if len(tool.Tags) == 0 || !json.Valid(tool.Schema) {
			t.Fatalf("工具 %s 元数据不完整", tool.Name)
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
