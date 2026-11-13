package marketcenter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/datasource"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// plainData 只实现 contract.DataService，用来模拟旧版数据层（没有市场中心扩展）。
type plainData struct{}

func (d *plainData) Quote(context.Context, []string) ([]contract.Quote, error) { return nil, nil }
func (d *plainData) Bars(context.Context, string, contract.Period, int, contract.Adjust) ([]contract.Bar, error) {
	return nil, nil
}
func (d *plainData) StockFundFlow(context.Context, string) (contract.FundFlow, error) {
	return contract.FundFlow{}, nil
}
func (d *plainData) FundFlowRank(context.Context, string, int) ([]contract.FundFlow, error) {
	return nil, nil
}
func (d *plainData) Sectors(context.Context, string) ([]contract.Sector, error) { return nil, nil }
func (d *plainData) Watchlist(context.Context) ([]contract.WatchItem, error)    { return nil, nil }
func (d *plainData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	return contract.SyncResult{}, nil
}
func (d *plainData) Search(context.Context, string) ([]contract.StockRef, error) { return nil, nil }
func (d *plainData) Health(context.Context) map[string]string                    { return map[string]string{} }

// fakeData 记录入参并返回预置结果。
type fakeData struct {
	plainData
	mu sync.Mutex

	indices []datasource.MarketIndex
	breadth datasource.MarketBreadth
	ladder  []datasource.LimitUpStock
	longhu  []datasource.LongHuRow
	hot     []datasource.HotStock
	snap    datasource.MarketSnapshot

	indicesErr error
	breadthErr error
	ladderErr  error
	longhuErr  error
	hotErr     error
	snapErr    error

	lastLadderLimit int
	lastLongHuLimit int
	lastHotType     string
	lastHotLimit    int
	lastSnapLadder  int
	lastSnapLongHu  int
}

func (f *fakeData) MarketIndices(context.Context) ([]datasource.MarketIndex, error) {
	return f.indices, f.indicesErr
}

func (f *fakeData) MarketBreadth(context.Context) (datasource.MarketBreadth, error) {
	return f.breadth, f.breadthErr
}

func (f *fakeData) MarketLadder(_ context.Context, limit int) ([]datasource.LimitUpStock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastLadderLimit = limit
	return f.ladder, f.ladderErr
}

func (f *fakeData) MarketLongHu(_ context.Context, limit int) ([]datasource.LongHuRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastLongHuLimit = limit
	return f.longhu, f.longhuErr
}

func (f *fakeData) MarketHot(_ context.Context, listType string, limit int) ([]datasource.HotStock, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastHotType, f.lastHotLimit = listType, limit
	return f.hot, f.hotErr
}

func (f *fakeData) MarketSnapshot(_ context.Context, ladderLimit, longhuLimit int) (datasource.MarketSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSnapLadder, f.lastSnapLongHu = ladderLimit, longhuLimit
	return f.snap, f.snapErr
}

// newTestContext 构造最小插件上下文。
func newTestContext(t *testing.T, data contract.DataService) *plugin.Context {
	t.Helper()
	return &plugin.Context{
		ID:       pluginID,
		Manifest: (&Plugin{}).Manifest(),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:    time.Now,
		Tools:    plugin.NewToolRegistry(pluginID),
		UI:       plugin.NewUIRegistry(pluginID),
		Methods:  plugin.NewMethodRegistry(pluginID),
		Services: plugin.NewServiceRegistry(),
		Events:   plugin.NewBus(),
		Data:     data,
	}
}

// callMethod 调用已注册方法并返回原始结果。
func callMethod(t *testing.T, c *plugin.Context, name string, payload any) (any, error) {
	t.Helper()
	method, ok := c.Methods.Get(name)
	if !ok {
		t.Fatalf("方法 %s 未注册", name)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化入参失败: %v", err)
	}
	return method.Fn(context.Background(), raw)
}

