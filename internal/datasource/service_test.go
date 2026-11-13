package datasource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fakeTransport 把出网请求按「host+path+query」路由到本地 fixture，测试全程不联网。
type fakeTransport struct {
	mu     sync.Mutex
	routes []fakeRoute
	calls  []string
}

type fakeRoute struct {
	match  string
	body   []byte
	status int // 0/200 表示成功，其它值模拟 HTTP 错误
}

func (f *fakeTransport) add(match string, body []byte) *fakeTransport {
	f.routes = append(f.routes, fakeRoute{match: match, body: body})
	return f
}

func (f *fakeTransport) addStatus(match string, status int) *fakeTransport {
	f.routes = append(f.routes, fakeRoute{match: match, status: status})
	return f
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.Host + req.URL.Path + "?" + req.URL.RawQuery
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	for _, route := range f.routes {
		if !strings.Contains(key, route.match) {
			continue
		}
		if route.status != 0 && route.status != http.StatusOK {
			return &http.Response{
				StatusCode: route.status,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(route.body)),
			Header:     http.Header{},
			Request:    req,
		}, nil
	}
	// 未配置的路由一律 400（不可重试），让「某源不可用」的用例快速失败
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (f *fakeTransport) countCalls(substr string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if strings.Contains(call, substr) {
			n++
		}
	}
	return n
}

// newServiceWithTransport 组装一个把网络请求重定向到 fixture 的 Service。
func newServiceWithTransport(t *testing.T, store contract.Store, transport *fakeTransport) (*Service, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	dir := t.TempDir()
	service := newTestService(t, Config{
		Guard:   newFakeGuard(),
		Store:   store,
		DataDir: dir,
	})
	service.now = clock.Now
	service.cache = newCache(clock.Now)
	service.client.now = clock.Now
	service.client.http = &http.Client{Transport: transport, Timeout: 5 * time.Second}
	return service, clock
}

// standardRoutes 配置一套「全部数据源可用」的路由。
func standardRoutes(t *testing.T) *fakeTransport {
	t.Helper()
	return (&fakeTransport{}).
		add("qt.gtimg.cn/q=", readTestdata(t, "tx_quote_600000.txt")).
		add("hq.sinajs.cn/list=", readTestdata(t, "sina_quote_600000.txt")).
		add("/v6/realhead/hs_600000/", readTestdata(t, "ths_realhead_stock600000.js")).
		add("/v6/line/hs_600000/01/2026.js", readTestdata(t, "ths_line_600000_2026.js")).
		add("/v6/line/hs_600000/01/2025.js", readTestdata(t, "ths_line_600000_2025.js")).
		add("/v6/line/hs_600000/11/last.js", readTestdata(t, "ths_line_600000_week_last.js")).
		add("/v6/line/hs_600000/50/last.js", readTestdata(t, "ths_line_600000_min60_last.js")).
		add("ssi_ssfx_flzjtj", readTestdata(t, "sina_flow_600000.json")).
		add("MoneyFlow.ssl_bkzj_bk?asc=0&fenlei=0", readTestdata(t, "sina_bkzj_bk_sector.json")).
		add("MoneyFlow.ssl_bkzj_bk?asc=0&fenlei=1", readTestdata(t, "sina_bkzj_bk_sector.json")).
		add("Market_Center.getHQNodes", readTestdata(t, "sina_hqnodes.json")).
		add("CN_MarketDataService.getKLineData", readTestdata(t, "sina_kline_600000.json")).
		add("smartbox.gtimg.cn", readTestdata(t, "tx_suggest_pfyh.txt"))
}

func TestServiceQuotePrefersTHSAndCrossValidates(t *testing.T) {
	transport := standardRoutes(t)
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	quotes, err := service.Quote(context.Background(), []string{"600000"})
	if err != nil {
		t.Fatalf("Quote 失败: %v", err)
	}
	if len(quotes) != 1 {
		t.Fatalf("行情数量 = %d，期望 1", len(quotes))
	}
	quote := quotes[0]
	if quote.Source != sourceTHS {
		t.Fatalf("来源 = %q，期望 ths（同花顺与腾讯一致时应采信同花顺）", quote.Source)
	}
	if quote.Name != "浦发银行" || quote.Price != 10.20 {
		t.Fatalf("行情 = %+v", quote)
	}
	// 成交量单位必须是「手」：47,782,652 股 ÷ 100 = 477,826.52 手
	if quote.Volume < 477826 || quote.Volume > 477827 {
		t.Fatalf("成交量 = %v 手，期望约 477826.52 手", quote.Volume)
	}
	if transport.countCalls("qt.gtimg.cn") == 0 {
		t.Fatal("应抓过一次腾讯快照做交叉校验")
	}
}

