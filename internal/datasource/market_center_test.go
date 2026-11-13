package datasource

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// TestParseMarketIndicesTencent 校验腾讯指数 fixture 的关键字段与固定顺序。
func TestParseMarketIndicesTencent(t *testing.T) {
	quotes := parseTxQuotes(readTestdata(t, "market_center_tx_indices.txt"))
	if len(quotes) != 5 {
		t.Fatalf("指数数量 = %d，期望 5", len(quotes))
	}
	bySymbol := make(map[string]MarketIndex, len(quotes))
	for symbol, quote := range quotes {
		bySymbol[symbol] = MarketIndex{
			Code: quote.Code, Name: quote.Name, Price: quote.Price,
			Change: quote.Change, ChangePct: quote.ChangePct, Amount: quote.Amount,
		}
	}
	items := orderMarketIndices(bySymbol)
	if len(items) != 5 {
		t.Fatalf("排序后数量 = %d，期望 5", len(items))
	}
	cases := []struct {
		index     int
		code      string
		name      string
		price     float64
		changePct float64
		amount    float64
	}{
		{0, "000001", "上证指数", 3680.64, 0.46, 736154792000},
		{1, "399001", "深证成指", 11389.74, 1.32, 940123794188},
		{2, "000688", "科创50", 1300.38, 2.80, 76439313265},
		{3, "399006", "创业板指", 2572.07, 3.11, 478416773381},
		{4, "899050", "北证50", 923.31, 2.30, 0},
	}
	for _, tc := range cases {
		got := items[tc.index]
		if got.Code != tc.code || got.Name != tc.name {
			t.Fatalf("第 %d 个指数 = %s/%s，期望 %s/%s", tc.index, got.Code, got.Name, tc.code, tc.name)
		}
		if math.Abs(got.Price-tc.price) > 1e-6 {
			t.Fatalf("%s 点位 = %v，期望 %v", got.Name, got.Price, tc.price)
		}
		if math.Abs(got.ChangePct-tc.changePct) > 1e-6 {
			t.Fatalf("%s 涨跌幅 = %v，期望 %v", got.Name, got.ChangePct, tc.changePct)
		}
		if tc.amount > 0 && got.Amount != tc.amount {
			t.Fatalf("%s 成交额 = %v，期望 %v", got.Name, got.Amount, tc.amount)
		}
	}
	if turnover := marketTurnover(items); turnover != 736154792000+940123794188 {
		t.Fatalf("两市成交额 = %v，期望上证+深证之和", turnover)
	}
}

