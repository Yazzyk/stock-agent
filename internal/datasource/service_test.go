package datasource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		add("/stock/fflow/kline/get", readTestdata(t, "em_fflow_600000.json")).
		add("getbkzj?code=m%3A90%2Bt%3A2", readTestdata(t, "em_bkzj_sector.json")).
		add("getbkzj?code=m%3A90%2Bt%3A3", readTestdata(t, "em_bkzj_concept.json")).
		add("clist/get", readTestdata(t, "em_clist_sector.json")).
		add("suggest/get", readTestdata(t, "em_suggest_pfyh.json"))
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

func TestServiceBarsDayFromTHS(t *testing.T) {
	transport := standardRoutes(t)
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	bars, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone)
	if err != nil {
		t.Fatalf("Bars 失败: %v", err)
	}
	if len(bars) != 30 {
		t.Fatalf("K 线数量 = %d，期望 30", len(bars))
	}
	if bars[len(bars)-1].Date != "2026-11-10" || bars[len(bars)-1].Close != 10.20 {
		t.Fatalf("最后一根 = %+v，期望 2026-11-10 收 10.20", bars[len(bars)-1])
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].Date <= bars[i-1].Date {
			t.Fatalf("K 线未按时间升序: %s 之后是 %s", bars[i-1].Date, bars[i].Date)
		}
	}
	if bars[len(bars)-1].Volume < 477826 || bars[len(bars)-1].Volume > 477827 {
		t.Fatalf("成交量 = %v 手，期望约 477826.52 手", bars[len(bars)-1].Volume)
	}
	if transport.countCalls("/v6/line/hs_600000/01/") != 1 {
		t.Fatalf("当年文件足够时只应抓一次，实际 %d 次", transport.countCalls("/v6/line/hs_600000/01/"))
	}
}

func TestServiceBarsCrossYearAndIntraday(t *testing.T) {
	transport := standardRoutes(t)
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	// 请求超过当年剩余根数（205）→ 需要回抓上一年
	bars, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 250, contract.AdjustNone)
	if err != nil {
		t.Fatalf("跨年 Bars 失败: %v", err)
	}
	if len(bars) != 250 {
		t.Fatalf("K 线数量 = %d，期望 250", len(bars))
	}
	if transport.countCalls("/01/2026.js") == 0 || transport.countCalls("/01/2025.js") == 0 {
		t.Fatal("跨年请求应同时抓当年与上一年文件")
	}

	min60, err := service.Bars(context.Background(), "600000", contract.PeriodMin60, 60, contract.AdjustNone)
	if err != nil {
		t.Fatalf("60 分钟 K 线失败: %v", err)
	}
	if len(min60) != 60 {
		t.Fatalf("60 分钟 K 线数量 = %d，期望 60", len(min60))
	}
	if !strings.Contains(min60[len(min60)-1].Date, " ") {
		t.Fatalf("分钟线日期应带时间，实际 %q", min60[len(min60)-1].Date)
	}
	if !strings.Contains(min60[len(min60)-1].Date, "-") {
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
	// 前复权必须明确报错，不能拿不复权冒充
	_, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 10, contract.AdjustQfq)
	if !errors.Is(err, ErrAdjustUnsupported) {
		t.Fatalf("前复权应返回 ErrAdjustUnsupported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "不复权") {
		t.Fatalf("错误文案应给出可操作建议，实际 %q", err.Error())
	}
}

func TestServiceBarsCacheTTL(t *testing.T) {
	transport := standardRoutes(t)
	service, clock := newServiceWithTransport(t, newMemStore(), transport)

	if _, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone); err != nil {
		t.Fatal(err)
	}
	before := transport.countCalls("/v6/line/")
	if _, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone); err != nil {
		t.Fatal(err)
	}
	if transport.countCalls("/v6/line/") != before {
		t.Fatal("60 秒内重复请求应命中缓存")
	}
	clock.Advance(ttlBars + time.Second)
	if _, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone); err != nil {
		t.Fatal(err)
	}
	if transport.countCalls("/v6/line/") <= before {
		t.Fatal("超过 TTL 后应重新抓取")
	}
}

func TestServiceStockFundFlow(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), standardRoutes(t))
	flow, err := service.StockFundFlow(context.Background(), "600000")
	if err != nil {
		t.Fatalf("个股资金流失败: %v", err)
	}
	if flow.Source != sourceEastMoney || flow.Kind != "stock" {
		t.Fatalf("来源/Kind = %q/%q", flow.Source, flow.Kind)
	}
	if flow.MainNet != 26922753 {
		t.Fatalf("主力净额 = %v，期望 26922753", flow.MainNet)
	}
	if flow.MainNet != flow.LargeNet+flow.SuperNet {
		t.Fatalf("主力净额应等于大单+超大单: %+v", flow)
	}

	// 东财不可用 → 新浪兜底
	fallback := (&fakeTransport{}).
		addStatus("/stock/fflow/kline/get", http.StatusBadRequest).
		add("MoneyFlow.ssi_ssfx_flzjtj", readTestdata(t, "sina_moneyflow_600000.json"))
	fallbackService, _ := newServiceWithTransport(t, newMemStore(), fallback)
	sinaFlow, err := fallbackService.StockFundFlow(context.Background(), "600000")
	if err != nil {
		t.Fatalf("新浪兜底失败: %v", err)
	}
	if sinaFlow.Source != sourceSina {
		t.Fatalf("来源 = %q，期望 sina", sinaFlow.Source)
	}
	if sinaFlow.MainNet < 59605000 || sinaFlow.MainNet > 59606000 {
		t.Fatalf("新浪主力净额 = %v，期望约 59,605,030.45", sinaFlow.MainNet)
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
	if flows[0].Name != "电子" || flows[0].Rank != 1 || flows[0].Kind != "sector" {
		t.Fatalf("首条 = %+v，期望 电子 sector rank=1", flows[0])
	}
	for i, flow := range flows {
		if flow.Rank != i+1 {
			t.Fatalf("第 %d 条 rank = %d", i, flow.Rank)
		}
	}

	concepts, err := service.FundFlowRank(context.Background(), "concept", 3)
	if err != nil {
		t.Fatalf("概念资金流失败: %v", err)
	}
	if len(concepts) != 3 || concepts[0].Name != "融资融券" || concepts[0].Kind != "concept" {
		t.Fatalf("概念榜 = %+v", concepts)
	}
	if _, err := service.FundFlowRank(context.Background(), "unknown", 3); err == nil {
		t.Fatal("非法 kind 应报错")
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
	if sectors[0].Code != "BK1201" || sectors[0].Name != "电子" || sectors[0].Kind != "sector" {
		t.Fatalf("首条 = %+v", sectors[0])
	}
	if sectors[0].ChangePct != 3.29 {
		t.Fatalf("涨跌幅 = %v，期望 3.29", sectors[0].ChangePct)
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
	for _, name := range []string{sourceTHS, sourceEastMoney, sourceSina, sourceTencent, sourceStore, "cache"} {
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