func TestServiceQuoteFallsBackToTencent(t *testing.T) {
	transport := (&fakeTransport{}).
		addStatus("/v6/realhead/hs_600000/", http.StatusBadRequest).
		add("qt.gtimg.cn/q=", readTestdata(t, "tx_quote_600000.txt"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	quotes, err := service.Quote(context.Background(), []string{"600000"})
	if err != nil {
		t.Fatalf("Quote 失败: %v", err)
	}
	if quotes[0].Source != sourceTencent {
		t.Fatalf("同花顺不可用时应降级到腾讯，实际来源 %q", quotes[0].Source)
	}
	if quotes[0].Volume != 477827 {
		t.Fatalf("腾讯成交量已是手，实际 %v", quotes[0].Volume)
	}
}

func TestServiceQuotePartialFailure(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))

	// BK1036 是东财板块代码：没有同花顺/腾讯/新浪快照，必然失败
	quotes, err := service.Quote(context.Background(), []string{"600000", "BK1036"})
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("部分失败应返回 ErrPartial，实际 %v", err)
	}
	if len(quotes) != 1 || quotes[0].Code != "600000" {
		t.Fatalf("应保留成功项，实际 %+v", quotes)
	}

	// 全部失败 → ErrNoData
	if _, err := service.Quote(context.Background(), []string{"BK1036"}); !errors.Is(err, ErrNoData) {
		t.Fatalf("全部失败应返回 ErrNoData，实际 %v", err)
	}
}

func TestServiceQuoteStaleFromStore(t *testing.T) {
	store := newMemStore()
	cached := contract.Quote{Code: "600000", Name: "浦发银行", Market: contract.MarketSH, Price: 9.99, PrevClose: 10.10}
	raw, _ := json.Marshal(cached)
	if err := store.Put(bucketMarketCache, prefixCachedQuote+"sh600000", raw); err != nil {
		t.Fatal(err)
	}
	// 所有实时源都失败
	transport := (&fakeTransport{}).
		addStatus("/v6/realhead/", http.StatusBadRequest).
		addStatus("qt.gtimg.cn", http.StatusBadRequest).
		addStatus("hq.sinajs.cn", http.StatusBadRequest)
	service, _ := newServiceWithTransport(t, store, transport)

	quotes, err := service.Quote(context.Background(), []string{"600000"})
	if err != nil {
		t.Fatalf("有缓存兜底时不应报错: %v", err)
	}
	if !quotes[0].Stale {
		t.Fatal("缓存兜底必须标记 stale")
	}
	if quotes[0].Source != sourceStore {
		t.Fatalf("来源 = %q，期望 store", quotes[0].Source)
	}
	if quotes[0].Price != 9.99 {
		t.Fatalf("价格 = %v，期望缓存值 9.99", quotes[0].Price)
	}
}

