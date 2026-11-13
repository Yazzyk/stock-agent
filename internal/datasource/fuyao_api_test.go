package datasource

// 官方 API 通用层与各业务域的离线单测：全部使用 httptest + testdata/fuyao 固件，
// 不联网。真实网络验证见 fuyao_live_test.go（默认跳过，需 GUANLAN_FUYAO_LIVE=1）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// loadFuyaoFixture 读取 testdata/fuyao 下的官方响应固件。
func loadFuyaoFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fuyao", name))
	if err != nil {
		t.Fatalf("读取固件 %s 失败: %v", name, err)
	}
	return raw
}

// newFixtureServer 返回一个固定响应固件的官方 API 测试服务。
func newFixtureServer(t *testing.T, name string) (*Service, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	body := loadFuyaoFixture(t, name)
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("X-api-key"); got != "sk-test" {
			t.Errorf("必须带 X-api-key，实际 %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	setFuyaoBase(t, s, srv.URL)
	return s, &calls
}

// TestFuyaoCatalog 验证目录完整性：数量、ID 唯一、路径与必填参数。
func TestFuyaoCatalog(t *testing.T) {
	svc, err := New(Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := svc.(*Service)
	eps := s.FuyaoCatalog(context.Background())
	if len(eps) != 125 {
		t.Fatalf("目录应覆盖 125 个官方端点，实际 %d", len(eps))
	}
	seen := map[string]bool{}
	groups := map[string]int{}
	for _, ep := range eps {
		if ep.ID == "" || ep.Path == "" || ep.Title == "" || ep.Group == "" {
			t.Fatalf("端点字段不完整: %+v", ep)
		}
		if !strings.HasPrefix(ep.Path, "/api/") {
			t.Fatalf("端点路径非法: %s", ep.Path)
		}
		if seen[ep.ID] {
			t.Fatalf("端点 ID 重复: %s", ep.ID)
		}
		seen[ep.ID] = true
		groups[ep.Group]++
		// 必填参数必须能说清用途，避免工具 schema 与文档漂移。
		for _, p := range ep.Params {
			if p.Required && p.Desc == "" {
				t.Fatalf("%s 的必填参数 %s 缺少说明", ep.ID, p.Name)
			}
		}
	}
	for _, group := range []string{"行情", "特色数据", "财务", "指数", "基金", "期货", "期权", "资讯"} {
		if groups[group] == 0 {
			t.Fatalf("目录缺少分组 %s", group)
		}
	}
	// 客户端专用端点必须标注权限，避免工具把它们当成普通故障。
	client := 0
	for _, ep := range eps {
		if ep.Permission == "client" {
			client++
		}
	}
	if client < 8 {
		t.Fatalf("客户端专用端点数量异常（实测应 ≥8），实际 %d", client)
	}
}

// TestFuyaoQueryValidates 验证未知参数与必填参数在本地就被拒绝。
func TestFuyaoQueryValidates(t *testing.T) {
	s, _ := newFixtureServer(t, "snapshot.json")
	_, err := s.FuyaoQuery(context.Background(), contract.FuyaoQuery{
		Endpoint: "a-share.prices.snapshot",
		Params:   map[string]string{"thscode": "600519.SH"},
	})
	if err == nil || !strings.Contains(err.Error(), "不支持参数") {
		t.Fatalf("未知参数必须报错，实际 %v", err)
	}
	_, err = s.FuyaoQuery(context.Background(), contract.FuyaoQuery{Endpoint: "meta.tickers.search"})
	if err == nil || !strings.Contains(err.Error(), "缺少必填参数") {
		t.Fatalf("缺少必填参数必须报错，实际 %v", err)
	}
	_, err = s.FuyaoQuery(context.Background(), contract.FuyaoQuery{Endpoint: "no.such.endpoint"})
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("未知端点必须报错，实际 %v", err)
	}
}

// TestFuyaoQueryTrimsAndMaps 验证时间戳、条数裁剪与长文本截断。
func TestFuyaoQueryTrimsAndMaps(t *testing.T) {
	s, _ := newFixtureServer(t, "snapshot.json")
	data, err := s.FuyaoQuery(context.Background(), contract.FuyaoQuery{
		Endpoint: "a-share.prices.snapshot",
		Params:   map[string]string{"thscodes": "600519.SH,000001.SZ"},
		Limit:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if data.Count != 2 || len(data.Items) != 1 || !data.Truncated {
		t.Fatalf("条数/裁剪不对: count=%d items=%d truncated=%v", data.Count, len(data.Items), data.Truncated)
	}
	if data.Timestamp != 1794552945000 {
		t.Fatalf("时间戳必须来自 data.timestamp，实际 %d", data.Timestamp)
	}
	if data.Items[0]["thscode"] != "600519.SH" {
		t.Fatalf("记录字段不对: %+v", data.Items[0])
	}
}

// TestFuyaoQueryPermissionDegrade 验证 2004 被转成可展示的降级结果而不是错误。
func TestFuyaoQueryPermissionDegrade(t *testing.T) {
	s, _ := newFixtureServer(t, "permission_2004.json")
	data, err := s.FuyaoQuery(context.Background(), contract.FuyaoQuery{
		Endpoint: "a-share.capital-flow.snapshot",
		Params:   map[string]string{"thscode": "600519.SH"},
	})
	if err != nil {
		t.Fatalf("2004 不应返回 error: %v", err)
	}
	if data.Permission != "client" || data.Count != 0 || data.Note == "" {
		t.Fatalf("2004 应降级为 permission=client + note，实际 %+v", data)
	}
}

// TestFuyaoRetryOn4001 验证 code=4001 只退避重试一次，成功后不再降级。
func TestFuyaoRetryOn4001(t *testing.T) {
	var calls atomic.Int64
	okBody := loadFuyaoFixture(t, "snapshot.json")
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"code":4001,"message":"rate limited","data":null}`))
			return
		}
		_, _ = w.Write(okBody)
	})
	setFuyaoBase(t, s, srv.URL)
	data, err := s.FuyaoQuery(context.Background(), contract.FuyaoQuery{
		Endpoint: "a-share.prices.snapshot",
		Params:   map[string]string{"thscodes": "600519.SH"},
	})
	if err != nil {
		t.Fatalf("退避重试后应成功: %v", err)
	}
	if calls.Load() != 2 || data.Count != 2 {
		t.Fatalf("应恰好请求 2 次并成功，实际 calls=%d count=%d", calls.Load(), data.Count)
	}
}

// TestFuyaoQuerySplitsBatch 验证超过目录上限的 thscodes 会被切分。
func TestFuyaoQuerySplitsBatch(t *testing.T) {
	codes := make([]string, 0, 250)
	for i := 0; i < 250; i++ {
		codes = append(codes, fmt.Sprintf("%06d.SH", 600000+i))
	}
	var calls atomic.Int64
	body := loadFuyaoFixture(t, "auction.json")
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		got := len(strings.Split(r.URL.Query().Get("thscodes"), ","))
		if got > 100 {
			t.Errorf("单批代码数超过 100: %d", got)
		}
		_, _ = w.Write(body)
	})
	setFuyaoBase(t, s, srv.URL)
	_, err := s.FuyaoQuery(context.Background(), contract.FuyaoQuery{
		Endpoint: "a-share.auction.snapshot",
		Params:   map[string]string{"thscodes": strings.Join(codes, ",")},
		Limit:    300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("250 个代码按 100/批应请求 3 次，实际 %d", calls.Load())
	}
}

// TestFuyaoQuoteRowsBatchCache 验证 120 只代码按 50/批并发、且第二次调用零请求。
func TestFuyaoQuoteRowsBatchCache(t *testing.T) {
	const latency = 40 * time.Millisecond
	var calls atomic.Int64
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(latency)
		ths := strings.Split(r.URL.Query().Get("thscodes"), ",")
		items := make([]map[string]any, 0, len(ths))
		for _, code := range ths {
			items = append(items, map[string]any{
				"thscode": code, "ticker": strings.Split(code, ".")[0],
				"volume": 100000, "turnover": 1000000,
				"last_price": 10.5, "price_change": 0.2, "price_change_ratio_pct": 1.94,
				"open_price": 10.3, "high_price": 10.6, "low_price": 10.2, "prev_price": 10.3,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "message": "success",
			"data": map[string]any{"timestamp": 1794552945000, "item": items},
		})
	})
	setFuyaoBase(t, s, srv.URL)
	codes := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		codes = append(codes, fmt.Sprintf("%06d.SH", 600000+i))
	}
	start := time.Now()
	rows, err := s.FuyaoQuoteRows(context.Background(), codes)
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	if len(rows) != 120 {
		t.Fatalf("应返回 120 行，实际 %d", len(rows))
	}
	if calls.Load() != 3 {
		t.Fatalf("120 只按 50/批应 3 次请求，实际 %d", calls.Load())
	}
	if cold > 3*latency {
		t.Fatalf("分批必须并发（3 批不应串行 3×40ms），实际 %v", cold)
	}
	before := calls.Load()
	start = time.Now()
	if _, err := s.FuyaoQuoteRows(context.Background(), codes); err != nil {
		t.Fatal(err)
	}
	warm := time.Since(start)
	if calls.Load() != before {
		t.Fatalf("热缓存不应再发请求，实际新增 %d 次", calls.Load()-before)
	}
	if warm > 10*time.Millisecond {
		t.Fatalf("热缓存应明显快于冷启动，实际 %v", warm)
	}
	t.Logf("120 只：冷 %v（3 请求）、热 %v（0 请求）", cold, warm)
}

// TestFuyaoLimitRowsDecode 验证涨停池字段（连板数、封单额）映射。
func TestFuyaoLimitRowsDecode(t *testing.T) {
	s, _ := newFixtureServer(t, "limit_up_pool.json")
	rows, err := s.FuyaoLimitRows(context.Background(), "up", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("涨停池条数不对: %d", len(rows))
	}
	if rows[0].Boards != 3 || rows[0].SealMoney != 11088756 || rows[0].Reason == "" {
		t.Fatalf("涨停池字段映射不对: %+v", rows[0])
	}
	if rows[1].OpenTimes != nil {
		t.Fatalf("open_times=null 应映射成 nil，实际 %v", *rows[1].OpenTimes)
	}
}

// TestFuyaoHotRowsDecode 验证热榜字段（heat 字符串、排名变化）。
func TestFuyaoHotRowsDecode(t *testing.T) {
	s, _ := newFixtureServer(t, "hot_stock.json")
	rows, err := s.FuyaoHotRows(context.Background(), "hot", "hour", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Heat != "5508260" || rows[0].RankChange == nil || *rows[0].RankChange != 3 {
		t.Fatalf("热榜字段映射不对: %+v", rows)
	}
}

// TestFuyaoAuctionDecode 验证竞价快照字段。
func TestFuyaoAuctionDecode(t *testing.T) {
	s, _ := newFixtureServer(t, "auction.json")
	rows, err := s.FuyaoAuction(context.Background(), []string{"600519"}, "final")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AuctionPct != 0.35 || rows[0].AuctionVolume != 12600 {
		t.Fatalf("竞价字段映射不对: %+v", rows)
	}
}

// TestFuyaoFinancialsDecode 验证报表分组字段与单位说明（金额原样，不做单位换算）。
func TestFuyaoFinancialsDecode(t *testing.T) {
	s, _ := newFixtureServer(t, "financial_income.json")
	items, err := s.FuyaoFinancials(context.Background(), "income", "600519", "annual", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || mapFloat(items[0], "net_profit") != 60000000000 {
		t.Fatalf("利润表字段映射不对: %+v", items)
	}
}

// TestFuyaoNewsView 验证资讯 items 容器与长文本裁剪。
func TestFuyaoNewsView(t *testing.T) {
	s, _ := newFixtureServer(t, "news_flash.json")
	data, err := s.FuyaoNewsView(context.Background(), "flash", map[string]string{"tag_id": "1", "seq": "0"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if data.Count != 2 || data.Items[0]["title"] == "" {
		t.Fatalf("资讯 items 容器解析不对: %+v", data)
	}
}

// TestFuyaoFundView 验证基金视图白名单与参数映射。
func TestFuyaoFundView(t *testing.T) {
	s, _ := newFixtureServer(t, "fund_profile.json")
	data, err := s.FuyaoFundView(context.Background(), "profile", map[string]string{"thscode": "510300.SH"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if data.Count != 1 || data.Items[0]["fund_name"] != "沪深300ETF华泰柏瑞" {
		t.Fatalf("基金资料解析不对: %+v", data)
	}
	if _, err := s.FuyaoFundView(context.Background(), "no-such-view", nil, 10); err == nil {
		t.Fatal("未知基金视图必须报错")
	}
}

// TestFuyaoKlineBatch 验证批量 K 线的分组、顺序与缓存。
func TestFuyaoKlineBatch(t *testing.T) {
	var calls atomic.Int64
	body := loadFuyaoFixture(t, "kline_batch.json")
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasPrefix(r.URL.Path, "/api/a-share/prices/historical-batch") {
			t.Errorf("批量 K 线路径不对: %s", r.URL.Path)
		}
		_, _ = w.Write(body)
	})
	setFuyaoBase(t, s, srv.URL)
	series, err := s.FuyaoKline(context.Background(), []string{"600519", "000001"}, "1d", time.Time{}, time.Time{}, "forward", "stock", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("应返回 2 组序列，实际 %d", len(series))
	}
	if len(series[0].Bars) != 2 || series[0].Bars[1].Date != "2025-12-31" {
		t.Fatalf("K 线字段/日期不对: %+v", series[0])
	}
	if series[0].Bars[0].Volume != 3000000 {
		t.Fatalf("对外行保留官方成交量单位（股），实际 %v", series[0].Bars[0].Volume)
	}
	if len(series[1].Bars) != 0 {
		t.Fatalf("合法空组必须保留空 Bars，实际 %+v", series[1])
	}
	first := calls.Load()
	if _, err := s.FuyaoKline(context.Background(), []string{"600519", "000001"}, "1d", time.Time{}, time.Time{}, "forward", "stock", 10); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != first {
		t.Fatalf("K 线热缓存不应重复请求，实际新增 %d 次", calls.Load()-first)
	}
}

// TestFuyaoKlineChunking 验证 12 个代码按 10/批切分。
func TestFuyaoKlineChunking(t *testing.T) {
	var calls atomic.Int64
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ths := strings.Split(r.URL.Query().Get("thscodes"), ",")
		if len(ths) > 10 {
			t.Errorf("批量 K 线单批超过 10 个: %d", len(ths))
		}
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"interval":"1d","adjust":"forward","item":[]}}`))
	})
	setFuyaoBase(t, s, srv.URL)
	codes := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		codes = append(codes, fmt.Sprintf("%06d.SZ", 1+i))
	}
	if _, err := s.FuyaoKline(context.Background(), codes, "1d", time.Time{}, time.Time{}, "none", "stock", 5); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("12 个代码应分 2 批，实际 %d", calls.Load())
	}
}

