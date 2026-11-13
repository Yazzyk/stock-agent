package datasource

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖「多日资金流 / 批量资金流 / 板块资金流」在新浪口径下的行为。
// 所有用例都用 testdata 里的真实响应离线跑，不联网。

func TestParseSinaStockFlowHistoryRealResponse(t *testing.T) {
	history, err := parseSinaStockFlowHistory(readTestdata(t, "sina_flow_history_sh600000.json"), "600000", 20)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if history.Source != sourceSina {
		t.Fatalf("来源 = %q，期望 %q", history.Source, sourceSina)
	}
	if len(history.Points) == 0 {
		t.Fatal("应解析出至少一天")
	}
	// 上游倒序，解析后必须升序：首日日期要小于末日日期。
	first, last := history.Points[0], history.Points[len(history.Points)-1]
	if first.Date >= last.Date {
		t.Fatalf("时间轴未升序: %s >= %s", first.Date, last.Date)
	}
	// 交叉校验：取一份真实响应里的 2026-11-10 那根，r0_net=49438357.05，
	// r0_ratio=0.10191916（=> 10.19%），trade=10.20，changeratio=0.00990099（=> 0.99%）。
	var found bool
	for _, point := range history.Points {
		if point.Date != "2026-11-10" {
			continue
		}
		found = true
		if diff := point.MainNet - 49438357.05; diff > 1 || diff < -1 {
			t.Fatalf("MainNet = %v，期望 49438357.05", point.MainNet)
		}
		if point.MainPct < 10.19 || point.MainPct > 10.20 {
			t.Fatalf("MainPct = %v，期望约 10.19", point.MainPct)
		}
		if point.Close != 10.20 {
			t.Fatalf("Close = %v，期望 10.20", point.Close)
		}
		if point.ChangePct < 0.98 || point.ChangePct > 1.0 {
			t.Fatalf("ChangePct = %v，期望约 0.99", point.ChangePct)
		}
	}
	if !found {
		t.Fatal("响应里应有 2026-11-10 这一天")
	}
}