// TestServiceBarsDayRawFromTencent 覆盖 adj=none 的主链路：腾讯原始序列（真不复权）。
func TestServiceBarsDayRawFromTencent(t *testing.T) {
	transport := standardRoutes(t)
	transport.add("day,,,30,", readTestdata(t, "tx_fqkline_600000_raw_day.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	bars, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone)
	if err != nil {
		t.Fatalf("Bars 失败: %v", err)
	}
	if len(bars) != 30 {
		t.Fatalf("K 线数量 = %d，期望 30", len(bars))
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].Date <= bars[i-1].Date {
			t.Fatalf("K 线未按时间升序: %s 之后是 %s", bars[i-1].Date, bars[i].Date)
		}
	}
	if transport.countCalls(",qfq") != 0 {
		t.Fatalf("不复权路径不得请求前复权端点，实际 %d 次", transport.countCalls(",qfq"))
	}
}

// TestServiceBarsCrossYearAndIntraday 覆盖同花顺日线的跨年拼接与分钟线。
// 直接打 thsBars，避免被腾讯原始序列抢先命中（那是另一条链路，另有专门用例）。
func TestServiceBarsCrossYearAndIntraday(t *testing.T) {
	transport := standardRoutes(t)
	service, _ := newServiceWithTransport(t, newMemStore(), transport)
	code, err := ParseCode("600000")
	if err != nil {
		t.Fatal(err)
	}

	// 请求超过当年剩余根数（205）-> 需要回抓上一年
	bars, err := service.thsBars(context.Background(), code, contract.PeriodDay, 250)
	if err != nil {
		t.Fatalf("跨年 Bars 失败: %v", err)
	}
	if len(bars) < 250 {
		t.Fatalf("K 线数量 = %d，期望至少 250", len(bars))
	}
	if transport.countCalls("/01/2026.js") == 0 || transport.countCalls("/01/2025.js") == 0 {
		t.Fatal("跨年请求应同时抓当年与上一年文件")
	}

	min60, err := service.thsBars(context.Background(), code, contract.PeriodMin60, 60)
	if err != nil {
		t.Fatalf("60 分钟 K 线失败: %v", err)
	}
	if len(min60) != 60 {
		t.Fatalf("60 分钟 K 线数量 = %d，期望 60", len(min60))
	}
	if !strings.Contains(min60[len(min60)-1].Date, " ") || !strings.Contains(min60[len(min60)-1].Date, "-") {
		t.Fatalf("分钟线日期格式应为 2026-11-10 15:00，实际 %q", min60[len(min60)-1].Date)
	}
}

func TestServiceBarsAdjustUnsupported(t *testing.T) {
	transport := standardRoutes(t)
	// 东财 kline 在本机不可用：用 400 快速失败模拟（真实环境是空响应）
	transport.addStatus("/stock/kline/get", http.StatusBadRequest)
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	// 不复权正常
	if _, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 10, contract.AdjustNone); err != nil {
		t.Fatalf("不复权 Bars 失败: %v", err)
	}
	// 前复权：东财失败时还会试腾讯 fqkline；本用例的 transport 没有 fqkline 路由（400），
	// 两个源都拿不到时仍必须明确报 ErrAdjustUnsupported，不能拿不复权冒充。
	_, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 10, contract.AdjustQfq)
	if !errors.Is(err, ErrAdjustUnsupported) {
		t.Fatalf("两个源都不可用时应返回 ErrAdjustUnsupported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "不复权") {
		t.Fatalf("错误文案应给出可操作建议，实际 %q", err.Error())
	}
}

func TestServiceBarsCacheTTL(t *testing.T) {
	transport := standardRoutes(t)
	transport.add("day,,,30,", readTestdata(t, "tx_fqkline_600000_raw_day.json"))
	service, clock := newServiceWithTransport(t, newMemStore(), transport)

	if _, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone); err != nil {
		t.Fatal(err)
	}
	before := transport.countCalls("fqkline/get")
	if _, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone); err != nil {
		t.Fatal(err)
	}
	if transport.countCalls("fqkline/get") != before {
		t.Fatal("60 秒内重复请求应命中缓存")
	}
	clock.Advance(ttlBars + time.Second)
	if _, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone); err != nil {
		t.Fatal(err)
	}
	if transport.countCalls("fqkline/get") <= before {
		t.Fatal("超过 TTL 后应重新抓取")
	}
}

func TestServiceStockFundFlow(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))
	flow, err := service.StockFundFlow(context.Background(), "600000")
	if err != nil {
		t.Fatalf("个股资金流失败: %v", err)
	}
	if flow.Source != sourceSina || flow.Kind != "stock" {
		t.Fatalf("来源/Kind = %q/%q", flow.Source, flow.Kind)
	}
	// fixture（sina_flow_600000.json）真实值：
	//   r0_net = 24807640.23-42604228.05 = -17796587.82（超大单）
	//   r1_net = 36909673.63-54960758.46 = -18051084.83（大单）
	//   主力 = 两者之和 = -35847672.65
	if math.Abs(flow.SuperNet-(-17796587.82)) > 0.01 {
		t.Fatalf("超大单净额 = %v，期望 -17796587.82", flow.SuperNet)
	}
	if math.Abs(flow.LargeNet-(-18051084.83)) > 0.01 {
		t.Fatalf("大单净额 = %v，期望 -18051084.83", flow.LargeNet)
	}
	if math.Abs(flow.MainNet-(flow.LargeNet+flow.SuperNet)) > 0.01 {
		t.Fatalf("主力净额应等于大单+超大单: %+v", flow)
	}
	if flow.Name != "浦发银行" {
		t.Fatalf("名称 = %q，期望 浦发银行", flow.Name)
	}
}

