package datasource

import (
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

func TestParseCode(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		symbol string
		market contract.Market
		kind   Kind
		ths    string
		secid  string
		msym   string
	}{
		{name: "纯数字默认深市", raw: "000001", symbol: "000001", market: contract.MarketSZ, kind: KindStock, ths: "hs_000001", secid: "0.000001", msym: "sz000001"},
		{name: "六位沪市", raw: "600000", symbol: "600000", market: contract.MarketSH, kind: KindStock, ths: "hs_600000", secid: "1.600000", msym: "sh600000"},
		{name: "前缀沪市", raw: "sh600000", symbol: "600000", market: contract.MarketSH, kind: KindStock, ths: "hs_600000", secid: "1.600000", msym: "sh600000"},
		{name: "大写前缀", raw: "SH600000", symbol: "600000", market: contract.MarketSH, kind: KindStock, ths: "hs_600000", secid: "1.600000", msym: "sh600000"},
		{name: "带空白", raw: " sz300750 ", symbol: "300750", market: contract.MarketSZ, kind: KindStock, ths: "hs_300750", secid: "0.300750", msym: "sz300750"},
		{name: "东财 secid 沪", raw: "1.600000", symbol: "600000", market: contract.MarketSH, kind: KindStock, ths: "hs_600000", secid: "1.600000", msym: "sh600000"},
		{name: "东财 secid 深", raw: "0.000001", symbol: "000001", market: contract.MarketSZ, kind: KindStock, ths: "hs_000001", secid: "0.000001", msym: "sz000001"},
		{name: "同花顺前缀个股", raw: "hs_600000", symbol: "600000", market: contract.MarketSH, kind: KindStock, ths: "hs_600000", secid: "1.600000", msym: "sh600000"},
		{name: "指数必须带 sh 前缀", raw: "sh000001", symbol: "000001", market: contract.MarketSH, kind: KindIndex, ths: "hs_1A0001", secid: "1.000001", msym: "sh000001"},
		{name: "同花顺指数 1A0001", raw: "hs_1A0001", symbol: "000001", market: contract.MarketSH, kind: KindIndex, ths: "hs_1A0001", secid: "1.000001", msym: "sh000001"},
		{name: "沪深300", raw: "sh000300", symbol: "000300", market: contract.MarketSH, kind: KindIndex, ths: "hs_1B0300", secid: "1.000300", msym: "sh000300"},
		{name: "深证成指", raw: "399001", symbol: "399001", market: contract.MarketSZ, kind: KindIndex, ths: "hs_399001", secid: "0.399001", msym: "sz399001"},
		{name: "创业板指", raw: "sz399006", symbol: "399006", market: contract.MarketSZ, kind: KindIndex, ths: "hs_399006", secid: "0.399006", msym: "sz399006"},
		{name: "未收录映射的沪指", raw: "sh000016", symbol: "000016", market: contract.MarketSH, kind: KindIndex, ths: "", secid: "1.000016", msym: "sh000016"},
		{name: "北交所个股", raw: "430047", symbol: "430047", market: contract.MarketBJ, kind: KindStock, ths: "hs_430047", secid: "0.430047", msym: "bj430047"},
		{name: "北交所显式前缀", raw: "bj430047", symbol: "430047", market: contract.MarketBJ, kind: KindStock, ths: "hs_430047", secid: "0.430047", msym: "bj430047"},
		{name: "北证50", raw: "bj899050", symbol: "899050", market: contract.MarketBJ, kind: KindIndex, ths: "", secid: "0.899050", msym: "bj899050"},
		{name: "东财板块", raw: "BK1036", symbol: "BK1036", kind: KindSector, ths: "", secid: "90.BK1036", msym: ""},
		{name: "东财板块 secid", raw: "90.BK1036", symbol: "BK1036", kind: KindSector, ths: "", secid: "90.BK1036", msym: ""},
		{name: "东财板块小写", raw: "bk0486", symbol: "BK0486", kind: KindSector, ths: "", secid: "90.BK0486", msym: ""},
		{name: "同花顺板块", raw: "bk_881101", symbol: "881101", kind: KindSector, ths: "bk_881101", secid: "", msym: ""},
		{name: "同花顺板块裸数字", raw: "881101", symbol: "881101", kind: KindSector, ths: "bk_881101", secid: "", msym: ""},
		{name: "概念指数后缀写法", raw: "884201", symbol: "884201", kind: KindSector, ths: "bk_884201", secid: "", msym: ""},
		{name: "s_ 前缀", raw: "s_sh600000", symbol: "600000", market: contract.MarketSH, kind: KindStock, ths: "hs_600000", secid: "1.600000", msym: "sh600000"},
		{name: "沪市 ETF", raw: "510300", symbol: "510300", market: contract.MarketSH, kind: KindStock, ths: "hs_510300", secid: "1.510300", msym: "sh510300"},
		{name: "沪市 B 股 9 开头", raw: "900901", symbol: "900901", market: contract.MarketSH, kind: KindStock, ths: "hs_900901", secid: "1.900901", msym: "sh900901"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := ParseCode(tc.raw)
			if err != nil {
				t.Fatalf("ParseCode(%q) 失败: %v", tc.raw, err)
			}
			if code.Symbol != tc.symbol || code.Market != tc.market || code.Kind != tc.kind {
				t.Fatalf("ParseCode(%q) = {%s %s %s}，期望 {%s %s %s}",
					tc.raw, code.Symbol, code.Market, code.Kind, tc.symbol, tc.market, tc.kind)
			}
			if got := code.THSSymbol(); got != tc.ths {
				t.Fatalf("THSSymbol(%q) = %q，期望 %q", tc.raw, got, tc.ths)
			}
			if got := code.SecID(); got != tc.secid {
				t.Fatalf("SecID(%q) = %q，期望 %q", tc.raw, got, tc.secid)
			}
			if got := code.MarketSymbol(); got != tc.msym {
				t.Fatalf("MarketSymbol(%q) = %q，期望 %q", tc.raw, got, tc.msym)
			}
		})
	}
}

func TestParseCodeErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"空串", ""},
		{"空白", "   "},
		{"长度不对", "60000"},
		{"字母代码", "abc123"},
		{"非法板块", "bk_12345"},
		{"非法东财板块", "90.abc"},
		{"无法推断市场", "777777"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCode(tc.raw); err == nil {
				t.Fatalf("ParseCode(%q) 应报错", tc.raw)
			}
		})
	}
}

func TestCodeIDAndQuoteCode(t *testing.T) {
	stock, _ := ParseCode("sh600000")
	if stock.ID() != "sh600000" {
		t.Fatalf("个股 ID = %q，期望 sh600000", stock.ID())
	}
	if stock.QuoteCode() != "600000" {
		t.Fatalf("QuoteCode = %q，期望 600000", stock.QuoteCode())
	}
	sector, _ := ParseCode("BK1036")
	if sector.ID() != "sector:BK1036" {
		t.Fatalf("板块 ID = %q，期望 sector:BK1036", sector.ID())
	}
	thsSector, _ := ParseCode("bk_881101")
	if thsSector.ID() != "sector:881101" {
		t.Fatalf("同花顺板块 ID = %q，期望 sector:881101", thsSector.ID())
	}
}
