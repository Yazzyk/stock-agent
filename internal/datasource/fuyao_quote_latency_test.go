package datasource

// 自选股列表取数链路的「前后对比」计时（任务 1 验收表）。
//
// 旧链路（legacyQuoteSimulation）复刻改造前的行为：
//  1. 官方快照一次（逐 50/批、串行）；
//  2. 无论官方是否命中，都再打一次腾讯批量快照（用于交叉校验/补名称）；
//  3. 逐只 saveCachedQuote 落盘（50 只 = 50 次 sqlite 写）。
//
// 新链路（Quote）：
//   - 官方命中后不再请求腾讯（名称优先读内存/本地缓存）；
//   - 批量结果进短 TTL 缓存，热缓存零请求；
//   - 落盘按 30 秒节流。
//
// 计时用 httptest 注入 25ms 固定延迟，数字用于比较**相对差异**，不代表真实公网 RTT。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// fuyaoLatencyCodes 生成 n 个沪市代码。
func fuyaoLatencyCodes(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, fmt.Sprintf("%06d", 600000+i))
	}
	return out
}

// newLatencyService 构造带固定延迟的测试服务，并预置名称缓存（避免补名走腾讯）。
func newLatencyService(t *testing.T, codes []string, calls *atomic.Int64) *Service {
	t.Helper()
	latency := 25 * time.Millisecond
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(latency)
		ths := strings.Split(r.URL.Query().Get("thscodes"), ",")
		items := make([]map[string]any, 0, len(ths))
		for _, code := range ths {
			items = append(items, map[string]any{
				"thscode": code, "ticker": strings.Split(code, ".")[0],
				"volume": 3142572, "turnover": 5401389334.87,
				"last_price": 1602.6, "price_change": 21.8, "price_change_ratio_pct": 1.74,
				"open_price": 1611.6, "high_price": 1626.6, "low_price": 1601.7, "prev_price": 1580.8,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "message": "success",
			"data": map[string]any{"timestamp": 1794552945000, "item": items},
		})
	})
	setFuyaoBase(t, s, srv.URL)
	for _, raw := range codes {
		code, err := ParseCode(raw)
		if err != nil {
			t.Fatal(err)
		}
		s.rememberName(code, "测试标的"+code.Symbol)
	}
	return s
}

// legacyQuoteSimulation 复刻改造前的两次串行请求 + 逐只落盘。
func legacyQuoteSimulation(ctx context.Context, s *Service, codes []string) {
	parsed := make([]Code, 0, len(codes))
	for _, raw := range codes {
		if code, err := ParseCode(raw); err == nil {
			parsed = append(parsed, code)
		}
	}
	// 第一跳：官方快照。
	for start := 0; start < len(parsed); start += fuyaoQuoteChunk {
		end := start + fuyaoQuoteChunk
		if end > len(parsed) {
			end = len(parsed)
		}
		chunk := parsed[start:end]
		ths := make([]string, 0, len(chunk))
		for _, code := range chunk {
			ths = append(ths, code.THSCode())
		}
		var data fuyaoSnapshotData
		_ = s.fuyaoGetWith(ctx, "/api/a-share/prices/snapshot", map[string]string{
			"thscodes": strings.Join(ths, ","),
		}, &data)
	}
	// 第二跳：旧链路固定再打一次批量兜底（这里用同一端点模拟腾讯的 RTT）。
	var second fuyaoSnapshotData
	ths := make([]string, 0, len(parsed))
	for _, code := range parsed {
		ths = append(ths, code.THSCode())
	}
	_ = s.fuyaoGetWith(ctx, "/api/a-share/prices/snapshot", map[string]string{
		"thscodes": strings.Join(ths, ","),
	}, &second)
	// 逐只落盘（旧链路没有节流）。
	for _, code := range parsed {
		s.saveCachedQuote(code, contract.Quote{
			Code: code.Symbol, Name: "测试标的" + code.Symbol, Market: code.Market,
			Price: 1602.6, PrevClose: 1580.8, Volume: 31425.72, Amount: 5401389334.87,
			Time: "15:00:00", Source: sourceFuyao,
		})
	}
}

// TestWatchlistQuoteLatency 输出冷/热缓存与旧链路的耗时对比（10/30/50 只）。
func TestWatchlistQuoteLatency(t *testing.T) {
	t.Logf("%-6s %-14s %-10s %-12s %-10s %-12s %-10s", "只数", "旧链路", "旧请求数", "新-冷缓存", "冷请求数", "新-热缓存", "热请求数")
	for _, n := range []int{10, 30, 50} {
		codes := fuyaoLatencyCodes(n)

		var legacyCalls atomic.Int64
		legacySvc := newLatencyService(t, codes, &legacyCalls)
		start := time.Now()
		legacyQuoteSimulation(context.Background(), legacySvc, codes)
		legacyElapsed := time.Since(start)

		var newCalls atomic.Int64
		newSvc := newLatencyService(t, codes, &newCalls)
		start = time.Now()
		if _, err := newSvc.Quote(context.Background(), codes); err != nil {
			t.Fatalf("新链路冷启动失败: %v", err)
		}
		coldElapsed := time.Since(start)
		coldCalls := newCalls.Load()

		start = time.Now()
		if _, err := newSvc.Quote(context.Background(), codes); err != nil {
			t.Fatalf("新链路热缓存失败: %v", err)
		}
		warmElapsed := time.Since(start)
		warmCalls := newCalls.Load() - coldCalls

		t.Logf("%-6d %-14s %-10d %-12s %-10d %-12s %-10d",
			n, legacyElapsed.Round(time.Millisecond), legacyCalls.Load(),
			coldElapsed.Round(time.Millisecond), coldCalls,
			warmElapsed.Round(time.Millisecond), warmCalls)
	}
}