// TestFuyaoCalendarAndTickers 验证基础域解析。
func TestFuyaoCalendarAndTickers(t *testing.T) {
	var path string
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		switch r.URL.Path {
		case "/api/a-share/calendar/trading-days":
			_, _ = w.Write([]byte(`{"code":0,"data":{"item":[{"date_ms":1762963200000,"date":"20251113"}]}}`))
		case "/api/meta/tickers/search":
			if r.URL.Query().Get("q") != "茅台" {
				t.Errorf("检索关键词必须正确编码: %q", r.URL.Query().Get("q"))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"item":[{"thscode":"600519.SH","ticker":"600519","name":"贵州茅台","exchange":"SSE","asset_type":"a_share"}]}}`))
		default:
			t.Errorf("未预期路径: %s", r.URL.Path)
		}
	})
	setFuyaoBase(t, s, srv.URL)
	days, err := s.FuyaoCalendar(context.Background(), "20251101", "20251130")
	if err != nil || len(days) != 1 || days[0].Date != "20251113" {
		t.Fatalf("交易日历解析不对: %+v err=%v", days, err)
	}
	if got := FuyaoCalendarText(days); len(got) != 1 || got[0] != "2025-11-13" {
		t.Fatalf("交易日历格式化不对: %+v", got)
	}
	tickers, err := s.FuyaoSearchTickers(context.Background(), "茅台", "", 5)
	if err != nil || len(tickers) != 1 || tickers[0].Name != "贵州茅台" {
		t.Fatalf("标的检索解析不对: %+v err=%v", tickers, err)
	}
	if path == "" {
		t.Fatal("必须真的发过请求")
	}
	// 检索命中后名称应进入进程内缓存，供快照补名复用。
	if name := s.cachedNameFast(Code{Symbol: "600519", Market: contract.MarketSH, Kind: KindStock}); name != "贵州茅台" {
		t.Fatalf("检索命中后应写入名称缓存，实际 %q", name)
	}
}

// TestFuyaoFuturesOptionsViews 验证期货/期权视图白名单与通用查询贯通。
func TestFuyaoFuturesOptionsViews(t *testing.T) {
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"timestamp":1794552984370,"item":[{"variety_code":"IC","name":"中证500"}]}}`))
	})
	setFuyaoBase(t, s, srv.URL)
	data, err := s.FuyaoFuturesView(context.Background(), "varieties", nil, 10)
	if err != nil || data.Count != 1 {
		t.Fatalf("期货视图失败: %+v err=%v", data, err)
	}
	if _, err := s.FuyaoOptionsView(context.Background(), "no-such-view", nil, 10); err == nil {
		t.Fatal("未知期权视图必须报错")
	}
	if _, err := s.FuyaoOtherView(context.Background(), "dump-10d", nil, 1); err != nil {
		t.Fatalf("全市场导出视图应可查询: %v", err)
	}
}