// TestManifestAndActivate 校验插件身份、贡献点与面板顺序。
func TestManifestAndActivate(t *testing.T) {
	p := New().(*Plugin)
	if err := p.Manifest().Validate(); err != nil {
		t.Fatalf("manifest 校验失败: %v", err)
	}
	manifest := p.Manifest()
	if manifest.ID != "marketcenter" || manifest.Name != "市场中心" {
		t.Fatalf("manifest = %+v", manifest)
	}
	ctx := newTestContext(t, &fakeData{})
	if err := p.Activate(ctx); err != nil {
		t.Fatalf("Activate 失败: %v", err)
	}
	for _, name := range []string{"indices", "breadth", "ladder", "longhu", "snapshot"} {
		if _, ok := ctx.Methods.Get(name); !ok {
			t.Fatalf("方法 %s 未注册", name)
		}
	}
	for _, name := range []string{"market.indices", "market.breadth", "market.ladder", "market.longhu"} {
		tool, ok := ctx.Tools.Get(name)
		if !ok {
			t.Fatalf("工具 %s 未注册", name)
		}
		if tool.Risk != plugin.RiskNetwork {
			t.Fatalf("工具 %s 风险 = %s，期望 network", name, tool.Risk)
		}
		if !strings.Contains(tool.Summary, "只读") {
			t.Fatalf("工具 %s summary 未标注只读: %q", name, tool.Summary)
		}
	}
	slots := ctx.UI.List()
	if len(slots) != 1 || slots[0].ID != "marketcenter" || slots[0].Order != 15 {
		t.Fatalf("UI slot = %+v", slots)
	}
	if slots[0].Icon != "pulse" || slots[0].Group != "行情" || slots[0].Component != "marketcenter" {
		t.Fatalf("UI slot 字段 = %+v", slots[0])
	}
}

// TestLadderLimitBounds 校验 limit 默认值与上限。
func TestLadderLimitBounds(t *testing.T) {
	data := &fakeData{ladder: []datasource.LimitUpStock{{Code: "600776", Boards: 2}}}
	ctx := newTestContext(t, data)
	p := New().(*Plugin)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := callMethod(t, ctx, "ladder", map[string]any{}); err != nil {
		t.Fatalf("默认 limit 调用失败: %v", err)
	}
	if data.lastLadderLimit != defaultLadder {
		t.Fatalf("默认 limit = %d，期望 %d", data.lastLadderLimit, defaultLadder)
	}
	if _, err := callMethod(t, ctx, "ladder", map[string]any{"limit": 999}); err != nil {
		t.Fatalf("上限 limit 调用失败: %v", err)
	}
	if data.lastLadderLimit != maxLadder {
		t.Fatalf("上限 limit = %d，期望 %d", data.lastLadderLimit, maxLadder)
	}
	if _, err := callMethod(t, ctx, "ladder", map[string]any{"limit": -3}); err != nil {
		t.Fatalf("负数 limit 调用失败: %v", err)
	}
	if data.lastLadderLimit != defaultLadder {
		t.Fatalf("负数 limit = %d，期望默认 %d", data.lastLadderLimit, defaultLadder)
	}
}

// TestLongHuLimitBounds 校验龙虎榜 limit 默认值与上限。
func TestLongHuLimitBounds(t *testing.T) {
	data := &fakeData{longhu: []datasource.LongHuRow{{Code: "002384"}}}
	ctx := newTestContext(t, data)
	p := New().(*Plugin)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := callMethod(t, ctx, "longhu", map[string]any{}); err != nil {
		t.Fatalf("默认 limit 调用失败: %v", err)
	}
	if data.lastLongHuLimit != defaultLongHu {
		t.Fatalf("默认 limit = %d，期望 %d", data.lastLongHuLimit, defaultLongHu)
	}
	if _, err := callMethod(t, ctx, "longhu", map[string]any{"limit": 999}); err != nil {
		t.Fatalf("上限 limit 调用失败: %v", err)
	}
	if data.lastLongHuLimit != maxLongHu {
		t.Fatalf("上限 limit = %d，期望 %d", data.lastLongHuLimit, maxLongHu)
	}
}

// TestSnapshotPassesLimits 校验 snapshot 把两块 limit 分别收敛后透传。
func TestSnapshotPassesLimits(t *testing.T) {
	data := &fakeData{snap: datasource.MarketSnapshot{UpdatedAt: "2026-11-11T15:30:00+08:00"}}
	ctx := newTestContext(t, data)
	p := New().(*Plugin)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	reply, err := callMethod(t, ctx, "snapshot", map[string]any{})
	if err != nil {
		t.Fatalf("snapshot 失败: %v", err)
	}
	snap, ok := reply.(datasource.MarketSnapshot)
	if !ok || snap.UpdatedAt != "2026-11-11T15:30:00+08:00" {
		t.Fatalf("snapshot 返回 = %#v", reply)
	}
	if data.lastSnapLadder != defaultSnapshotLimit || data.lastSnapLongHu != defaultSnapshotLimit {
		t.Fatalf("snapshot 默认 limit = %d/%d", data.lastSnapLadder, data.lastSnapLongHu)
	}
	if _, err := callMethod(t, ctx, "snapshot", map[string]any{"ladderLimit": 500, "longhuLimit": 500}); err != nil {
		t.Fatalf("snapshot 上限调用失败: %v", err)
	}
	if data.lastSnapLadder != maxLadder || data.lastSnapLongHu != maxLongHu {
		t.Fatalf("snapshot 上限 limit = %d/%d", data.lastSnapLadder, data.lastSnapLongHu)
	}
}

