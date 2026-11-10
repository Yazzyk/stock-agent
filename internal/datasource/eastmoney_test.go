package datasource

import (
	"math"
	"testing"
)

func TestParseEMFFlow(t *testing.T) {
	flow, err := parseEMFFlow(readTestdata(t, "em_fflow_600000.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if flow.Name != "浦发银行" || flow.Code != "600000" {
		t.Fatalf("名称/代码 = %q/%q", flow.Name, flow.Code)
	}
	if flow.Date != "2026-11-10" {
		t.Fatalf("日期 = %q，期望 2026-11-10", flow.Date)
	}
	cases := []struct {
		label string
		got   float64
		want  float64
	}{
		// 行顺序：日期,主力(f52),小单(f53),中单(f54),大单(f55),超大单(f56)
		{"主力净额", flow.MainNet, 26922753.0},
		{"小单净额", flow.SmallNet, -13585594.0},
		{"中单净额", flow.MediumNet, -13337160.0},
		{"大单净额", flow.LargeNet, 17282621.0},
		{"超大单净额", flow.SuperNet, 9640132.0},
	}
	for _, tc := range cases {
		if math.Abs(tc.got-tc.want) > 1e-6 {
			t.Fatalf("%s = %v，期望 %v", tc.label, tc.got, tc.want)
		}
	}
	// 口径校验：主力 = 大单 + 超大单（这是判断字段映射是否写反的关键断言）
	if math.Abs(flow.MainNet-(flow.LargeNet+flow.SuperNet)) > 1e-6 {
		t.Fatalf("主力净额 %v != 大单 %v + 超大单 %v", flow.MainNet, flow.LargeNet, flow.SuperNet)
	}
}

func TestParseEMFFlowErrors(t *testing.T) {
	if _, err := parseEMFFlow([]byte("")); err == nil {
		t.Fatal("空响应应报错")
	}
	if _, err := parseEMFFlow([]byte(`{"rc":0,"data":null}`)); err == nil {
		t.Fatal("data 为空应报错")
	}
	if _, err := parseEMFFlow([]byte(`{"rc":0,"data":{"klines":["2026-11-10,1,2"]}}`)); err == nil {
		t.Fatal("字段不足应报错")
	}
}

func TestParseEMBkzj(t *testing.T) {
	cases := []struct {
		name string
		file string
		want string
	}{
		{"行业板块", "em_bkzj_sector.json", "电子"},
		{"概念板块", "em_bkzj_concept.json", "融资融券"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := parseEMBkzj(readTestdata(t, tc.file))
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if len(rows) != 20 {
				t.Fatalf("行数 = %d，期望 20", len(rows))
			}
			if rows[0].Name != tc.want {
				t.Fatalf("第一名 = %q，期望 %q", rows[0].Name, tc.want)
			}
			if rows[0].Code == "" || !math.IsInf(0, 0) == false {
				// 代码必须非空，且主力净额必须是有限数
			}
			if rows[0].Code == "" {
				t.Fatal("代码不能为空")
			}
			if math.IsNaN(rows[0].MainNet) || math.IsInf(rows[0].MainNet, 0) {
				t.Fatalf("主力净额非法: %v", rows[0].MainNet)
			}
			// 排名按主力净额降序，榜单才有意义
			for i := 1; i < len(rows); i++ {
				if rows[i].MainNet > rows[i-1].MainNet {
					t.Fatalf("第 %d 名主力净额 %v 大于第 %d 名 %v", i+1, rows[i].MainNet, i, rows[i-1].MainNet)
				}
			}
		})
	}
}

func TestParseEMClist(t *testing.T) {
	rows, err := parseEMClist(readTestdata(t, "em_clist_sector.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("板块列表为空")
	}
	if rows[0].Code != "BK1201" || rows[0].Name != "电子" {
		t.Fatalf("第一名 = %+v，期望 BK1201 电子", rows[0])
	}
	if math.Abs(rows[0].ChangePct-3.29) > 1e-6 {
		t.Fatalf("涨跌幅 = %v，期望 3.29", rows[0].ChangePct)
	}
	if math.Abs(rows[0].MainNet-9201537792) > 1 {
		t.Fatalf("主力净额 = %v，期望 9,201,537,792", rows[0].MainNet)
	}
}

func TestParseEMSuggest(t *testing.T) {
	items, err := parseEMSuggest(readTestdata(t, "em_suggest_pfyh.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("搜索结果为空")
	}
	first := items[0]
	if first.Code != "600000" || first.Name != "浦发银行" {
		t.Fatalf("第一条 = %+v，期望 600000 浦发银行", first)
	}
	if first.Market != "sh" {
		t.Fatalf("市场 = %q，期望 sh", first.Market)
	}
	// 债券等非 A 股/指数标的必须被过滤掉
	for _, item := range items {
		if item.Code == "360003" {
			t.Fatalf("债券代码不应出现在 A 股搜索结果里: %+v", item)
		}
	}
}

func TestParseEMSuggestErrors(t *testing.T) {
	if _, err := parseEMSuggest([]byte("")); err == nil {
		t.Fatal("空响应应报错")
	}
	if _, err := parseEMSuggest([]byte(`{"QuotationCodeTable":{"Data":[]}}`)); err == nil {
		t.Fatal("空列表应报错")
	}
}

func TestEMURLBuilders(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"K线含 secid 与复权", emKlineURL("1.600000", 101, 1, 30), "secid=1.600000"},
		{"资金流含 secid", emFFlowURL("0.000001"), "secid=0.000001"},
		{"板块资金流默认行业", emSectorFlowURL(KindSector, 10), "m%3A90%2Bt%3A2"},
		{"概念资金流", emSectorFlowURL(KindConcept, 10), "m%3A90%2Bt%3A3"},
		{"板块列表", emSectorListURL(KindSector, 10), "fid=f62"},
		{"搜索", emSuggestURL("浦发", 10), "input="},
	}
	for _, tc := range cases {
		if !contains(tc.got, tc.want) {
			t.Fatalf("%s: URL %q 应包含 %q", tc.name, tc.got, tc.want)
		}
	}
	if !contains(emKlineURL("1.600000", 102, 2, 50), "fqt=2") {
		t.Fatal("后复权应传 fqt=2")
	}
	if !contains(emKlineURL("1.600000", 101, 0, 30), "klt=101") {
		t.Fatal("日线应传 klt=101")
	}
}
