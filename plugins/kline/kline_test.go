package kline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/datasource"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// fakeData 返回预置 K 线，并记录收到的复权参数。
type fakeData struct {
	bars        []contract.Bar
	quotes      map[string]contract.Quote
	barsErr     error
	adjustCalls []contract.Adjust
	limitCalls  []int
	periodCalls []contract.Period
}

func (f *fakeData) Quote(_ context.Context, codes []string) ([]contract.Quote, error) {
	if len(f.quotes) == 0 {
		return nil, errors.New("没有行情")
	}
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

func (f *fakeData) Bars(_ context.Context, _ string, period contract.Period, limit int, adjust contract.Adjust) ([]contract.Bar, error) {
	f.adjustCalls = append(f.adjustCalls, adjust)
	f.limitCalls = append(f.limitCalls, limit)
	f.periodCalls = append(f.periodCalls, period)
	if adjust != contract.AdjustNone {
		return nil, datasource.ErrAdjustUnsupported
	}
	if f.barsErr != nil {
		return nil, f.barsErr
	}
	if len(f.bars) > limit {
		return f.bars[len(f.bars)-limit:], nil
	}
	return f.bars, nil
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

// risingBars 造一段单调上涨的日线，供指标断言使用。
func risingBars(n int) []contract.Bar {
	bars := make([]contract.Bar, n)
	price := 10.0
	for i := 0; i < n; i++ {
		open := price
		closePrice := price + 0.1
		bars[i] = contract.Bar{
			Date:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02"),
			Open:   open,
			High:   closePrice + 0.05,
			Low:    open - 0.05,
			Close:  closePrice,
			Volume: float64(1000 + i*10),
		}
		price = closePrice
	}
	return bars
}

func TestManifestAndActivate(t *testing.T) {
	p := New()
	if p.Manifest().ID != "kline" {
		t.Fatalf("插件 ID = %q", p.Manifest().ID)
	}
	ctx := newTestContext(t, &fakeData{bars: risingBars(80)})
	if err := p.Activate(ctx); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	if len(ctx.Methods.List()) != 2 {
		t.Fatalf("方法数 = %d，期望 2", len(ctx.Methods.List()))
	}
	if _, ok := ctx.Services.Get("kline.engine"); !ok {
		t.Fatal("未注册 kline.engine 服务")
	}
	tools := ctx.Tools.List()
	if len(tools) != 2 {
		t.Fatalf("工具数 = %d，期望 2", len(tools))
	}
	for _, tool := range tools {
		if runes := []rune(tool.Summary); len(runes) > 40 {
			t.Fatalf("工具 %s 的 Summary 超过 40 字（%d）", tool.Name, len(runes))
		}
		if len(tool.Tags) == 0 || !json.Valid(tool.Schema) {
			t.Fatalf("工具 %s 元数据不完整", tool.Name)
		}
		if tool.Risk != plugin.RiskNetwork {
			t.Fatalf("工具 %s 风险等级应为 network，实际 %s", tool.Name, tool.Risk)
		}
	}
	slots := ctx.UI.List()
	if len(slots) != 1 || slots[0].Component != "kline" || slots[0].Order != 30 {
		t.Fatalf("UI slot = %+v", slots)
	}
}

func TestBarsDefaultsAndIndicators(t *testing.T) {
	data := &fakeData{
		bars:   risingBars(200),
		quotes: map[string]contract.Quote{"600000": {Code: "600000", Name: "浦发银行"}},
	}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	bars, _ := ctx.Methods.Get("bars")
	out, err := bars.Fn(context.Background(), json.RawMessage(`{"code":"600000"}`))
	if err != nil {
		t.Fatalf("bars 失败: %v", err)
	}
	reply := out.(BarsReply)
	if reply.Period != "day" || reply.Adjust != "none" {
		t.Fatalf("默认周期/复权 = %q/%q", reply.Period, reply.Adjust)
	}
	if len(reply.Bars) != defaultBarsLimit {
		t.Fatalf("默认返回根数 = %d，期望 %d", len(reply.Bars), defaultBarsLimit)
	}
	if reply.Name != "浦发银行" {
		t.Fatalf("名称 = %q", reply.Name)
	}
	// 指标数组必须与 bars 等长（缺口为 NaN/null）
	for name, series := range map[string]int{
		"ma5": len(reply.Indicators.MA5), "ma10": len(reply.Indicators.MA10),
		"ma20": len(reply.Indicators.MA20), "ma60": len(reply.Indicators.MA60),
		"volume": len(reply.Indicators.Volume),
	} {
		if series != len(reply.Bars) {
			t.Fatalf("%s 长度 = %d，期望 %d", name, series, len(reply.Bars))
		}
	}
	// 上涨序列的 MA5 最后一根应为有效值（NaN 表示缺口）
	if last := reply.Indicators.MA5[len(reply.Indicators.MA5)-1]; math.IsNaN(last) {
		t.Fatal("上涨序列的 MA5 不应为缺口")
	}
	// JSON 序列化后缺口应为 null（面板靠这个留空）
	raw, err := json.Marshal(reply.Indicators.MA60)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "[null") {
		t.Fatalf("ma60 前几个点应为 null，实际前缀 %q", string(raw)[:min(24, len(raw))])
	}
}

func TestBarsAdjustDowngrade(t *testing.T) {
	data := &fakeData{
		bars:   risingBars(80),
		quotes: map[string]contract.Quote{"600000": {Code: "600000", Name: "浦发银行"}},
	}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	bars, _ := ctx.Methods.Get("bars")
	out, err := bars.Fn(context.Background(), json.RawMessage(`{"code":"600000","adjust":"qfq"}`))
	if err != nil {
		t.Fatalf("qfq 应降级而不是报错: %v", err)
	}
	reply := out.(BarsReply)
	if reply.Adjust != "none" {
		t.Fatalf("降级后 adjust = %q，期望 none", reply.Adjust)
	}
	if reply.Warning == "" || !strings.Contains(reply.Warning, "不复权") {
		t.Fatalf("降级必须带明确 warning，实际 %q", reply.Warning)
	}
	if len(data.adjustCalls) != 2 || data.adjustCalls[0] != contract.AdjustQfq || data.adjustCalls[1] != contract.AdjustNone {
		t.Fatalf("降级调用顺序 = %v，期望先 qfq 再 不复权", data.adjustCalls)
	}
}

func TestBarsErrorsAndValidation(t *testing.T) {
	data := &fakeData{bars: risingBars(30)}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	bars, _ := ctx.Methods.Get("bars")
	if _, err := bars.Fn(context.Background(), json.RawMessage(`{"code":""}`)); err == nil {
		t.Fatal("空代码应报错")
	}
	data.barsErr = errors.New("上游 502")
	if _, err := bars.Fn(context.Background(), json.RawMessage(`{"code":"600000"}`)); err == nil {
		t.Fatal("数据层报错应向上返回")
	}
}

func TestBarsLimitCap(t *testing.T) {
	data := &fakeData{bars: risingBars(400), quotes: map[string]contract.Quote{"600000": {Code: "600000", Name: "x"}}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	bars, _ := ctx.Methods.Get("bars")
	if _, err := bars.Fn(context.Background(), json.RawMessage(`{"code":"600000","limit":9999}`)); err != nil {
		t.Fatal(err)
	}
	if data.limitCalls[0] != maxBarsLimit {
		t.Fatalf("limit 上限 = %d，期望 %d", data.limitCalls[0], maxBarsLimit)
	}
	if _, err := bars.Fn(context.Background(), json.RawMessage(`{"code":"600000","period":"min1"}`)); err != nil {
		t.Fatal(err)
	}
	if data.periodCalls[len(data.periodCalls)-1] != contract.PeriodDay {
		t.Fatalf("未知周期应回落到 day，实际 %q", data.periodCalls[len(data.periodCalls)-1])
	}
}

func TestAnalyze(t *testing.T) {
	data := &fakeData{
		bars:   risingBars(200),
		quotes: map[string]contract.Quote{"600000": {Code: "600000", Name: "浦发银行"}},
	}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	analyze, _ := ctx.Methods.Get("analyze")
	out, err := analyze.Fn(context.Background(), json.RawMessage(`{"code":"600000"}`))
	if err != nil {
		t.Fatalf("analyze 失败: %v", err)
	}
	reply := out.(AnalyzeReply)
	if reply.Code != "600000" || reply.Name != "浦发银行" {
		t.Fatalf("返回头 = %+v", reply)
	}
	if reply.Last.Close <= 0 {
		t.Fatalf("last = %+v", reply.Last)
	}
	summary := reply.Summary
	if summary.Trend == "" || summary.MA == "" || summary.RSI == "" || summary.MACD == "" || summary.ATR == "" {
		t.Fatalf("摘要字段缺失: %+v", summary)
	}
	if reply.Adjust != "none" {
		t.Fatalf("分析口径应为不复权，实际 %q", reply.Adjust)
	}
	// 单调上涨的序列，趋势摘要应体现多头
	if !strings.Contains(summary.Trend, "多头") {
		t.Fatalf("上涨序列的趋势摘要 = %q", summary.Trend)
	}
}

func TestToolBarsReturnsPlainArray(t *testing.T) {
	data := &fakeData{bars: risingBars(60), quotes: map[string]contract.Quote{"600000": {Code: "600000", Name: "x"}}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	tool, ok := ctx.Tools.Get("market.bars")
	if !ok {
		t.Fatal("缺少 market.bars 工具")
	}
	out, err := tool.Run(context.Background(), json.RawMessage(`{"code":"600000","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	bars, ok := out.([]contract.Bar)
	if !ok {
		t.Fatalf("工具应直接返回 Bar 数组，实际 %T", out)
	}
	if len(bars) != 5 {
		t.Fatalf("根数 = %d，期望 5", len(bars))
	}

	analyzeTool, _ := ctx.Tools.Get("kline.analyze")
	if _, err := analyzeTool.Run(context.Background(), json.RawMessage(`{"code":"600000"}`)); err != nil {
		t.Fatal(err)
	}
}
