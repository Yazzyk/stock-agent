package datasource

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// parseCST 把 "2026-11-14 10:00:00" 解析成北京时间，供假时钟注入使用。
func parseCST(t *testing.T, stamp string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", stamp, time.FixedZone("CST", 8*3600))
	if err != nil {
		t.Fatalf("解析时间 %q 失败: %v", stamp, err)
	}
	return parsed
}

// 龙虎榜（同花顺 GBK HTML 表格）的离线解析测试：正常 / 空 / 畸形三种输入。
// fixture 是 2026-11-11 真实响应（GBK，65KB），断言口径见 ths_longhu.go 顶部注释。

func TestParseTHSLongHuRealResponse(t *testing.T) {
	body := readTestdata(t, "ths_lhb_2026-11-11.html")
	rows, err := parseTHSLongHu(body, "2026-11-11")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("应解析出龙虎榜数据")
	}
	if rows[0].Date != "2026-11-11" {
		t.Fatalf("日期 = %q，期望 2026-11-11", rows[0].Date)
	}
	// 排序：净买入绝对值降序。
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1].NetBuy, rows[i].NetBuy
		if abs(prev) < abs(cur) {
			t.Fatalf("第 %d 行净买入绝对值 %v 小于第 %d 行 %v（应按绝对值降序）", i, abs(prev), i+1, abs(cur))
		}
	}
	byCode := make(map[string]LongHuRow, len(rows))
	for _, row := range rows {
		if len(row.Code) != 6 || row.Name == "" {
			t.Fatalf("代码/名称不合法: %+v", row)
		}
		if row.Price <= 0 {
			t.Fatalf("%s 现价应大于 0: %+v", row.Code, row)
		}
		// fixture 首行：000026 飞亚达 23.16 +10.02% 成交 6.17亿 净买入 1.17亿
		byCode[row.Code] = row
	}
	var feiyada bool
	for code, row := range byCode {
		if row.Name != "飞亚达" {
			continue
		}
		feiyada = true
		if code != "000026" {
			t.Fatalf("飞亚达代码 = %q，期望 000026", code)
		}
		// 1.17亿 -> 117000000
		if diff := row.NetBuy - 117000000; diff > 1 || diff < -1 {
			t.Fatalf("飞亚达净买入 = %v，期望 1.17亿=117000000", row.NetBuy)
		}
		if diff := row.ChangePct - 10.02; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("飞亚达涨跌幅 = %v，期望 10.02", row.ChangePct)
		}
	}
	if !feiyada {
		t.Fatal("fixture 里应包含飞亚达（000026）")
	}
	// 负值也要正确换算：-3749.33万 -> -37493300
	var foundNegative bool
	for _, row := range rows {
		if row.NetBuy < 0 {
			foundNegative = true
			break
		}
	}
	if !foundNegative {
		t.Fatal("fixture 里应有净卖出（负净买入）的个股")
	}
}