func TestParseSinaStockFlowHistoryEmptyAndBad(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"空数组", "[]"},
		{"null", "null"},
		{"空串", ""},
		{"缺日期字段", `[{"r0_net":"1","trade":"1"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSinaStockFlowHistory([]byte(tc.body), "600000", 20); !errors.Is(err, ErrEmptyResponse) {
				t.Fatalf("应返回 ErrEmptyResponse，实际 %v", err)
			}
		})
	}
	t.Run("畸形 JSON", func(t *testing.T) {
		if _, err := parseSinaStockFlowHistory([]byte("{not json"), "600000", 20); err == nil {
			t.Fatal("畸形 JSON 应报错")
		}
	})
}

func TestParseSinaStockFlowHistoryTruncatesDays(t *testing.T) {
	body := readTestdata(t, "sina_flow_history_sh600000.json")
	history, err := parseSinaStockFlowHistory(body, "600000", 3)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(history.Points) != 3 {
		t.Fatalf("应只保留最近 3 天，实际 %d 天", len(history.Points))
	}
}

func TestFundFlowHistoryFromRoute(t *testing.T) {
	transport := (&fakeTransport{}).
		add("ssl_qsfx_zjlrqs", readTestdata(t, "sina_flow_history_sh600000.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	history, err := service.FundFlowHistory(context.Background(), "sh600000", 20)
	if err != nil {
		t.Fatalf("FundFlowHistory 失败: %v", err)
	}
	if history.Code != "600000" || history.Days != 20 || history.Source != sourceSina {
		t.Fatalf("返回值不符: %+v", history)
	}
	if transport.countCalls("ssl_qsfx_zjlrqs") != 1 {
		t.Fatalf("应请求 1 次新浪多日接口，实际 %d", transport.countCalls("ssl_qsfx_zjlrqs"))
	}
	if transport.countCalls("no_such_route") != 0 {
		t.Fatal("不应有其它请求")
	}
}

// TestFundFlowHistoryNoFallbackToOtherSource 保证多日资金流只有新浪一条链路：
// 上游失败时直接报错，不会偷偷回退到别的源给出不同口径的数字。
func TestFundFlowHistoryNoFallbackToOtherSource(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	if _, err := service.FundFlowHistory(context.Background(), "sh600000", 20); err == nil {
		t.Fatal("上游不可用时应报错")
	}
}

func TestFundFlowHistoryRejectsBadCode(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	if _, err := service.FundFlowHistory(context.Background(), "not-a-code", 20); err == nil {
		t.Fatal("非法代码应报错")
	}
}

// TestFundFlowSnapshotBatch 覆盖并发批量：每个代码各打一次新浪当日接口。
func TestFundFlowSnapshotBatch(t *testing.T) {
	transport := (&fakeTransport{}).
		add("ssi_ssfx_flzjtj?daima=sh600000", readTestdata(t, "sina_flow_600000.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	flows, err := service.FundFlowSnapshot(context.Background(), []string{"sh600000"})
	if err != nil {
		t.Fatalf("FundFlowSnapshot 失败: %v", err)
	}
	if len(flows) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(flows))
	}
	if flows[0].Source != sourceSina || flows[0].Kind != "stock" {
		t.Fatalf("来源/类型不符: %+v", flows[0])
	}
	if transport.countCalls("ssi_ssfx_flzjtj") != 1 {
		t.Fatalf("应请求 1 次，实际 %d", transport.countCalls("ssi_ssfx_flzjtj"))
	}
}

// TestFundFlowSnapshotPartialFailure 覆盖 ErrPartial：成功项照常返回，失败代码进错误里。
func TestFundFlowSnapshotPartialFailure(t *testing.T) {
	transport := (&fakeTransport{}).
		add("ssi_ssfx_flzjtj?daima=sh600000", readTestdata(t, "sina_flow_600000.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	flows, err := service.FundFlowSnapshot(context.Background(), []string{"sh600000", "sz000858"})
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("应返回 ErrPartial，实际 %v", err)
	}
	if len(flows) != 1 || flows[0].Code != "600000" {
		t.Fatalf("应只返回成功的那一只，实际 %+v", flows)
	}
	if !strings.Contains(err.Error(), "000858") {
		t.Fatalf("错误里应点名失败的代码，实际 %v", err)
	}
}

func TestFundFlowSnapshotAllFail(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	if _, err := service.FundFlowSnapshot(context.Background(), []string{"600519", "000858"}); !errors.Is(err, ErrNoData) {
		t.Fatalf("全部失败时应返回 ErrNoData，实际 %v", err)
	}
}

func TestFundFlowSnapshotRejectsTooManyCodes(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	codes := make([]string, 0, maxFlowCodes+1)
	for i := 0; i < maxFlowCodes+1; i++ {
		codes = append(codes, "600000")
	}
	if _, err := service.FundFlowSnapshot(context.Background(), codes); err == nil {
		t.Fatal("超过上限应报错")
	}
}

// TestFundFlowSnapshotReusesCache 覆盖 60s 缓存：重复调用不重复打上游。
func TestFundFlowSnapshotReusesCache(t *testing.T) {
	transport := (&fakeTransport{}).
		add("ssi_ssfx_flzjtj?daima=sh600000", readTestdata(t, "sina_flow_600000.json"))
	service, clock := newServiceWithTransport(t, newMemStore(), transport)

	if _, err := service.FundFlowSnapshot(context.Background(), []string{"sh600000"}); err != nil {
		t.Fatalf("首次批量失败: %v", err)
	}
	if _, err := service.FundFlowSnapshot(context.Background(), []string{"sh600000"}); err != nil {
		t.Fatalf("二次批量失败: %v", err)
	}
	if calls := transport.countCalls("ssi_ssfx_flzjtj"); calls != 1 {
		t.Fatalf("缓存期内应只请求 1 次，实际 %d", calls)
	}
	clock.Advance(2 * time.Minute)
	if _, err := service.FundFlowSnapshot(context.Background(), []string{"sh600000"}); err != nil {
		t.Fatalf("过期后批量失败: %v", err)
	}
	if calls := transport.countCalls("ssi_ssfx_flzjtj"); calls != 2 {
		t.Fatalf("缓存过期后应重新请求，实际累计 %d 次", calls)
	}
}

// ---------- 板块多日资金流（新浪 category） ----------

func TestSectorFundFlowHistory(t *testing.T) {
	transport := (&fakeTransport{}).
		add("ssl_bkzj_zjlrqs", readTestdata(t, "sina_bkzj_zjlrqs_new_dzxx.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	history, err := service.SectorFundFlowHistory(context.Background(), "new_dzxx", 20)
	if err != nil {
		t.Fatalf("SectorFundFlowHistory 失败: %v", err)
	}
	if history.Code != "new_dzxx" || history.Source != sourceSina {
		t.Fatalf("返回值不符: %+v", history)
	}
	if len(history.Points) == 0 {
		t.Fatal("应有至少一天")
	}
	if history.Points[0].Date >= history.Points[len(history.Points)-1].Date {
		t.Fatal("时间轴应升序")
	}
	if calls := transport.countCalls("bankuai=new_dzxx"); calls != 1 {
		t.Fatalf("应按 bankuai 参数请求 1 次，实际 %d", calls)
	}
}

func TestSectorFundFlowHistoryClampsDays(t *testing.T) {
	transport := (&fakeTransport{}).
		add("ssl_bkzj_zjlrqs", readTestdata(t, "sina_bkzj_zjlrqs_new_dzxx.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	history, err := service.SectorFundFlowHistory(context.Background(), "new_dzxx", 9999)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if history.Days != maxSectorFlowDays {
		t.Fatalf("天数应被收敛到 %d，实际 %d", maxSectorFlowDays, history.Days)
	}
}

// TestSectorFundFlowHistoryRejectsLegacyBKCodes 覆盖旧东财 BK 代码的可操作错误。
func TestSectorFundFlowHistoryRejectsLegacyBKCodes(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	for _, code := range []string{"BK1201", "90.BK1201", "bk1201"} {
		_, err := service.SectorFundFlowHistory(context.Background(), code, 20)
		if err == nil {
			t.Fatalf("%q 应报错", code)
		}
		if !errors.Is(err, errLegacySectorCode) {
			t.Fatalf("%q 的错误应是 errLegacySectorCode，实际 %v", code, err)
		}
		if !strings.Contains(err.Error(), "new_dzxx") || !strings.Contains(err.Error(), "sector.list") {
			t.Fatalf("%q 的错误应引导到新浪口径，实际 %v", code, err)
		}
	}
}

func TestSectorFundFlowHistoryRejectsStockCodes(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	if _, err := service.SectorFundFlowHistory(context.Background(), "600519", 20); err == nil {
		t.Fatal("纯数字股票代码不能当板块 category")
	}
}

func TestSectorFundFlowBatchPartialFailure(t *testing.T) {
	transport := (&fakeTransport{}).
		add("bankuai=new_dzxx", readTestdata(t, "sina_bkzj_zjlrqs_new_dzxx.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	histories, err := service.SectorFundFlowBatch(context.Background(), []string{"new_dzxx", "gn_hwqc"}, 10)
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("应返回 ErrPartial，实际 %v", err)
	}
	if len(histories) != 1 || histories[0].Code != "new_dzxx" {
		t.Fatalf("应只返回成功的那一个，实际 %+v", histories)
	}
}

func TestSectorFundFlowBatchAllFail(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	if _, err := service.SectorFundFlowBatch(context.Background(), []string{"new_dzxx", "gn_hwqc"}, 20); !errors.Is(err, ErrNoData) {
		t.Fatalf("全部失败时应返回 ErrNoData，实际 %v", err)
	}
}

func TestSectorFundFlowBatchAllInvalidStillNoData(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	_, err := service.SectorFundFlowBatch(context.Background(), []string{"600519", "90.BK1201"}, 5)
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("应返回 ErrNoData，实际 %v", err)
	}
	if !errors.Is(err, errLegacySectorCode) {
		t.Fatalf("错误里应带旧板块代码提示，实际 %v", err)
	}
}

func TestSectorFundFlowBatchDeduplicatesCategories(t *testing.T) {
	transport := (&fakeTransport{}).
		add("bankuai=new_dzxx", readTestdata(t, "sina_bkzj_zjlrqs_new_dzxx.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	histories, err := service.SectorFundFlowBatch(context.Background(), []string{"new_dzxx", " NEW_DZXX ", "new_dzxx"}, 10)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("去重后应只有 1 条，实际 %d", len(histories))
	}
}

func TestSectorFundFlowBatchConcurrency(t *testing.T) {
	transport := (&fakeTransport{}).
		add("bankuai=new_dzxx", readTestdata(t, "sina_bkzj_zjlrqs_new_dzxx.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	// 填满批量上限（maxSectorFlowCodes 个），全部命中同一份 fixture。
	codes := make([]string, 0, maxSectorFlowCodes)
	for i := 0; i < maxSectorFlowCodes; i++ {
		codes = append(codes, "new_dzxx")
	}
	histories, err := service.SectorFundFlowBatch(context.Background(), codes, 5)
	if err != nil {
		t.Fatalf("并发批量失败: %v", err)
	}
	if len(histories) != 1 {
		t.Fatalf("去重后应只有 1 条成功项，实际 %d", len(histories))
	}
	// 缓存命中后只应真正出网一次（同一 category）。
	if calls := transport.countCalls("ssl_bkzj_zjlrqs"); calls != 1 {
		t.Fatalf("相同 category 应只请求 1 次，实际 %d", calls)
	}
}