// TestServiceStockFundFlowNoFallbackToOtherSource 保证个股资金流只有新浪一条链路：
// 新浪不可用就直接报错，不会用别的源给出不同口径的数字。
func TestServiceStockFundFlowNoFallbackToOtherSource(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	if _, err := service.StockFundFlow(context.Background(), "600000"); err == nil {
		t.Fatal("新浪不可用时应报错")
	}
}

func TestServiceFundFlowRankDefaults(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))

	// product 要求：空 kind、空 limit 都不许报错
	flows, err := service.FundFlowRank(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("空参数不应报错: %v", err)
	}
	if len(flows) != 10 {
		t.Fatalf("默认 limit 应为 10，实际 %d", len(flows))
	}
	if flows[0].Rank != 1 || flows[0].Kind != "sector" || flows[0].Source != sourceSina {
		t.Fatalf("首条 = %+v，期望 rank=1 sector source=sina", flows[0])
	}
	if flows[0].Code == "" || len(flows[0].Code) < 3 {
		t.Fatalf("板块代码应是新浪 category，实际 %q", flows[0].Code)
	}
	for i, flow := range flows {
		if flow.Rank != i+1 {
			t.Fatalf("第 %d 条 rank = %d", i, flow.Rank)
		}
	}
	for i := 1; i < len(flows); i++ {
		if flows[i].MainNet > flows[i-1].MainNet {
			t.Fatalf("净流入榜第 %d 名 %v 大于第 %d 名 %v", i+1, flows[i].MainNet, i, flows[i-1].MainNet)
		}
	}

	concepts, err := service.FundFlowRank(context.Background(), "concept", 3)
	if err != nil {
		t.Fatalf("概念资金流失败: %v", err)
	}
	if len(concepts) != 3 || concepts[0].Kind != "concept" {
		t.Fatalf("概念榜 = %+v", concepts)
	}
	if _, err := service.FundFlowRank(context.Background(), "unknown", 3); err == nil {
		t.Fatal("非法 kind 应报错")
	}
}

