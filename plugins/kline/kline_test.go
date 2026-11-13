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
	// supportQfq=true 时前复权可用（模拟腾讯 fqkline qfq 正常）；默认不可用以覆盖降级分支。
	supportQfq bool
	// minute/minuteErr 供分时方法与工具测试使用。
	minute      contract.MinuteSeries
	minuteErr   error
	minuteCodes []string
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
	if adjust != contract.AdjustNone && !f.supportQfq {
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

// Minute 返回预置分时序列并记录入参代码；minuteErr 非空时模拟上游失败。
func (f *fakeData) Minute(_ context.Context, code string) (contract.MinuteSeries, error) {
	f.minuteCodes = append(f.minuteCodes, code)
	if f.minuteErr != nil {
		return contract.MinuteSeries{}, f.minuteErr
	}
	series := f.minute
	series.Code = code
	return series, nil
}

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
	if len(ctx.Methods.List()) != 3 {
		t.Fatalf("方法数 = %d，期望 3", len(ctx.Methods.List()))
	}
	if _, ok := ctx.Services.Get("kline.engine"); !ok {
		t.Fatal("未注册 kline.engine 服务")
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
	// 默认 fake 不支持前复权：必须显式降级为不复权并带 warning，且调用顺序是 qfq → none
	if reply.Adjust != "none" {
		t.Fatalf("qfq 不可用时分析口径应降级为 none，实际 %q", reply.Adjust)
	}
	if reply.Warning == "" || !strings.Contains(reply.Warning, "不复权") {
		t.Fatalf("降级必须带 warning，实际 %q", reply.Warning)
	}
	if len(data.adjustCalls) != 2 || data.adjustCalls[0] != contract.AdjustQfq || data.adjustCalls[1] != contract.AdjustNone {
		t.Fatalf("调用顺序 = %v，期望先 qfq 再 none", data.adjustCalls)
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

// TestAnalyzeUsesQfq 钉住研判口径：技术分析应基于前复权价，
// qfq 可用时 reply.Adjust 必须是 qfq、不能带降级 warning，也不能再请求不复权。
func TestAnalyzeUsesQfq(t *testing.T) {
	data := &fakeData{
		bars:       risingBars(200),
		quotes:     map[string]contract.Quote{"600000": {Code: "600000", Name: "浦发银行"}},
		supportQfq: true,
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
	if reply.Adjust != "qfq" {
		t.Fatalf("qfq 可用时分析口径应为 qfq，实际 %q", reply.Adjust)
	}
	if reply.Warning != "" {
		t.Fatalf("qfq 可用时不该有降级 warning，实际 %q", reply.Warning)
	}
	if len(data.adjustCalls) != 1 || data.adjustCalls[0] != contract.AdjustQfq {
		t.Fatalf("调用序列 = %v，期望只请求 qfq（不触发降级）", data.adjustCalls)
	}
	if reply.Last.Close <= 0 || reply.Summary.Trend == "" {
		t.Fatalf("分析结果不完整: %+v", reply)
	}
}

// validHorizonBias 判断双尺度结论是否落在三态字面量内（前端按此着色）。
func validHorizonBias(b string) bool {
	return b == "偏多" || b == "偏空" || b == "中性"
}

// TestAnalyzeHorizonsCompatibility 钉住增量契约：horizons 不破坏顶层 summary/patterns/support/resistance，
// 且 kline.analyze 工具返回体同样自动携带双尺度研判。
func TestAnalyzeHorizonsCompatibility(t *testing.T) {
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

	// 旧字段保持原样
	if reply.Summary.Trend == "" || reply.Last.Close <= 0 || reply.Code != "600000" {
		t.Fatalf("旧字段被破坏: %+v", reply)
	}

	if len(reply.Horizons) != 2 {
		t.Fatalf("horizons 份数 = %d，期望短线+长线 2 份", len(reply.Horizons))
	}
	if reply.Horizons[0].Key != "short" || reply.Horizons[1].Key != "long" {
		t.Fatalf("horizons 顺序 = %q/%q，期望 short/long", reply.Horizons[0].Key, reply.Horizons[1].Key)
	}
	for _, h := range reply.Horizons {
		if h.Title == "" || h.Window <= 0 || !validHorizonBias(h.Bias) {
			t.Fatalf("尺度字段不完整: %+v", h)
		}
		if h.Summary.Trend == "" || h.Summary.MA == "" || h.Summary.RSI == "" || h.Summary.MACD == "" || h.Summary.ATR == "" {
			t.Fatalf("%s 摘要字段缺失: %+v", h.Key, h.Summary)
		}
		if len(h.Support) == 0 || len(h.Resistance) == 0 {
			t.Fatalf("%s 支撑/阻力为空: %v / %v", h.Key, h.Support, h.Resistance)
		}
	}

	// JSON 契约：旧字段与 horizons 并存，字段名与前端 parse.ts 对齐
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"summary", "patterns", "support", "resistance", "horizons"} {
		if _, ok := obj[key]; !ok {
			t.Fatalf("返回体缺少字段 %q", key)
		}
	}

	tool, ok := ctx.Tools.Get("kline.analyze")
	if !ok {
		t.Fatal("缺少 kline.analyze 工具")
	}
	toolOut, err := tool.Run(context.Background(), json.RawMessage(`{"code":"600000"}`))
	if err != nil {
		t.Fatal(err)
	}
	toolRaw, err := json.Marshal(toolOut)
	if err != nil {
		t.Fatal(err)
	}
	var toolObj map[string]json.RawMessage
	if err := json.Unmarshal(toolRaw, &toolObj); err != nil {
		t.Fatal(err)
	}
	if _, ok := toolObj["horizons"]; !ok {
		t.Fatal("kline.analyze 工具返回体应自动携带 horizons")
	}
}

// noMinuteData 只实现 DataService、不实现分时扩展接口，用于覆盖「数据层版本不匹配」提示。
type noMinuteData struct{ contract.DataService }

func TestMinuteMethodAndTool(t *testing.T) {
	data := &fakeData{minute: contract.MinuteSeries{
		Name:      "贵州茅台",
		Date:      "2026-11-11",
		PrevClose: 1439,
		Points: []contract.MinutePoint{
			{Time: "09:30", Price: 1445, Avg: 1445, Volume: 239, Amount: 34535500},
			{Time: "09:31", Price: 1437.03, Avg: 1440.266, Volume: 633, Amount: 91055690},
		},
	}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	method, ok := ctx.Methods.Get("minute")
	if !ok {
		t.Fatal("缺少 minute 方法")
	}
	out, err := method.Fn(context.Background(), json.RawMessage(`{"code":"600519"}`))
	if err != nil {
		t.Fatalf("minute 失败: %v", err)
	}
	series, ok := out.(contract.MinuteSeries)
	if !ok {
		t.Fatalf("返回类型 = %T，期望 contract.MinuteSeries", out)
	}
	if series.Code != "600519" || series.Name != "贵州茅台" || len(series.Points) != 2 {
		t.Fatalf("分时返回 = %+v", series)
	}
	if series.PrevClose != 1439 || series.Points[0].Time != "09:30" {
		t.Fatalf("分时字段 = %+v", series)
	}

	tool, ok := ctx.Tools.Get("market.minute")
	if !ok {
		t.Fatal("缺少 market.minute 工具")
	}
	toolOut, err := tool.Run(context.Background(), json.RawMessage(`{"code":"600519"}`))
	if err != nil {
		t.Fatalf("工具失败: %v", err)
	}
	if toolSeries, ok := toolOut.(contract.MinuteSeries); !ok || toolSeries.Code != "600519" {
		t.Fatalf("工具返回 = %T %+v", toolOut, toolOut)
	}
	if tool.Summary == "" || len([]rune(tool.Summary)) > 40 || tool.Risk != plugin.RiskNetwork {
		t.Fatalf("工具元数据 = %+v", tool)
	}

	if _, err := method.Fn(context.Background(), json.RawMessage(`{"code":"  "}`)); err == nil {
		t.Fatal("空代码应报错")
	}
	data.minuteErr = errors.New("上游 502")
	if _, err := method.Fn(context.Background(), json.RawMessage(`{"code":"600519"}`)); err == nil ||
		!strings.Contains(err.Error(), "取 600519 分时失败") {
		t.Fatalf("上游失败应带上下文，实际 %v", err)
	}
}

// TestMinuteUnsupportedDataLayer 钉住降级提示：数据层没有 Minute 扩展时给出可读错误而不是 panic。
func TestMinuteUnsupportedDataLayer(t *testing.T) {
	p := New()
	ctx := newTestContext(t, &noMinuteData{&fakeData{bars: risingBars(10)}})
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	method, _ := ctx.Methods.Get("minute")
	if _, err := method.Fn(context.Background(), json.RawMessage(`{"code":"600519"}`)); err == nil ||
		!strings.Contains(err.Error(), "不支持分时") {
		t.Fatalf("应提示数据层不支持分时，实际 %v", err)
	}
}