// TestParseEMChangeDistribution 校验涨跌家数分桶口径（正=上涨、负=下跌、0=平盘）。
func TestChangeDistFromSinaSnapshot(t *testing.T) {
	transport := (&fakeTransport{}).
		add("getHQNodeData?asc=1&node=hs_a&num=100&page=1&sort=symbol", readTestdata(t, "sina_hqnode_hs_a_p1.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	dist, err := service.changeDistFromSina(context.Background())
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if dist.Source != sourceSina {
		t.Fatalf("来源 = %q，期望 %q", dist.Source, sourceSina)
	}
	if dist.Up+dist.Down+dist.Flat == 0 {
		t.Fatal("计数不应全为 0")
	}
	if dist.Amount <= 0 {
		t.Fatalf("成交额应大于 0，实际 %v", dist.Amount)
	}
	if dist.Date == "" {
		t.Fatal("日期不应为空")
	}
}

func TestParseSinaHQNodeDataRealResponse(t *testing.T) {
	rows, err := parseSinaHQNodeData(readTestdata(t, "sina_hqnode_hs_a_p1.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(rows) != 100 {
		t.Fatalf("数量 = %d，期望 100", len(rows))
	}
	first := rows[0]
	if first.Code == "" || first.Name == "" {
		t.Fatalf("首行代码/名称缺失: %+v", first)
	}
	if first.PrevClose <= 0 {
		t.Fatalf("昨收应大于 0: %+v", first)
	}
}

func TestParseSinaHQNodeDataEmptyAndBad(t *testing.T) {
	for _, body := range []string{"", "null"} {
		if _, err := parseSinaHQNodeData([]byte(body)); !errors.Is(err, ErrEmptyResponse) {
			t.Fatalf("%q 应返回 ErrEmptyResponse，实际 %v", body, err)
		}
	}
	if _, err := parseSinaHQNodeData([]byte("{not json")); err == nil {
		t.Fatal("畸形 JSON 应报错")
	}
	// 空数组是合法响应（最后一页之后），返回 0 行但不报错。
	rows, err := parseSinaHQNodeData([]byte("[]"))
	if err != nil || len(rows) != 0 {
		t.Fatalf("空数组应返回 0 行无错，实际 %d 行 / %v", len(rows), err)
	}
}

// TestChangeDistFromTHSIndexFallback 覆盖新浪整轮失败时降级到同花顺单市场家数。
func TestChangeDistFromTHSIndexFallback(t *testing.T) {
	transport := (&fakeTransport{}).
		add("/v6/realhead/hs_1A0001/", readTestdata(t, "ths_realhead_index1A0001.js"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	dist, err := service.changeDistFromTHSIndex(context.Background())
	if err != nil {
		t.Fatalf("降级统计失败: %v", err)
	}
	// fixture 实测：37 上涨 2360 / 38 下跌 1656 / 39 平盘 622（只覆盖沪市）。
	if dist.Up != 2360 || dist.Down != 1656 || dist.Flat != 622 {
		t.Fatalf("家数 = 涨%d/跌%d/平%d，期望 2360/1656/622", dist.Up, dist.Down, dist.Flat)
	}
	if !dist.Partial || dist.Source != sourceTHS+"-index" {
		t.Fatalf("降级口径标记不符: %+v", dist)
	}
	if dist.Amount != 0 {
		t.Fatalf("降级口径没有成交额，实际 %v", dist.Amount)
	}
}

// TestMarketChangeDistributionFallsBack 覆盖主链路失败后的整体降级。
func TestMarketChangeDistributionFallsBack(t *testing.T) {
	transport := (&fakeTransport{}).
		add("/v6/realhead/hs_1A0001/", readTestdata(t, "ths_realhead_index1A0001.js"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	dist, err := service.marketChangeDistribution(context.Background())
	if err != nil {
		t.Fatalf("应降级成功，实际 %v", err)
	}
	if dist.Source != sourceTHS+"-index" || !dist.Partial {
		t.Fatalf("应标记为同花顺单市场降级，实际 %+v", dist)
	}
}

func TestParseTHSLimitPool(t *testing.T) {
	pool, err := parseTHSLimitPool(readTestdata(t, "market_center_ths_limit_up.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if pool.Date != "2026-11-11" || pool.Source != sourceTHS {
		t.Fatalf("日期/来源 = %q/%q", pool.Date, pool.Source)
	}
	if pool.LimitUp != 84 || pool.LimitDown != 0 || pool.Broken != 19 {
		t.Fatalf("涨停/跌停/炸板 = %d/%d/%d，期望 84/0/19", pool.LimitUp, pool.LimitDown, pool.Broken)
	}
	if rate := brokenRate(pool.Broken, pool.LimitUp); math.Abs(rate-18.4) > 1e-9 {
		t.Fatalf("炸板率 = %v，期望 18.4", rate)
	}
	if len(pool.Stocks) != 10 {
		t.Fatalf("梯队数量 = %d，期望 10", len(pool.Stocks))
	}
	if pool.Stocks[0].Boards < pool.Stocks[len(pool.Stocks)-1].Boards {
		t.Fatalf("梯队未按连板数降序: %+v", pool.Stocks)
	}
	byCode := make(map[string]LimitUpStock, len(pool.Stocks))
	for _, row := range pool.Stocks {
		byCode[row.Code] = row
	}
	dx := byCode["600776"]
	if dx.Name != "东方通信" || dx.Boards != 2 || dx.Price != 13.65 {
		t.Fatalf("600776 = %+v，期望 东方通信/2板/13.65", dx)
	}
	if dx.FirstTime != "14:35:26" || dx.LastTime != "14:56:29" {
		t.Fatalf("600776 封板时间 = %q~%q，期望 14:35:26~14:56:29", dx.FirstTime, dx.LastTime)
	}
	if dx.Reason != "“东方”+专网通信+卫星互联网+商业航天" {
		t.Fatalf("600776 涨停原因 = %q", dx.Reason)
	}
	if dx.Amount != 33013371 || dx.AmountKind != "封单额" || dx.Turnover != 8.7506 {
		t.Fatalf("600776 封单额/口径/换手 = %v/%q/%v", dx.Amount, dx.AmountKind, dx.Turnover)
	}
	if byCode["301052"].Boards != 1 {
		t.Fatalf("301052 首板解析 = %d，期望 1", byCode["301052"].Boards)
	}
}

// TestParseBoards 覆盖 high_days_value 编码与文案兜底。
func TestParseBoards(t *testing.T) {
	cases := []struct {
		text string
		raw  int64
		want int
	}{
		{"首板", 65537, 1},
		{"3天2板", 131075, 2},
		{"4天3板", 196612, 3},
		{"7天5板", 0, 5},
		{"4天4板", 262148, 4},
		{"", 0, 0},
	}
	for _, tc := range cases {
		if got := parseBoards(tc.text, tc.raw); got != tc.want {
			t.Fatalf("parseBoards(%q,%d) = %d，期望 %d", tc.text, tc.raw, got, tc.want)
		}
	}
}

// TestAssembleMarketSnapshotWarnings 校验各块失败时的 warnings 文案与顺序
// （东财降级源已下线，涨停梯队/龙虎榜现在只有同花顺一条链路）。
func TestAssembleMarketSnapshotWarnings(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	snapshot := assembleMarketSnapshot(now, marketSnapshotParts{
		Breadth:    MarketBreadth{Date: "2026-11-11", Up: 10},
		IndicesErr: errors.New("partial"),
		BreadthErr: nil,
		LadderErr:  errors.New("ths pool down"),
		LongHuErr:  errors.New("ths longhu down"),
		Hints:      []string{"涨停梯队已降级：仅提供行业分类，无涨停原因", ""},
	})
	if snapshot.UpdatedAt != "2026-11-11T15:30:00+08:00" {
		t.Fatalf("更新时间 = %q", snapshot.UpdatedAt)
	}
	if snapshot.Indices == nil || snapshot.Ladder == nil || snapshot.LongHu == nil {
		t.Fatal("空块必须返回空数组而不是 nil（前端可直接 .length）")
	}
	want := []string{
		"指数数据暂不可用（腾讯/新浪均未返回）",
		"涨停梯队暂不可用（同花顺涨停池未返回）",
		"龙虎榜暂不可用（同花顺龙虎榜表格未返回）",
		"涨停梯队已降级：仅提供行业分类，无涨停原因",
	}
	if len(snapshot.Warnings) != len(want) {
		t.Fatalf("warnings = %v，期望 %v", snapshot.Warnings, want)
	}
	for i, text := range want {
		if snapshot.Warnings[i] != text {
			t.Fatalf("warning[%d] = %q，期望 %q", i, snapshot.Warnings[i], text)
		}
	}

	// 部分指数成功时不报「完全不可用」，模块仍有数据。
	partial := assembleMarketSnapshot(now, marketSnapshotParts{
		Indices:    []MarketIndex{{Code: "000001", Name: "上证指数"}},
		IndicesErr: ErrPartial,
	})
	if len(partial.Warnings) == 0 || partial.Warnings[0] != "部分指数数据缺失（数据源响应不完整）" {
		t.Fatalf("部分指数 warnings = %v", partial.Warnings)
	}
}

// TestBuildMarketBreadth 校验宽度合成与炸板率边界。
func TestBuildMarketBreadth(t *testing.T) {
	breadth := buildMarketBreadth(
		marketChangeDist{Date: "2026-11-11", Up: 3456, Down: 1735, Flat: 190},
		marketLimitPoolData{Date: "2026-11-11", LimitUp: 84, LimitDown: 0, Broken: 19, Source: sourceTHS},
	)
	if breadth.Up != 3456 || breadth.Down != 1735 || breadth.Flat != 190 {
		t.Fatalf("宽度计数 = %+v", breadth)
	}
	if breadth.LimitUp != 84 || breadth.Broken != 19 || breadth.BrokenRate != 18.4 {
		t.Fatalf("涨停/炸板/炸板率 = %+v", breadth)
	}
	if breadth.Source != "sina+ths" {
		t.Fatalf("来源 = %q，期望 sina+ths", breadth.Source)
	}
	if rate := brokenRate(0, 0); rate != 0 {
		t.Fatalf("空分母炸板率 = %v，期望 0", rate)
	}
	if rate := brokenRate(1, 0); rate != 100 {
		t.Fatalf("只有炸板时炸板率 = %v，期望 100", rate)
	}
}

// TestClampMarketLimit 校验插件/数据层共用的 limit 边界。
func TestClampMarketLimit(t *testing.T) {
	cases := []struct{ in, def, max, want int }{
		{0, 50, 200, 50},
		{-1, 50, 200, 50},
		{1, 50, 200, 1},
		{500, 50, 200, 200},
	}
	for _, tc := range cases {
		if got := clampMarketLimit(tc.in, tc.def, tc.max); got != tc.want {
			t.Fatalf("clampMarketLimit(%d) = %d，期望 %d", tc.in, got, tc.want)
		}
	}
}

// TestMarketSnapshotDegradesPerBlock 校验快照在单块失败时仍返回其余块（不整页失败）。
func TestMarketSnapshotDegradesPerBlock(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	snapshot := assembleMarketSnapshot(now, marketSnapshotParts{
		Indices:   []MarketIndex{{Code: "000001", Name: "上证指数", Price: 3680.64}},
		Breadth:   MarketBreadth{Date: "2026-11-11", Up: 3456, LimitUp: 84},
		Ladder:    []LimitUpStock{{Code: "600776", Name: "东方通信", Boards: 2}},
		LongHuErr: errors.New("down"),
	})
	if len(snapshot.Indices) != 1 || len(snapshot.Ladder) != 1 {
		t.Fatalf("成功块被错误清空: %+v", snapshot)
	}
	if snapshot.Breadth.Up != 3456 {
		t.Fatalf("宽度块被错误清空: %+v", snapshot.Breadth)
	}
	if len(snapshot.LongHu) != 0 || len(snapshot.Warnings) != 1 {
		t.Fatalf("龙虎榜降级结果 = %d 行, warnings=%v", len(snapshot.LongHu), snapshot.Warnings)
	}
}

// TestMarketIndicesCachedPartialStillErrors 校验缓存命中时仍如实报告部分缺失。
func TestMarketIndicesCachedPartialStillErrors(t *testing.T) {
	s := &Service{cache: newCache(func() time.Time { return time.Unix(0, 0) }), now: func() time.Time { return time.Unix(0, 0) }}
	s.cache.put(cacheKeyMarketIndices, []MarketIndex{{Code: "000001", Name: "上证指数"}}, time.Minute)
	items, err := s.MarketIndices(context.Background())
	if len(items) != 1 || !errors.Is(err, ErrPartial) {
		t.Fatalf("缓存部分数据返回 items=%d err=%v，期望 1/ErrPartial", len(items), err)
	}
}
