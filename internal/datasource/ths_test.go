package datasource

import (
	"math"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 三份 realhead fixture（个股 / 上证指数 / 板块）都做过交叉校验：
// 价格、成交量、成交额与腾讯、新浪同刻快照一致，见 docs/PLAN.md §4。
func TestParseTHSRealheadStock(t *testing.T) {
	code, err := ParseCode("600000")
	if err != nil {
		t.Fatal(err)
	}
	head, err := parseTHSRealhead(readTestdata(t, "ths_realhead_stock600000.js"), code)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if head.Name != "浦发银行" {
		t.Fatalf("名称 = %q，期望 浦发银行", head.Name)
	}
	if head.Code != "600000" {
		t.Fatalf("代码 = %q，期望 600000", head.Code)
	}
	if head.Market != contract.MarketSH {
		t.Fatalf("市场 = %q，期望 sh", head.Market)
	}
	// 关键字段逐一核对（与腾讯/新浪同刻一致）
	cases := []struct {
		label string
		got   float64
		want  float64
	}{
		{"现价", head.Price, 10.20},
		{"昨收", head.PrevClose, 10.10},
		{"今开", head.Open, 10.06},
		{"最高", head.High, 10.24},
		{"最低", head.Low, 10.00},
		{"涨跌额", head.Change, 0.10},
		{"涨跌幅", head.ChangePct, 0.99},
		{"成交额", head.Amount, 485074220},
		{"换手率", head.Turnover, 0.144},
		{"振幅", head.Amplitude, 2.376},
		{"量比", head.VolumeRatio, 0.665},
	}
	for _, tc := range cases {
		if math.Abs(tc.got-tc.want) > 1e-6 {
			t.Fatalf("%s = %v，期望 %v", tc.label, tc.got, tc.want)
		}
	}
	// 成交量：同花顺给「股」，对外必须换成「手」
	if math.Abs(head.VolumeHand-477826.52) > 1e-6 {
		t.Fatalf("成交量 = %v 手，期望 477826.52 手（47,782,652 股 ÷ 100）", head.VolumeHand)
	}
	if head.Time != "2026-11-10T15:30:00+08:00" {
		t.Fatalf("时间 = %q，期望 2026-11-10T15:30:00+08:00", head.Time)
	}
	if head.Status != "闭市" {
		t.Fatalf("stockStatus = %q，期望 闭市（实测收盘后取值）", head.Status)
	}
}

func TestParseTHSRealheadIndex(t *testing.T) {
	code, err := ParseCode("sh000001")
	if err != nil {
		t.Fatal(err)
	}
	head, err := parseTHSRealhead(readTestdata(t, "ths_realhead_index1A0001.js"), code)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if head.Name != "上证指数" {
		t.Fatalf("名称 = %q，期望 上证指数", head.Name)
	}
	if head.Kind != KindIndex {
		t.Fatalf("类别 = %q，期望 index", head.Kind)
	}
	if math.Abs(head.Price-3663.72) > 1e-6 {
		t.Fatalf("现价 = %v，期望 3663.72", head.Price)
	}
	// 指数成交量同样是「股」：40,486,518,000 股 ÷ 100 = 404,865,180 手（腾讯同刻给 404,865,177 手）
	if math.Abs(head.VolumeHand-404865180) > 1 {
		t.Fatalf("指数成交量 = %v 手，期望约 404,865,180 手", head.VolumeHand)
	}
	if math.Abs(head.Amount-673951890000) > 1 {
		t.Fatalf("指数成交额 = %v，期望 673,951,890,000", head.Amount)
	}
	if head.Status != "--" {
		t.Fatalf("指数 stockStatus = %q，期望 --（实测取值）", head.Status)
	}
}

func TestParseTHSRealheadSector(t *testing.T) {
	code, err := ParseCode("bk_881101")
	if err != nil {
		t.Fatal(err)
	}
	head, err := parseTHSRealhead(readTestdata(t, "ths_realhead_sector881101.js"), code)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if head.Name != "种植业与林业" {
		t.Fatalf("名称 = %q，期望 种植业与林业", head.Name)
	}
	if head.Kind != KindSector {
		t.Fatalf("类别 = %q，期望 sector", head.Kind)
	}
	if math.Abs(head.Price-4072.614) > 1e-6 {
		t.Fatalf("现价 = %v，期望 4072.614", head.Price)
	}
	if math.Abs(head.VolumeHand-10561118.5) > 1e-6 {
		t.Fatalf("板块成交量 = %v 手，期望 10,561,118.5 手", head.VolumeHand)
	}
}

func TestParseTHSRealheadErrors(t *testing.T) {
	code, _ := ParseCode("600000")
	cases := []struct {
		name string
		body string
	}{
		{"空响应", ""},
		{"非 JSONP", "hello world"},
		{"items 为空", `cb({"items":{}})`},
		{"items 缺失", `cb({"data":1})`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTHSRealhead([]byte(tc.body), code); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}
}

func TestParseTHSLineDay(t *testing.T) {
	code, _ := ParseCode("600000")
	data, err := parseTHSLine(readTestdata(t, "ths_line_600000_2026.js"), code)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	// 年线文件（YYYY.js）只返回 data 字段，不带 name；名称断言见分钟线用例
	if len(data.Bars) != 205 {
		t.Fatalf("K 线数量 = %d，期望 205", len(data.Bars))
	}
	last := data.Bars[len(data.Bars)-1]
	// 同花顺行顺序是「日期,开,高,低,收,量(股),额」——写错顺序会把高低点算反
	if last.Date != "2026-11-10" {
		t.Fatalf("最后一根日期 = %q，期望 2026-11-10", last.Date)
	}
	if math.Abs(last.Open-10.06) > 1e-9 || math.Abs(last.High-10.24) > 1e-9 ||
		math.Abs(last.Low-10.00) > 1e-9 || math.Abs(last.Close-10.20) > 1e-9 {
		t.Fatalf("最后一根 OHLC = %v/%v/%v/%v，期望 10.06/10.24/10.00/10.20",
			last.Open, last.High, last.Low, last.Close)
	}
	if math.Abs(last.Volume-477826.52) > 1e-6 {
		t.Fatalf("成交量 = %v 手，期望 477826.52 手", last.Volume)
	}
	if math.Abs(last.Amount-485074220) > 1e-6 {
		t.Fatalf("成交额 = %v，期望 485,074,220", last.Amount)
	}
	// 每根都必须满足 high >= max(open, close) 且 low <= min(open, close)
	for i, bar := range data.Bars {
		if bar.High < bar.Open-1e-9 || bar.High < bar.Close-1e-9 {
			t.Fatalf("第 %d 根高点 %v 小于开/收 %v/%v", i, bar.High, bar.Open, bar.Close)
		}
		if bar.Low > bar.Open+1e-9 || bar.Low > bar.Close+1e-9 {
			t.Fatalf("第 %d 根低点 %v 大于开/收 %v/%v", i, bar.Low, bar.Open, bar.Close)
		}
	}
}

func TestParseTHSLineIntraday(t *testing.T) {
	code, _ := ParseCode("600000")
	data, err := parseTHSLine(readTestdata(t, "ths_line_600000_min60_last.js"), code)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	last := data.Bars[len(data.Bars)-1]
	if last.Date != "2026-11-10 15:00" {
		t.Fatalf("分钟线日期 = %q，期望 2026-11-10 15:00", last.Date)
	}
	if math.Abs(last.Close-10.20) > 1e-9 {
		t.Fatalf("收盘 = %v，期望 10.20", last.Close)
	}
}

func TestParseTHSLineSector(t *testing.T) {
	code, _ := ParseCode("bk_881101")
	data, err := parseTHSLine(readTestdata(t, "ths_line_bk_881101_2026.js"), code)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(data.Bars) == 0 {
		t.Fatal("板块 K 线为空")
	}
	last := data.Bars[len(data.Bars)-1]
	if math.Abs(last.Close-4072.614) > 1e-6 {
		t.Fatalf("板块收盘 = %v，期望 4072.614", last.Close)
	}
}

func TestParseTHSLineErrors(t *testing.T) {
	code, _ := ParseCode("600000")
	if _, err := parseTHSLine([]byte(""), code); err == nil {
		t.Fatal("空响应应报错")
	}
	if _, err := parseTHSLine([]byte(`cb({"data":""})`), code); err == nil {
		t.Fatal("空 data 应报错")
	}
}

func TestFormatTHSDate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"20261110", "2026-11-10"},
		{"202611101030", "2026-11-10 10:30"},
		{"2026-11-10", "2026-11-10"},
		{"weird", "weird"},
	}
	for _, tc := range cases {
		if got := formatTHSDate(tc.in); got != tc.want {
			t.Fatalf("formatTHSDate(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestTHSURLs(t *testing.T) {
	if got := thsRealheadURL("hs_600000"); !strings.HasSuffix(got, "/v6/realhead/hs_600000/defer/last.js") {
		t.Fatalf("realhead URL = %q", got)
	}
	if got := thsLineURL("hs_600000", "01", "2026"); !strings.HasSuffix(got, "/v6/line/hs_600000/01/2026.js") {
		t.Fatalf("line URL = %q", got)
	}
}