// TestToolsReturnCount 校验工具返回 {count, items} 形状。
func TestToolsReturnCount(t *testing.T) {
	data := &fakeData{
		indices: []datasource.MarketIndex{{Code: "000001", Name: "上证指数"}},
		ladder:  []datasource.LimitUpStock{{Code: "600776"}, {Code: "001339"}},
		longhu:  []datasource.LongHuRow{{Code: "002384"}},
		hot:     []datasource.HotStock{{Code: "600715"}, {Code: "300308"}},
		breadth: datasource.MarketBreadth{Up: 3456, LimitUp: 84},
	}
	ctx := newTestContext(t, data)
	p := New().(*Plugin)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args any) any {
		t.Helper()
		tool, ok := ctx.Tools.Get(name)
		if !ok {
			t.Fatalf("工具 %s 未注册", name)
		}
		raw, _ := json.Marshal(args)
		out, err := tool.Run(context.Background(), raw)
		if err != nil {
			t.Fatalf("工具 %s 失败: %v", name, err)
		}
		return out
	}
	indices := run("market.indices", map[string]any{}).(map[string]any)
	if indices["count"] != 1 {
		t.Fatalf("market.indices count = %v", indices["count"])
	}
	ladder := run("market.ladder", map[string]any{}).(map[string]any)
	if ladder["count"] != 2 {
		t.Fatalf("market.ladder count = %v", ladder["count"])
	}
	longhu := run("market.longhu", map[string]any{}).(map[string]any)
	if longhu["count"] != 1 {
		t.Fatalf("market.longhu count = %v", longhu["count"])
	}
	hot := run("market.hot", map[string]any{"type": "day", "limit": 10}).(map[string]any)
	if hot["count"] != 2 {
		t.Fatalf("market.hot count = %v", hot["count"])
	}
	if data.lastHotType != "day" || data.lastHotLimit != 10 {
		t.Fatalf("market.hot 入参 = %q/%d，期望 day/10", data.lastHotType, data.lastHotLimit)
	}
	breadth := run("market.breadth", map[string]any{}).(datasource.MarketBreadth)
	if breadth.Up != 3456 || breadth.LimitUp != 84 {
		t.Fatalf("market.breadth = %+v", breadth)
	}
}

// TestUnsupportedDataLayer 校验旧数据层给出中文升级提示。
func TestUnsupportedDataLayer(t *testing.T) {
	ctx := newTestContext(t, &plainData{})
	p := New().(*Plugin)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := callMethod(t, ctx, "snapshot", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "当前数据层不支持市场中心") {
		t.Fatalf("错误 = %v，期望中文升级提示", err)
	}
}

// TestErrorsAreWrapped 校验空数据与上游失败会被翻成中文。
func TestErrorsAreWrapped(t *testing.T) {
	data := &fakeData{
		ladderErr: errors.New("upstream down"),
		longhuErr: errors.New("upstream down"),
	}
	ctx := newTestContext(t, data)
	p := New().(*Plugin)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := callMethod(t, ctx, "ladder", map[string]any{}); err == nil || !strings.Contains(err.Error(), "取涨停梯队失败") {
		t.Fatalf("ladder 错误 = %v", err)
	}
	if _, err := callMethod(t, ctx, "longhu", map[string]any{}); err == nil || !strings.Contains(err.Error(), "取龙虎榜失败") {
		t.Fatalf("longhu 错误 = %v", err)
	}
}

// TestPartialIndicesStillReturned 校验部分指数缺失时仍返回已有数据。
func TestPartialIndicesStillReturned(t *testing.T) {
	data := &fakeData{
		indices:    []datasource.MarketIndex{{Code: "000001", Name: "上证指数"}},
		indicesErr: datasource.ErrPartial,
	}
	ctx := newTestContext(t, data)
	p := New().(*Plugin)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	reply, err := callMethod(t, ctx, "indices", map[string]any{})
	if err != nil {
		t.Fatalf("部分指数失败: %v", err)
	}
	items := reply.([]datasource.MarketIndex)
	if len(items) != 1 {
		t.Fatalf("指数数量 = %d，期望 1", len(items))
	}
}
