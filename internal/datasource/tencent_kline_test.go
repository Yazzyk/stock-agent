package datasource

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

func TestTxFqKlineURL(t *testing.T) {
	address, err := txFqKlineURL("sh600519", contract.PeriodDay, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(address, "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=") {
		t.Fatalf("端点不符: %q", address)
	}
	if !strings.Contains(address, "param=sh600519,day,,,20,qfq") {
		t.Fatalf("参数必须保留字面逗号（转义后腾讯解析不出来）: %q", address)
	}
	if strings.Contains(address, "%2C") {
		t.Fatalf("逗号不能被 query-escape: %q", address)
	}
	if _, err := txFqKlineURL("sh600519", contract.PeriodWeek, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := txFqKlineURL("sh600519", contract.PeriodMonth, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := txFqKlineURL("sh600519", contract.PeriodMin60, 10); !errors.Is(err, ErrPeriodUnsupported) {
		t.Fatalf("分钟线应返回 ErrPeriodUnsupported，实际 %v", err)
	}
}

// fixture 是 curl 原样的真实响应（2026-11-10 收盘后，sh600519）。
func TestParseTxFqKlineDay(t *testing.T) {
	bars, err := parseTxFqKline(readTestdata(t, "tx_fqkline_600519_qfq_day.json"), "sh600519", contract.PeriodDay)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(bars) != 20 {
		t.Fatalf("K 线数量 = %d，期望 20", len(bars))
	}
	last := bars[len(bars)-1]
	if last.Date != "2026-11-10" {
		t.Fatalf("最后一根日期 = %q", last.Date)
	}
	// 字段顺序是「开收高低」：写错会把高低点算反，这里逐个钉住
	if math.Abs(last.Open-1438.23) > 1e-9 || math.Abs(last.Close-1439) > 1e-9 ||
		math.Abs(last.High-1444.5) > 1e-9 || math.Abs(last.Low-1431.26) > 1e-9 {
		t.Fatalf("OHLC = %v/%v/%v/%v", last.Open, last.High, last.Low, last.Close)
	}
	if math.Abs(last.Volume-23038) > 1e-9 {
		t.Fatalf("成交量 = %v 手，期望 23038（腾讯已是手，不能再除 100）", last.Volume)
	}
	// 成交额是估算值：close × volume(手) × 100
	if want := 1439.0 * 23038 * 100; math.Abs(last.Amount-want) > 1 {
		t.Fatalf("成交额估算 = %v，期望 %v", last.Amount, want)
	}
	for i, bar := range bars {
		if bar.Date == "" || bar.Open <= 0 || bar.Close <= 0 || bar.Volume <= 0 {
			t.Fatalf("第 %d 根字段不完整: %+v", i, bar)
		}
		if bar.High < bar.Open-1e-9 || bar.High < bar.Close-1e-9 {
			t.Fatalf("第 %d 根高点 %v 小于开/收 %v/%v（字段顺序可能接错）", i, bar.High, bar.Open, bar.Close)
		}
		if bar.Low > bar.Open+1e-9 || bar.Low > bar.Close+1e-9 {
			t.Fatalf("第 %d 根低点 %v 大于开/收 %v/%v", i, bar.Low, bar.Open, bar.Close)
		}
	}
}

func TestParseTxFqKlineWeekAndMonth(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		p    contract.Period
	}{
		{"周线", "tx_fqkline_600519_qfq_week.json", contract.PeriodWeek},
		{"月线", "tx_fqkline_600519_qfq_month.json", contract.PeriodMonth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bars, err := parseTxFqKline(readTestdata(t, tc.file), "sh600519", tc.p)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(bars) == 0 {
				t.Fatal("K 线为空")
			}
			if !strings.Contains(bars[0].Date, "-") {
				t.Fatalf("日期格式异常: %q", bars[0].Date)
			}
		})
	}
}

// 指数没有复权概念：腾讯对它只回 day/week/month，严格只认 qfq 键会拿不到数据，
// 允许回退到原始键后才能取到序列（原始 == 前复权）。
func TestParseTxFqKlineIndexRawFallback(t *testing.T) {
	raw := readTestdata(t, "tx_fqkline_sh000001_day.json")
	if _, err := parseTxFqKline(raw, "sh000001", contract.PeriodDay); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("严格模式应报 ErrEmptyResponse，实际 %v", err)
	}
	bars, err := parseTxKlineRows(raw, "sh000001", contract.PeriodDay, txKlineQfqThenRaw)
	if err != nil {
		t.Fatalf("指数回退解析失败: %v", err)
	}
	if len(bars) == 0 {
		t.Fatal("指数序列为空")
	}
	if bars[len(bars)-1].Close <= 0 {
		t.Fatalf("指数收盘价非法: %+v", bars[len(bars)-1])
	}
}

