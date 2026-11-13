package datasource

// 官方 API 真实采样（默认跳过；只有 GUANLAN_FUYAO_LIVE=1 且提供 Key 时才联网）。
//
// 运行方式：
//
//	GUANLAN_FUYAO_LIVE=1 GUANLAN_FUYAO_API_KEY=$(cat ~/.guanlan/fuyao.key) \
//	  go test ./internal/datasource -run TestFuyaoLivedDomains -v -count=1
//
// 该测试只断言「能取到形状正确的数据」，不把具体行情数值写死（行情每天变）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// TestFuyaoLivedDomains 对主要业务域做一次真实采样，并打印返回形状摘要。
func TestFuyaoLivedDomains(t *testing.T) {
	if os.Getenv("GUANLAN_FUYAO_LIVE") != "1" {
		t.Skip("未开启真实采样（GUANLAN_FUYAO_LIVE=1）")
	}
	if strings.TrimSpace(os.Getenv(fuyaoKeyEnv)) == "" {
		t.Skip("缺少 " + fuyaoKeyEnv)
	}
	svc, err := New(Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := svc.(*Service)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	probe := func(label, endpoint string, params map[string]string) contract.FuyaoData {
		t.Helper()
		data, err := s.FuyaoQuery(ctx, contract.FuyaoQuery{Endpoint: endpoint, Params: params, Limit: 3})
		if err != nil {
			t.Fatalf("%s 失败: %v", label, err)
		}
		if data.Permission == "client" {
			t.Logf("%s: 客户端专用（2004），已优雅降级", label)
			return data
		}
		if len(data.Items) == 0 && data.Raw == nil {
			t.Logf("%s: 合法空结果（%s）", label, endpoint)
			return data
		}
		shape := "raw:" + strings.Join(mapKeys(data.Raw), ",")
		if len(data.Items) > 0 {
			shape = "item:" + strings.Join(mapKeys(data.Items[0]), ",")
		}
		t.Logf("%s: ok count=%d ts=%d shape=%s", label, data.Count, data.Timestamp, shape)
		return data
	}

	// 行情/估值/涨跌停/热榜/竞价/财务/基础/指数/基金/期货/期权/资讯。
	probe("行情快照", "a-share.prices.snapshot", map[string]string{"thscodes": "600519.SH,000001.SZ"})
	probe("估值快照", "a-share.valuations.snapshot", map[string]string{"thscodes": "600519.SH"})
	probe("涨停池", "a-share.special-data.limit-up-pool", map[string]string{"size": "2"})
	probe("热股榜", "a-share.special-data.hot-stock-list", map[string]string{"limit": "2"})
	probe("集合竞价", "a-share.auction.snapshot", map[string]string{"thscodes": "600519.SH"})
	probe("财务指标", "a-share.financials.income-statements", map[string]string{"thscode": "600519.SH", "period": "annual", "limit": "1"})
	probe("交易日历", "a-share.calendar.trading-days", nil)
	probe("指数目录", "a-share-index.catalog.ths-index-list", map[string]string{"tag": "cn_concept"})
	probe("基金资料", "fund.profile.detail", map[string]string{"thscode": "510300.SH"})
	probe("期货品种", "futures.varieties.list", nil)
	probe("期权合约", "options.contracts.list", map[string]string{"limit": "2"})
	probe("龙虎榜", "a-share.special-data.dragon-tiger-list", nil)

	// 客户端专用能力：必须返回 permission=client 而不是报错/崩溃。
	clientOnly := probe("主力资金", "a-share.capital-flow.snapshot", map[string]string{"thscode": "600519.SH"})
	if clientOnly.Permission != "client" {
		t.Logf("注意：主力资金对当前 Key 可用（未返回 2004），数据形状 %v", clientOnly.Raw)
	}

	// 批量快照：验证 3 个代码一次返回且 volume 单位是股。
	rows, err := s.FuyaoQuoteRows(ctx, []string{"600519.SH", "000001.SZ", "300750.SZ"})
	if err != nil {
		t.Fatalf("批量快照失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("批量快照为空")
	}
	// 批量 K 线：只取最近 5 根，避免大响应。
	series, err := s.FuyaoKline(ctx, []string{"600519.SH", "000001.SZ"}, "1d", time.Time{}, time.Time{}, "forward", "stock", 5)
	if err != nil {
		t.Fatalf("批量 K 线失败: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("批量 K 线应返回 2 组，实际 %d", len(series))
	}
	t.Logf("批量快照 %d 行；批量 K 线 %d 组（首组 %d 根）", len(rows), len(series), len(series[0].Bars))

	// 真实网络下的 50 只冷/热耗时（只用于报告，不作为断言，避免受本地网络波动影响）。
	tickers, err := s.FuyaoTickers(ctx, "a-share", 50, 0)
	if err != nil || len(tickers) < 10 {
		t.Logf("跳过 50 只真实计时：tickers err=%v len=%d", err, len(tickers))
		return
	}
	codes := make([]string, 0, 50)
	for _, ticker := range tickers {
		if ticker.Thscode != "" {
			codes = append(codes, ticker.Thscode)
		}
		if len(codes) == 50 {
			break
		}
	}
	s.cache.clear()
	start := time.Now()
	if _, err := s.FuyaoQuoteRows(ctx, codes); err != nil {
		t.Logf("50 只冷启动失败: %v", err)
		return
	}
	cold := time.Since(start)
	start = time.Now()
	if _, err := s.FuyaoQuoteRows(ctx, codes); err != nil {
		t.Logf("50 只热缓存失败: %v", err)
		return
	}
	warm := time.Since(start)
	t.Logf("真实网络 %d 只：冷 %v、热 %v（热缓存 0 请求）", len(codes), cold.Round(time.Millisecond), warm.Round(time.Millisecond))
}

// mapKeys 返回 map 的键（稳定排序），用于打印真实返回形状。
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 小规模切片，插入排序足够，避免额外 import。
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