// TestServiceFundFlowRankByDirection 验证净流入/净流出榜的方向参数、本地重排与字段完整性。
// 上游 fenlei 0/1 是行业/概念，方向完全由本地按 netamount 重排（新浪只给 asc=0 降序）。
func TestServiceFundFlowRankByDirection(t *testing.T) {
	rankBody := []byte(`[{"category":"new_dzxx","name":"电子信息","netamount":"10024947698.76","ratioamount":"0.0771156","avg_changeratio":"0.0165576"},
		{"category":"new_nyhy","name":"能源行业","netamount":"-5000000000","ratioamount":"-0.05","avg_changeratio":"-0.02"},
		{"category":"new_yhhy","name":"银行行业","netamount":"120000000","ratioamount":"0.011","avg_changeratio":"0.005"}]`)
	transport := (&fakeTransport{}).add("MoneyFlow.ssl_bkzj_bk", rankBody)
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	// 空 kind / 空 direction / 空 limit 都使用默认值
	in, err := service.FundFlowRankBy(context.Background(), "", "", 0)
	if err != nil {
		t.Fatalf("默认参数不应报错: %v", err)
	}
	if len(in) != 3 {
		t.Fatalf("净流入榜行数 = %d，期望 3", len(in))
	}
	if in[0].Name != "电子信息" || in[0].Kind != "sector" || in[0].Rank != 1 {
		t.Fatalf("净流入榜首条 = %+v，期望 电子信息 sector rank=1", in[0])
	}
	if in[0].Source != sourceSina {
		t.Fatalf("来源 = %q，期望 %q", in[0].Source, sourceSina)
	}
	if math.Abs(in[0].ChangePct-1.65576) > 1e-4 || math.Abs(in[0].MainPct-7.71156) > 1e-4 {
		t.Fatalf("涨跌幅/占比 = %v/%v，期望约 1.65576/7.71156", in[0].ChangePct, in[0].MainPct)
	}
	for i := 1; i < len(in); i++ {
		if in[i].MainNet > in[i-1].MainNet {
			t.Fatalf("净流入榜第 %d 名 %v 大于第 %d 名 %v", i+1, in[i].MainNet, i, in[i-1].MainNet)
		}
	}
	for i, row := range in {
		if row.Rank != i+1 {
			t.Fatalf("第 %d 条 rank = %d，期望 %d", i, row.Rank, i+1)
		}
	}

	// 净流出榜：最负的排第一，rank 依然连续
	out, err := service.FundFlowRankBy(context.Background(), "sector", "out", 3)
	if err != nil {
		t.Fatalf("净流出榜失败: %v", err)
	}
	if len(out) != 3 || out[0].Name != "能源行业" || out[0].Rank != 1 {
		t.Fatalf("净流出榜 = %+v，期望 能源行业 排第一", out)
	}
	if math.Abs(out[0].ChangePct-(-2)) > 1e-6 || math.Abs(out[0].MainPct-(-5)) > 1e-6 {
		t.Fatalf("净流出榜涨跌幅/占比 = %v/%v，期望 -2/-5", out[0].ChangePct, out[0].MainPct)
	}
	for i := 1; i < len(out); i++ {
		if out[i].MainNet < out[i-1].MainNet {
			t.Fatalf("净流出榜第 %d 名 %v 小于第 %d 名 %v", i+1, out[i].MainNet, i, out[i-1].MainNet)
		}
	}

	// all 与 in 同序（都是净额降序），但缓存 key 必须区分方向
	all, err := service.FundFlowRankBy(context.Background(), "sector", "all", 2)
	if err != nil {
		t.Fatalf("all 方向失败: %v", err)
	}
	if len(all) != 2 || all[0].Name != "电子信息" {
		t.Fatalf("all 榜 = %+v", all)
	}

	// 缓存：同 kind + 同方向 + 同 limit 重复请求不再出网
	before := transport.countCalls("MoneyFlow.ssl_bkzj_bk")
	if _, err := service.FundFlowRankBy(context.Background(), "sector", "in", 10); err != nil {
		t.Fatal(err)
	}
	if got := transport.countCalls("MoneyFlow.ssl_bkzj_bk"); got != before {
		t.Fatalf("相同方向与 limit 应命中缓存，请求数 %d -> %d", before, got)
	}

	// 概念范围走 fenlei=1
	if _, err := service.FundFlowRankBy(context.Background(), "concept", "in", 5); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	calls := append([]string{}, transport.calls...)
	transport.mu.Unlock()
	if !strings.Contains(calls[len(calls)-1], "fenlei=1") {
		t.Fatalf("概念榜应请求 fenlei=1，最后一条请求 = %q", calls[len(calls)-1])
	}

	if _, err := service.FundFlowRankBy(context.Background(), "sector", "sideways", 5); err == nil {
		t.Fatal("非法 direction 应报错")
	}
}

func TestServiceSectorsDefault(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))
	sectors, err := service.Sectors(context.Background(), "")
	if err != nil {
		t.Fatalf("板块列表失败: %v", err)
	}
	if len(sectors) == 0 {
		t.Fatal("板块列表为空")
	}
	for _, sector := range sectors {
		if sector.Kind != "sector" {
			t.Fatalf("行业分类的 Kind 应为 sector，实际 %q", sector.Kind)
		}
		// 新浪行业 category 一定是 new_ 前缀（gn_ 是概念）。
		if !strings.HasPrefix(sector.Code, "new_") {
			t.Fatalf("行业 entry 的代码应是新浪 category（new_*），实际 %q", sector.Code)
		}
		if sector.Name == "" {
			t.Fatal("板块名称不应为空")
		}
	}

	concepts, err := service.Sectors(context.Background(), "concept")
	if err != nil {
		t.Fatalf("概念目录失败: %v", err)
	}
	if len(concepts) == 0 {
		t.Fatal("概念目录为空")
	}
	for _, concept := range concepts {
		if concept.Kind != "concept" || !strings.HasPrefix(concept.Code, "gn_") {
			t.Fatalf("概念 entry 不符: %+v", concept)
		}
	}
}