func TestParseTxFqKlineEmptyAndBadResponses(t *testing.T) {
	if _, err := parseTxFqKline(readTestdata(t, "tx_fqkline_empty.json"), "bj430047", contract.PeriodDay); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("无数据的标的应返回 ErrEmptyResponse，实际 %v", err)
	}
	cases := []struct{ name, body string }{
		{"空响应", ""},
		{"非 JSON", "<html>502</html>"},
		{"code 非 0", `{"code":-1,"msg":"参数错误","data":{}}`},
		{"没有该标的", `{"code":0,"data":{}}`},
		{"缺字段的行", `{"code":0,"data":{"sh600519":{"qfqday":[["2026-11-10","1438.23"]]}}}`},
		{"价格非法的行", `{"code":0,"data":{"sh600519":{"qfqday":[["2026-11-10","-","-","-","-","0"]]}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTxFqKline([]byte(tc.body), "sh600519", contract.PeriodDay); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}
	if _, err := parseTxFqKline([]byte(`{"code":0,"data":{}}`), "sh600519", contract.PeriodMin60); !errors.Is(err, ErrPeriodUnsupported) {
		t.Fatalf("分钟线应报 ErrPeriodUnsupported，实际 %v", err)
	}
}

// TestServiceBarsQfqFallsBackToTencent 是本次缺陷的回归：
// 东财 K 线端点返回空/失败时，qfq 必须由腾讯 fqkline 接住，而不是报 ErrAdjustUnsupported。
func TestServiceBarsQfqFallsBackToTencent(t *testing.T) {
	transport := standardRoutes(t)
	transport.addStatus("/stock/kline/get", http.StatusBadRequest)
	transport.add("app/fqkline/get?param=sh600519,day", readTestdata(t, "tx_fqkline_600519_qfq_day.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	bars, err := service.Bars(context.Background(), "600519", contract.PeriodDay, 20, contract.AdjustQfq)
	if err != nil {
		t.Fatalf("qfq 应降级到腾讯 fqkline，实际报错: %v", err)
	}
	if len(bars) != 20 {
		t.Fatalf("K 线数量 = %d，期望 20", len(bars))
	}
	if bars[len(bars)-1].Date != "2026-11-10" || bars[len(bars)-1].Close != 1439 {
		t.Fatalf("末根 = %+v", bars[len(bars)-1])
	}
	if transport.countCalls("fqkline/get") != 1 {
		t.Fatalf("腾讯兜底应被调用一次: %v", transport.calls)
	}
	// 结果要进缓存，第二次不再打上游
	if _, err := service.Bars(context.Background(), "600519", contract.PeriodDay, 20, contract.AdjustQfq); err != nil {
		t.Fatal(err)
	}
	if calls := transport.countCalls("fqkline/get"); calls != 1 {
		t.Fatalf("缓存未命中：腾讯请求数 = %d", calls)
	}
}

// TestServiceBarsHfqUsesTencentHfq 钉住「后复权只走后复权键」：
// 腾讯 fqkline 的 hfq 键可用（实测 param 末尾 hfq -> data.<symbol>.hfqday），
// 并且绝不能退化成拿 qfq 冒充（两者数值差异巨大）。
func TestServiceBarsHfqUsesTencentHfq(t *testing.T) {
	transport := standardRoutes(t)
	transport.add("day,,,20,hfq", readTestdata(t, "tx_fqkline_600000_hfq.json"))
	transport.add("day,,,20,qfq", readTestdata(t, "tx_fqkline_600000_qfq_day.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	bars, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 20, contract.AdjustHfq)
	if err != nil {
		t.Fatalf("后复权应可用: %v", err)
	}
	if len(bars) != 20 {
		t.Fatalf("K 线数量 = %d，期望 20", len(bars))
	}
	if calls := transport.countCalls(",hfq"); calls != 1 {
		t.Fatalf("后复权应请求 hfq 键 1 次，实际 %d", calls)
	}
	if calls := transport.countCalls(",qfq"); calls != 0 {
		t.Fatalf("后复权不得请求 qfq（%d 次）", calls)
	}
	// 后复权价一定高于原始价（600000 除权多次，累计上推明显）。
	rawBars, err := parseTxRawKline(readTestdata(t, "tx_fqkline_600000_raw_day.json"), "sh600000", contract.PeriodDay)
	if err != nil {
		t.Fatal(err)
	}
	if bars[0].Close <= rawBars[0].Close {
		t.Fatalf("后复权价 %v 应高于原始价 %v", bars[0].Close, rawBars[0].Close)
	}
}

// TestServiceBarsHfqUnavailable 覆盖后复权唯一源不可用时的明确报错。
func TestServiceBarsHfqUnavailable(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	_, err := service.Bars(context.Background(), "600519", contract.PeriodDay, 20, contract.AdjustHfq)
	if !errors.Is(err, ErrAdjustUnsupported) {
		t.Fatalf("后复权不可用时应返回 ErrAdjustUnsupported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "后复权") {
		t.Fatalf("错误文案应点明后复权，实际 %q", err.Error())
	}
}

// TestServiceBarsQfqMin60Unsupported: 腾讯没有分钟级复权序列，两个源都拿不到时必须如实报不支持。
func TestServiceBarsQfqMin60Unsupported(t *testing.T) {
	transport := standardRoutes(t)
	transport.addStatus("/stock/kline/get", http.StatusBadRequest)
	transport.add("app/fqkline/get", readTestdata(t, "tx_fqkline_600519_qfq_day.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	_, err := service.Bars(context.Background(), "600519", contract.PeriodMin60, 20, contract.AdjustQfq)
	if !errors.Is(err, ErrAdjustUnsupported) {
		t.Fatalf("60 分钟前复权应返回 ErrAdjustUnsupported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "只支持日/周/月") {
		t.Fatalf("错误文案应说明周期限制，实际 %q", err.Error())
	}
}

// TestServiceBarsQfqIndexUsesRawSequence: 指数没有复权概念，腾讯回原始序列，可以直接采信。
func TestServiceBarsQfqIndexUsesRawSequence(t *testing.T) {
	transport := standardRoutes(t)
	transport.addStatus("/stock/kline/get", http.StatusBadRequest)
	transport.add("app/fqkline/get?param=sh000001", readTestdata(t, "tx_fqkline_sh000001_day.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	bars, err := service.Bars(context.Background(), "sh000001", contract.PeriodDay, 10, contract.AdjustQfq)
	if err != nil {
		t.Fatalf("指数 qfq 应可用: %v", err)
	}
	if len(bars) == 0 {
		t.Fatal("指数 K 线为空")
	}
}

func TestTxKlineURLRawAndQfq(t *testing.T) {
	rawURL, err := txRawKlineURL("sh600519", contract.PeriodDay, 120)
	if err != nil {
		t.Fatal(err)
	}
	// 不复权 = 位置参数最后一段留空，不能出现 qfq
	if !strings.HasSuffix(rawURL, "param=sh600519,day,,,120,") {
		t.Fatalf("不复权 URL = %q", rawURL)
	}
	if strings.Contains(rawURL, "qfq") || strings.Contains(rawURL, "%2C") {
		t.Fatalf("不复权 URL 不能带 qfq 也不能转义逗号: %q", rawURL)
	}
	qfqURL, err := txFqKlineURL("sh600519", contract.PeriodDay, 120)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(qfqURL, "param=sh600519,day,,,120,qfq") {
		t.Fatalf("前复权 URL = %q", qfqURL)
	}
	if _, err := txRawKlineURL("sh600519", contract.PeriodMin60, 10); !errors.Is(err, ErrPeriodUnsupported) {
		t.Fatalf("不复权分钟线应返回 ErrPeriodUnsupported，实际 %v", err)
	}
}

// TestParseTxRawKline 用真实响应证明取到的是**未复权**价：
// 2026-06-24 原始收盘 1207.68，而同日腾讯 qfq 是 1179.656（除权日 2026-06-26 之前）。
func TestParseTxRawKline(t *testing.T) {
	bars, err := parseTxRawKline(readTestdata(t, "tx_fqkline_600519_raw_day.json"), "sh600519", contract.PeriodDay)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(bars) != 120 {
		t.Fatalf("K 线数量 = %d，期望 120", len(bars))
	}
	var target *contract.Bar
	for i := range bars {
		if bars[i].Date == "2026-06-24" {
			target = &bars[i]
		}
	}
	if target == nil {
		t.Fatal("fixture 里应包含 2026-06-24")
	}
	if math.Abs(target.Close-1207.68) > 1e-6 {
		t.Fatalf("2026-06-24 收盘 = %v，期望原始价 1207.68", target.Close)
	}
	if math.Abs(target.Close-1179.656) < 1e-6 {
		t.Fatal("不能用前复权价冒充不复权（1179.656 是 qfq）")
	}
	// 反向：给一份只带 qfq 键的响应，raw 解析必须拿不到数据（不允许拿 qfq 冒充 raw）
	if _, err := parseTxRawKline(readTestdata(t, "tx_fqkline_600519_qfq_day.json"), "sh600519", contract.PeriodDay); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("qfq 响应不该被当作不复权解析，实际 %v", err)
	}
}

// TestServiceBarsNoneFallsBackToTencentRaw 是本次口径修正的回归：
// 东财 400 后，adj=none 必须由腾讯**原始**序列接住（价格=raw），且不能去请求 qfq 端点。
func TestServiceBarsNoneFallsBackToTencentRaw(t *testing.T) {
	transport := standardRoutes(t)
	transport.addStatus("/stock/kline/get", http.StatusBadRequest)
	// 两条路由都配上，用来区分「请求了哪一条」：qfq 请求会命中带 ,qfq 的那条
	transport.add("day,,,120,qfq", readTestdata(t, "tx_fqkline_600519_qfq_day.json"))
	transport.add("day,,,120,", readTestdata(t, "tx_fqkline_600519_raw_day.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	bars, err := service.Bars(context.Background(), "600519", contract.PeriodDay, 120, contract.AdjustNone)
	if err != nil {
		t.Fatalf("不复权应降级到腾讯原始序列: %v", err)
	}
	if len(bars) != 120 {
		t.Fatalf("K 线数量 = %d，期望 120", len(bars))
	}
	found := false
	for _, bar := range bars {
		if bar.Date == "2026-06-24" {
			found = true
			if math.Abs(bar.Close-1207.68) > 1e-6 {
				t.Fatalf("2026-06-24 收盘 = %v，期望原始价 1207.68（不是 qfq 1179.656）", bar.Close)
			}
		}
	}
	if !found {
		t.Fatal("结果里缺少 2026-06-24")
	}
	if calls := transport.countCalls(",qfq"); calls != 0 {
		t.Fatalf("不复权路径不得请求前复权端点（%d 次）", calls)
	}
	if calls := transport.countCalls("/v6/line/"); calls != 0 {
		t.Fatalf("腾讯原始可用时不该再落到同花顺兜底（%d 次）", calls)
	}
}

// TestServiceBarsNoneFallsBackToTHSLast：腾讯原始与新浪日线都失败时，才允许用同花顺兜底（口径降级）。
func TestServiceBarsNoneFallsBackToTHSLast(t *testing.T) {
	// 只配同花顺路由：腾讯与新浪都不在表里 -> 它们的请求一律 400（fakeTransport 的默认行为）。
	transport := (&fakeTransport{}).
		add("/v6/line/hs_600000/01/2026.js", readTestdata(t, "ths_line_600000_2026.js")).
		add("/v6/line/hs_600000/01/2025.js", readTestdata(t, "ths_line_600000_2025.js"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	bars, err := service.Bars(context.Background(), "600000", contract.PeriodDay, 30, contract.AdjustNone)
	if err != nil {
		t.Fatalf("最后兜底应可用: %v", err)
	}
	if len(bars) != 30 || bars[len(bars)-1].Close != 10.20 {
		t.Fatalf("末根 = %+v，期望同花顺 fixture 收 10.20", bars[len(bars)-1])
	}
	if transport.countCalls("/v6/line/") == 0 {
		t.Fatal("腾讯与新浪都失败时应走同花顺兜底")
	}
}
