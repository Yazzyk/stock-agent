package datasource

import (
	"math"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// txBody/sinaBody 模拟 client 的 GBK 解码步骤：腾讯与新浪响应都是 GBK，
// 生产路径里由 client.get(gbk:true) 解码后再交给解析函数，测试走同一条路径。
func txBody(t *testing.T, name string) []byte {
	t.Helper()
	text, err := decodeGBK(readTestdata(t, name))
	if err != nil {
		t.Fatalf("GBK 解码 %s 失败: %v", name, err)
	}
	return []byte(text)
}

func TestParseTxQuotesStock(t *testing.T) {
	quotes := parseTxQuotes(txBody(t, "tx_quote_600000.txt"))
	quote, ok := quotes["sh600000"]
	if !ok {
		t.Fatalf("未解析出 sh600000，实际: %v", keysOf(quotes))
	}
	if quote.Name != "浦发银行" || quote.Code != "600000" {
		t.Fatalf("名称/代码 = %q/%q", quote.Name, quote.Code)
	}
	cases := []struct {
		label     string
		got, want float64
	}{
		{"现价", quote.Price, 10.20},
		{"昨收", quote.PrevClose, 10.10},
		{"今开", quote.Open, 10.06},
		{"最高", quote.High, 10.24},
		{"最低", quote.Low, 10.00},
		{"涨跌额", quote.Change, 0.10},
		{"涨跌幅", quote.ChangePct, 0.99},
		{"成交量(手)", quote.VolumeHand, 477827},
		{"成交额(元)", quote.Amount, 485074221},
	}
	for _, tc := range cases {
		if math.Abs(tc.got-tc.want) > 1e-6 {
			t.Fatalf("%s = %v，期望 %v", tc.label, tc.got, tc.want)
		}
	}
	if quote.Time != "2026-11-10T16:14:59+08:00" {
		t.Fatalf("时间 = %q", quote.Time)
	}
}

func TestParseTxQuotesIndex(t *testing.T) {
	quote := parseTxQuotes(txBody(t, "tx_quote_sh000001.txt"))["sh000001"]
	if quote.Name != "上证指数" {
		t.Fatalf("名称 = %q，期望 上证指数", quote.Name)
	}
	// 腾讯指数成交量已经是「手」，不能再除 100
	if math.Abs(quote.VolumeHand-404865177) > 1 {
		t.Fatalf("指数成交量 = %v 手，期望 404,865,177 手", quote.VolumeHand)
	}
	if math.Abs(quote.Amount-673951893240) > 1 {
		t.Fatalf("指数成交额 = %v，期望 673,951,893,240", quote.Amount)
	}
}

func TestParseTxQuotesSuspended(t *testing.T) {
	// 430047（诺思兰德）实测处于停牌：现价=昨收=8.17，今开 0、成交量 0
	quote, ok := parseTxQuotes(txBody(t, "tx_quote_bj430047.txt"))["bj430047"]
	if !ok {
		t.Fatal("未解析出 bj430047")
	}
	if quote.VolumeHand != 0 || quote.Amount != 0 {
		t.Fatalf("停牌股成交量/额应为 0，实际 %v/%v", quote.VolumeHand, quote.Amount)
	}
	normalized := normalizeQuoteState(txQuoteToContract(quote, contract.MarketBJ))
	if !normalized.Stale {
		t.Fatal("零成交快照必须标记 stale")
	}
	if normalized.Price != 8.17 {
		t.Fatalf("停牌时价格应保留停牌前收盘 8.17，实际 %v", normalized.Price)
	}
}

func TestParseTxQuotesEmpty(t *testing.T) {
	if got := parseTxQuotes([]byte("")); len(got) != 0 {
		t.Fatalf("空响应应返回空 map，实际 %v", got)
	}
	// 停牌占位串（v_pv_none_match="1" 之类）不能当成有效行情
	if got := parseTxQuotes([]byte(`v_sh999999="1";`)); len(got) != 0 {
		t.Fatalf("占位响应不应解析出行情，实际 %v", got)
	}
}

func TestParseSinaQuotesStockAndIndex(t *testing.T) {
	kinds := map[string]Kind{"sh600000": KindStock, "sh000001": KindIndex}

	stockBody := append(txBody(t, "sina_quote_600000.txt"), txBody(t, "sina_quote_sh000001.txt")...)
	quotes := parseSinaQuotes(stockBody, kinds)

	stock, ok := quotes["sh600000"]
	if !ok {
		t.Fatalf("未解析出 sh600000，实际 %d 条", len(quotes))
	}
	if stock.Name != "浦发银行" {
		t.Fatalf("名称 = %q", stock.Name)
	}
	// 新浪个股成交量是「股」
	if math.Abs(stock.VolumeHand-477826.52) > 1e-6 {
		t.Fatalf("个股成交量 = %v 手，期望 477826.52 手", stock.VolumeHand)
	}
	if math.Abs(stock.Amount-485074221) > 1e-6 {
		t.Fatalf("个股成交额 = %v，期望 485,074,221", stock.Amount)
	}

	index, ok := quotes["sh000001"]
	if !ok {
		t.Fatal("未解析出 sh000001")
	}
	// 新浪指数成交量是「手」，与腾讯一致
	if math.Abs(index.VolumeHand-404865177) > 1 {
		t.Fatalf("指数成交量 = %v 手，期望 404,865,177 手", index.VolumeHand)
	}
	if index.Time != "2026-11-10T15:35:57+08:00" {
		t.Fatalf("指数时间 = %q", index.Time)
	}
}

func TestParseSinaQuotesSuspended(t *testing.T) {
	// 新浪对北交所个股返回全 0 快照，必须识别为无效而不是当成 0 元行情
	quotes := parseSinaQuotes(txBody(t, "sina_quote_bj430047.txt"), map[string]Kind{"bj430047": KindStock})
	if _, ok := quotes["bj430047"]; ok {
		t.Fatalf("全 0 快照不应解析为有效行情: %+v", quotes["bj430047"])
	}
}

func TestParseSinaMoneyFlow(t *testing.T) {
	flow, err := parseSinaMoneyFlow(readTestdata(t, "sina_moneyflow_600000.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if flow.Name != "浦发银行" {
		t.Fatalf("名称 = %q", flow.Name)
	}
	// r0_in - r0_out = 119,605,272.06 - 70,166,915.01
	if math.Abs(flow.SuperNet-49438357.05) > 1e-6 {
		t.Fatalf("超大单净额 = %v，期望 49438357.05", flow.SuperNet)
	}
	if math.Abs(flow.LargeNet-10166673.40) > 1e-6 {
		t.Fatalf("大单净额 = %v，期望 10166673.40", flow.LargeNet)
	}
	if math.Abs(flow.MediumNet-11253444.54) > 1e-6 {
		t.Fatalf("中单净额 = %v，期望 11253444.54", flow.MediumNet)
	}
	if math.Abs(flow.SmallNet-(-4295087.49)) > 1e-6 {
		t.Fatalf("小单净额 = %v，期望 -4295087.49", flow.SmallNet)
	}
	// 主力 = 超大 + 大；且四级净额之和必须等于接口给的 netamount
	mainNet := flow.SuperNet + flow.LargeNet
	if math.Abs(flow.MainNet-mainNet) > 1e-6 {
		t.Fatalf("主力净额 = %v，期望 %v", flow.MainNet, mainNet)
	}
	sum := flow.SuperNet + flow.LargeNet + flow.MediumNet + flow.SmallNet
	if math.Abs(sum-flow.AmountYuan) > 1e-6 {
		t.Fatalf("四级净额之和 %v 应等于 netamount %v", sum, flow.AmountYuan)
	}
	// 涨跌幅原始值是小数比例 0.00990099，对外要换算成百分数
	if math.Abs(flow.ChangePct-0.99) > 1e-3 {
		t.Fatalf("涨跌幅 = %v，期望 0.99", flow.ChangePct)
	}
}

func TestParseSinaMoneyFlowErrors(t *testing.T) {
	if _, err := parseSinaMoneyFlow([]byte("")); err == nil {
		t.Fatal("空响应应报错")
	}
	if _, err := parseSinaMoneyFlow([]byte("not json")); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

func TestParseSinaKline(t *testing.T) {
	bars, err := parseSinaKline(readTestdata(t, "sina_kline_600000.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(bars) != 60 {
		t.Fatalf("K 线数量 = %d，期望 60", len(bars))
	}
	last := bars[len(bars)-1]
	if last.Date != "2026-11-10" {
		t.Fatalf("最后一根日期 = %q，期望 2026-11-10", last.Date)
	}
	// 新浪 K 线成交量是「股」→ /100
	if math.Abs(last.Volume-477826.52) > 1e-6 {
		t.Fatalf("成交量 = %v 手，期望 477826.52 手", last.Volume)
	}
	for _, bar := range bars {
		if bar.High < bar.Low {
			t.Fatalf("高 %v 小于低 %v（%s）", bar.High, bar.Low, bar.Date)
		}
	}
}

func TestParseTxSuggest(t *testing.T) {
	items := parseTxSuggest(readTestdata(t, "tx_suggest_pfyh.txt"))
	if len(items) != 1 {
		t.Fatalf("建议数量 = %d，期望 1: %+v", len(items), items)
	}
	if items[0].Code != "600000" || items[0].Name != "浦发银行" || items[0].Market != contract.MarketSH {
		t.Fatalf("解析结果 = %+v", items[0])
	}
}

func keysOf(m map[string]txQuote) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