// TestServiceSectorsOnlyExposesFlowedCategories 保证只暴露有资金流数据的
// 新浪行业 / 概念板块两支，不混入申万、证监会等没有资金流的分类。
func TestServiceSectorsOnlyExposesFlowedCategories(t *testing.T) {
	entries, err := parseSinaHQNodes(readTestdata(t, "sina_hqnodes.json"))
	if err != nil {
		t.Fatalf("解析目录失败: %v", err)
	}
	for _, group := range [][]sectorEntry{entries[KindSector], entries[KindConcept]} {
		for _, entry := range group {
			if strings.HasPrefix(entry.Code, "sw") || strings.HasPrefix(entry.Code, "hangye_") ||
				strings.HasPrefix(entry.Code, "zhishu_") || strings.HasPrefix(entry.Code, "diyu_") {
				t.Fatalf("不应暴露没有资金流的分类: %+v", entry)
			}
		}
	}
	if len(entries[KindSector]) < 30 {
		t.Fatalf("新浪行业数量异常偏少: %d", len(entries[KindSector]))
	}
	if len(entries[KindConcept]) < 100 {
		t.Fatalf("概念板块数量异常偏少: %d", len(entries[KindConcept]))
	}
}

func TestServiceSearch(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))
	refs, err := service.Search(context.Background(), "浦发")
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("搜索结果为空")
	}
	if refs[0].Code != "600000" || refs[0].Name != "浦发银行" || refs[0].Market != contract.MarketSH {
		t.Fatalf("首条 = %+v", refs[0])
	}
	if _, err := service.Search(context.Background(), "  "); err == nil {
		t.Fatal("空关键词应报错")
	}
}

func TestServiceHealthListsAllSources(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))
	health := service.Health(context.Background())
	for _, name := range []string{sourceTHS, sourceSina, sourceTencent, sourceStore, "cache"} {
		if _, ok := health[name]; !ok {
			t.Fatalf("Health 缺少 %q，实际 %v", name, health)
		}
	}
	if _, err := service.Quote(context.Background(), []string{"600000"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(service.Health(context.Background())[sourceTHS], "可用") {
		t.Fatalf("调用成功后 ths 健康应显示可用，实际 %v", service.Health(context.Background())[sourceTHS])
	}
}

func TestServiceRequiresGuard(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("没有 Guard 应拒绝装配")
	}
}

func TestServiceQuoteRejectsBadInput(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))
	if _, err := service.Quote(context.Background(), nil); err == nil {
		t.Fatal("空代码列表应报错")
	}
	many := make([]string, 0, maxQuoteCodes+1)
	for i := 0; i <= maxQuoteCodes; i++ {
		many = append(many, fmt.Sprintf("60%04d", i))
	}
	if _, err := service.Quote(context.Background(), many); err == nil {
		t.Fatal("超过上限应报错")
	}
	if _, err := service.Quote(context.Background(), []string{"??"}); !errors.Is(err, ErrNoData) {
		t.Fatalf("全部无法识别应返回 ErrNoData，实际 %v", err)
	}
}

// TestServiceFundFlowRankFailureSurfacesError 保证新浪资金榜失败时整块报错，
// 不会再降级到别的源给出不同口径的榜单。
func TestServiceFundFlowRankFailureSurfacesError(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	if _, err := service.FundFlowRankBy(context.Background(), "sector", "out", 2); err == nil {
		t.Fatal("新浪资金榜不可用时应报错")
	}
}

// TestServiceSectorHistoryUsesSinaCategory 覆盖「榜单里点进去看多日」这条链路：
// 榜单返回的新浪 category 必须能直接喂给 SectorFundFlowHistory。
func TestServiceSectorHistoryUsesSinaCategory(t *testing.T) {
	transport := (&fakeTransport{}).
		add("MoneyFlow.ssl_bkzj_bk", readTestdata(t, "sina_bkzj_bk_sector.json")).
		add("MoneyFlow.ssl_bkzj_zjlrqs", readTestdata(t, "sina_bkzj_zjlrqs_new_dzxx.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	rank, err := service.FundFlowRankBy(context.Background(), "sector", "in", 5)
	if err != nil {
		t.Fatalf("资金榜失败: %v", err)
	}
	category := rank[0].Code
	if category == "" {
		t.Fatal("榜单条目必须有新浪 category")
	}
	hist, err := service.SectorFundFlowHistory(context.Background(), category, 5)
	if err != nil {
		t.Fatalf("用榜单 category 取多日历史失败: %v", err)
	}
	if len(hist.Points) == 0 {
		t.Fatal("多日历史为空")
	}
	// 榜单缓存里存了 category -> 中文名，历史接口要把名称补上，避免图例显示 new_xxxx。
	if hist.Name == "" || strings.HasPrefix(hist.Name, "new_") {
		t.Fatalf("历史名称应补成中文，实际 %q", hist.Name)
	}
}