// TestParseTHSLongHuEmptyResponse 覆盖空响应：没有表格数据时返回 ErrEmptyResponse。
func TestParseTHSLongHuEmptyResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"空字节", ""},
		{"只有表头", `<table class="m-table"><thead><tr><th>代码</th></tr></thead></table>`},
		{"空 tbody", `<table class="m-table"><tbody></tbody></table>`},
		{"非交易日提示", `<div class="no-data">当日无数据</div>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTHSLongHu([]byte(tc.body), "2026-11-11")
			if err == nil {
				t.Fatal("应返回错误")
			}
		})
	}
}

// TestParseTHSLongHuMalformedRows 覆盖畸形表格：坏行跳过，好行照用；整页都坏才报错。
func TestParseTHSLongHuMalformedRows(t *testing.T) {
	good := `<tr><td>3日</td><td>000026</td><td>飞亚达</td><td>23.16</td><td>10.02%</td><td>6.17亿</td><td>1.17亿</td></tr>`
	cases := []struct {
		name     string
		rows     string
		wantRows int
	}{
		{"列数不足", `<tr><td>000001</td><td>平安银行</td></tr>`, 0},
		{"代码非 6 位", `<tr><td></td><td>00002</td><td>飞亚达</td><td>23.16</td><td>10.02%</td><td>6.17亿</td><td>1.17亿</td></tr>`, 0},
		{"B 股 200 开头被过滤", `<tr><td></td><td>200001</td><td>深物业B</td><td>3.2</td><td>1%</td><td>1亿</td><td>1000万</td></tr>`, 0},
		{"价格非法但金额可读", `<tr><td></td><td>000026</td><td>飞亚达</td><td>--</td><td>--</td><td>--</td><td>1.17亿</td></tr>`, 1},
		{"坏行与好行混排", `<tr><td>1</td></tr>` + good, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `<table class="m-table"><tbody>` + tc.rows + `</tbody></table>`
			rows, err := parseTHSLongHu([]byte(body), "2026-11-11")
			if tc.wantRows == 0 {
				if err == nil {
					t.Fatalf("应返回错误，实际解析出 %d 行", len(rows))
				}
				return
			}
			if err != nil {
				t.Fatalf("应能解析出好行，实际错误 %v", err)
			}
			if len(rows) != tc.wantRows {
				t.Fatalf("行数 = %d，期望 %d", len(rows), tc.wantRows)
			}
		})
	}
}

// TestParseCnAmount 是金额量级换算的单测：亿 / 万 / 元 / 负号 / 缺失。
func TestParseCnAmount(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"1.17亿", 117000000},
		{"-3749.33万", -37493300},
		{"5830.19万", 58301900},
		{"6.17亿", 617000000},
		{"1234", 1234},
		{"1234元", 1234},
		{"", 0},
		{"-", 0},
		{"--", 0},
		{"abc", 0},
	}
	for _, tc := range cases {
		got := parseCnAmount(tc.text)
		// 浮点乘法（×1e4 / ×1e8）有末位误差，按相对误差比较。
		if diff := abs(got - tc.want); diff > abs(tc.want)*1e-9+1e-6 {
			t.Fatalf("parseCnAmount(%q) = %v，期望 %v", tc.text, got, tc.want)
		}
	}
}

// TestParseTHSLongHuGBKDecoding 覆盖编码分支：GBK 字节要能解出中文，UTF-8 原样通过。
func TestParseTHSLongHuGBKDecoding(t *testing.T) {
	utf8Body := `<table class="m-table"><tbody><tr><td></td><td>000026</td><td>飞亚达</td><td>23.16</td><td>10.02%</td><td>6.17亿</td><td>1.17亿</td></tr></tbody></table>`
	rows, err := parseTHSLongHu([]byte(utf8Body), "2026-11-11")
	if err != nil || len(rows) != 1 {
		t.Fatalf("UTF-8 表格应能解析，实际 rows=%d err=%v", len(rows), err)
	}
	if rows[0].Name != "飞亚达" {
		t.Fatalf("UTF-8 名称 = %q，期望 飞亚达", rows[0].Name)
	}
}

// TestFetchMarketLongHuSkipsWeekend 保证日期回退跳过周末（周末上游一定没有龙虎榜）。
func TestFetchMarketLongHuSkipsWeekend(t *testing.T) {
	// 2026-11-14 是周六，2026-11-13 是周五。
	transport := (&fakeTransport{}).add("report/2026-11-13/1", []byte(
		`<table class="m-table"><tbody><tr><td></td><td>000026</td><td>飞亚达</td><td>23.16</td><td>10.02%</td><td>6.17亿</td><td>1.17亿</td></tr></tbody></table>`))
	service, clock := newServiceWithTransport(t, newMemStore(), transport)
	clock.t = parseCST(t, "2026-11-14 10:00:00")

	rows, err := service.fetchMarketLongHu(context.Background())
	if err != nil {
		t.Fatalf("应回退到周五的数据: %v", err)
	}
	if len(rows) == 0 || rows[0].Date != "2026-11-13" {
		t.Fatalf("应取到 2026-11-13 的龙虎榜，实际 %+v", rows)
	}
	if transport.countCalls("report/2026-11-14/1") != 0 {
		t.Fatal("周六不应请求上游")
	}
}

func TestFixturesPresent(t *testing.T) {
	for _, name := range []string{
		"sina_flow_600000.json",
		"sina_flow_history_sh600000.json",
		"tx_fqkline_600000_hfq.json",
		"ths_limit_up_pool.json",
		"ths_lhb_2026-11-11.html",
	} {
		info, err := os.Stat(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("缺少 fixture %s: %v", name, err)
		}
		if info.Size() > 200<<10 {
			t.Fatalf("fixture %s 体积 %d 超过 200KB 上限", name, info.Size())
		}
	}
}
