package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// A 股市场中心：指数、市场宽度、涨停梯队与龙虎榜。
//
// 实测口径（2026-11-12）：
//   - 指数：腾讯 qt.gtimg.cn 批量快照（GBK，价格/涨跌幅/成交额直接可用），新浪 hq.sinajs.cn 兜底；
//   - 涨跌家数：新浪全市场快照（Market_Center.getHQNodeData?node=hs_a，56 页并发扫描）。
//     按 changepercent 正/负/零分桶统计并顺手累计成交额；
//     整轮失败时降级到同花顺 realhead 的 37/38/39 三个字段 —— 那三个字段只覆盖单一市场
//     （上证 realhead 的家数不含深市），属于降级口径，必须在 Source 与 warning 里说清。
//   - 涨停/跌停/炸板：同花顺 limit_up_pool 的 limit_up_count / limit_down_count 汇总最省请求；
//     其中 open_num = 炸板数、history_num = 曾涨停数（= 涨停 + 炸板）、rate = 封板率。
//     同花顺已是主源且实测可用，失败就按 ErrPartial 报错并写 warning，不再回退其它源。
//   - 涨停梯队：同花顺 limit_up_pool（含涨停原因 reason_type、连板数 high_days_value）。
//   - 龙虎榜：同花顺 ifmarket/lhbtable/report/<date>/1（GBK HTML 表格）。
//     表格列是 3日标记 / 代码 / 名称 / 现价 / 涨跌幅 / 成交金额 / 净买入额，金额是「1.17亿 / 5830.19万」这种中文量级。
//     这张表没有上榜原因与买入/卖出拆分，LongHuRow 的 Reason/Buy/Sell 会留空（见解析处注释）。
//
// 本文件只做只读抓取与归一化；不改 contract.DataService 接口，插件通过可选接口断言使用。

const (
	cacheKeyMarketIndices = "market:indices"
	cacheKeyMarketPool    = "market:limitpool"
	cacheKeyMarketChange  = "market:change"
	cacheKeyMarketLongHu  = "market:longhu"

	ttlMarketIndices = 15 * time.Second
	ttlMarketBlock   = 60 * time.Second

	defaultMarketLadderLimit = 50
	maxMarketLadderLimit     = 200
	defaultMarketLongHuLimit = 50
	maxMarketLongHuLimit     = 200
	// thsLimitPoolLimit 是单次拉取的上限：按 4 QPS 与 8MB 响应上限，200 足够覆盖极端行情。
	thsLimitPoolLimit = 200
	// thsLongHuMaxTradingDays 是龙虎榜当日无数据时最多回看的交易日个数
	// （只剩最近 5 个交易日：实测足够跨过周末与常见 3 天小长假，再久就没有参考意义）。
	thsLongHuMaxTradingDays = 5
	// thsLongHuMaxResponseBytes 是龙虎榜 HTML 的读取上限。
	// 实测单日表格约 65KB（GBK），留 2MB 余量防止异常页面把内存打满。
	thsLongHuMaxResponseBytes = 2 << 20
)

// marketIndexSymbols 是市场中心固定展示的指数（顺序即面板顺序）。
// 北证 50 若上游缺失会被跳过，不补假数据。
var marketIndexSymbols = []string{"sh000001", "sz399001", "sh000688", "sz399006", "bj899050"}

// marketAmountCodes 是「两市成交额」的求和口径：上证指数 + 深证成指。
var marketAmountCodes = map[string]bool{"000001": true, "399001": true}

// MarketIndex 是市场中心的一个指数快照。
type MarketIndex struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Change    float64 `json:"change"`
	ChangePct float64 `json:"changePct"`
	Amount    float64 `json:"amount,omitempty"`
}

// MarketBreadth 是市场宽度（涨跌家数、涨跌停、炸板与两市成交额）。
type MarketBreadth struct {
	Date       string  `json:"date"`
	Up         int     `json:"up"`
	Down       int     `json:"down"`
	Flat       int     `json:"flat"`
	LimitUp    int     `json:"limitUp"`
	LimitDown  int     `json:"limitDown"`
	Broken     int     `json:"broken"`
	BrokenRate float64 `json:"brokenRate"`
	Amount     float64 `json:"amount,omitempty"`
	Source     string  `json:"source,omitempty"`
}

// LimitUpStock 是涨停梯队的一只股票。
type LimitUpStock struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"changePct"`
	// Boards 是连续涨停板数（首板 = 1）。
	Boards    int    `json:"boards"`
	FirstTime string `json:"firstTime,omitempty"`
	LastTime  string `json:"lastTime,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// Amount 的语义由 AmountKind 标注：同花顺涨停池给的是封单额（order_amount）。
	Amount     float64 `json:"amount,omitempty"`
	AmountKind string  `json:"amountKind,omitempty"`
	Turnover   float64 `json:"turnover,omitempty"`
}

// LongHuRow 是龙虎榜的一只个股（同一代码的多个上榜原因已合并）。
type LongHuRow struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"changePct"`
	Reason    string  `json:"reason"`
	NetBuy    float64 `json:"netBuy"`
	Buy       float64 `json:"buy"`
	Sell      float64 `json:"sell"`
	Date      string  `json:"date"`
}

// MarketSnapshot 是四块数据的聚合体，面板一次 IPC 取齐。
type MarketSnapshot struct {
	Indices   []MarketIndex  `json:"indices"`
	Breadth   MarketBreadth  `json:"breadth"`
	Ladder    []LimitUpStock `json:"ladder"`
	LongHu    []LongHuRow    `json:"longhu"`
	Hot       []HotStock     `json:"hot"`
	UpdatedAt string         `json:"updatedAt"`
	Warnings  []string       `json:"warnings,omitempty"`
}

// marketLimitPoolData 是涨停池的内部缓存结构（涨停梯队 + 涨跌停/炸板统计共用一次请求）。
type marketLimitPoolData struct {
	Date      string
	Stocks    []LimitUpStock
	LimitUp   int
	LimitDown int
	Broken    int
	Source    string
	Warnings  []string
}

// marketChangeDist 是涨跌家数分布。
type marketChangeDist struct {
	Date string
	Up   int
	Down int
	Flat int
	// Amount 是全市场成交额（元）；只有新浪全市场扫描能给出，降级口径为 0。
	Amount float64
	// Partial 表示「家数不完整」：新浪有失败页，或降级到同花顺单市场口径。
	Partial bool
	// Source 是这份分布的实际来源（sina / ths-index），用于拼 Breadth.Source。
	Source string
}

// MarketIndices 返回固定指数列表。腾讯为主、新浪兜底；部分缺失返回 ErrPartial。
func (s *Service) MarketIndices(ctx context.Context) ([]MarketIndex, error) {
	if v, ok := s.cache.get(cacheKeyMarketIndices); ok {
		if items, ok := v.([]MarketIndex); ok {
			out := append([]MarketIndex(nil), items...)
			if len(out) > 0 && len(out) < len(marketIndexSymbols) {
				return out, fmt.Errorf("%w: 部分指数缺失（%d/%d）", ErrPartial, len(out), len(marketIndexSymbols))
			}
			return out, nil
		}
	}

	out, err := s.fetchMarketIndicesTencent(ctx)
	if err != nil {
		out, err = s.fetchMarketIndicesSina(ctx)
		if err != nil {
			return nil, fmt.Errorf("datasource: 指数取数失败（腾讯与新浪均不可用）: %w", err)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: 指数列表为空", ErrNoData)
	}

	s.cache.put(cacheKeyMarketIndices, append([]MarketIndex(nil), out...), ttlMarketIndices)
	if len(out) < len(marketIndexSymbols) {
		return out, fmt.Errorf("%w: 部分指数缺失（%d/%d）", ErrPartial, len(out), len(marketIndexSymbols))
	}
	return out, nil
}

// fetchMarketIndicesTencent 用腾讯批量快照抓指数；缺失项再用新浪补齐。
func (s *Service) fetchMarketIndicesTencent(ctx context.Context) ([]MarketIndex, error) {
	body, err := s.client.get(ctx, request{source: sourceTencent, url: txQuoteURL(marketIndexSymbols), gbk: true})
	if err != nil {
		return nil, err
	}
	quotes := parseTxQuotes(body)
	bySymbol := make(map[string]MarketIndex, len(quotes))
	for symbol, quote := range quotes {
		bySymbol[symbol] = MarketIndex{
			Code:      quote.Code,
			Name:      quote.Name,
			Price:     quote.Price,
			Change:    quote.Change,
			ChangePct: quote.ChangePct,
			Amount:    quote.Amount,
		}
	}
	out := orderMarketIndices(bySymbol)
	if len(out) == len(marketIndexSymbols) {
		return out, nil
	}

	// 缺哪只补哪只：新浪批量接口与腾讯字段同构，不整批重抓。
	missing := make([]string, 0, len(marketIndexSymbols))
	for _, symbol := range marketIndexSymbols {
		if _, ok := bySymbol[symbol]; !ok {
			missing = append(missing, symbol)
		}
	}
	if len(missing) > 0 {
		if fallback, err := s.fetchMarketIndicesSinaSymbols(ctx, missing); err == nil {
			for symbol, item := range fallback {
				if _, exists := bySymbol[symbol]; !exists {
					bySymbol[symbol] = item
				}
			}
		}
	}
	out = orderMarketIndices(bySymbol)
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: 腾讯指数响应为空", ErrEmptyResponse)
	}
	return out, nil
}

// fetchMarketIndicesSina 用新浪批量快照抓全部指数（腾讯整体失败时使用）。
func (s *Service) fetchMarketIndicesSina(ctx context.Context) ([]MarketIndex, error) {
	items, err := s.fetchMarketIndicesSinaSymbols(ctx, marketIndexSymbols)
	if err != nil {
		return nil, err
	}
	return orderMarketIndices(items), nil
}

// fetchMarketIndicesSinaSymbols 抓指定新浪指数并换算涨跌额/涨跌幅。
func (s *Service) fetchMarketIndicesSinaSymbols(ctx context.Context, symbols []string) (map[string]MarketIndex, error) {
	kinds := make(map[string]Kind, len(symbols))
	for _, symbol := range symbols {
		kinds[symbol] = KindIndex
	}
	body, err := s.client.get(ctx, request{
		source:  sourceSina,
		url:     sinaQuoteURL(symbols),
		referer: "https://finance.sina.com.cn/",
		gbk:     true,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]MarketIndex, len(symbols))
	for symbol, quote := range parseSinaQuotes(body, kinds) {
		if !quote.Valid {
			continue
		}
		item := MarketIndex{
			Code:   strings.TrimLeft(symbol, "abcdefghijklmnopqrstuvwxyz"),
			Name:   quote.Name,
			Price:  quote.Price,
			Amount: quote.Amount,
		}
		if quote.PrevClose > 0 {
			item.Change = quote.Price - quote.PrevClose
			item.ChangePct = item.Change / quote.PrevClose * 100
		}
		out[symbol] = item
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: 新浪指数响应为空", ErrEmptyResponse)
	}
	return out, nil
}

// orderMarketIndices 按固定顺序输出，并丢弃上游未返回的指数。
func orderMarketIndices(items map[string]MarketIndex) []MarketIndex {
	out := make([]MarketIndex, 0, len(items))
	for _, symbol := range marketIndexSymbols {
		if item, ok := items[symbol]; ok && item.Name != "" {
			out = append(out, item)
		}
	}
	return out
}

// MarketBreadth 返回市场宽度。涨跌家数与涨跌停统计分别降级，任一块失败返回 ErrPartial。
func (s *Service) MarketBreadth(ctx context.Context) (MarketBreadth, error) {
	pool, poolErr := s.marketLimitPool(ctx)
	dist, distErr := s.marketChangeDistribution(ctx)

	breadth := buildMarketBreadth(dist, pool)
	// 成交额优先用「两市」口径（上证指数 + 深证成指成交额），拿不到时退回全市场快照的累计额。
	// 两者口径不同：两市不含北交所，全市场快照含北交所，所以只在指数不可用时才用后者。
	if items, err := s.MarketIndices(ctx); err == nil || len(items) > 0 {
		if turnover := marketTurnover(items); turnover > 0 {
			breadth.Amount = turnover
		}
	}
	if breadth.Date == "" {
		breadth.Date = s.now().Format("2006-01-02")
	}

	switch {
	case distErr != nil && poolErr != nil:
		return breadth, fmt.Errorf("%w: 涨跌家数与涨跌停统计均不可用", ErrNoData)
	case distErr != nil:
		return breadth, fmt.Errorf("%w: 涨跌家数不可用", ErrPartial)
	case poolErr != nil:
		return breadth, fmt.Errorf("%w: 涨跌停统计不可用", ErrPartial)
	}
	return breadth, nil
}

// MarketLadder 返回按连板数降序排列的涨停梯队。
func (s *Service) MarketLadder(ctx context.Context, limit int) ([]LimitUpStock, error) {
	limit = clampMarketLimit(limit, defaultMarketLadderLimit, maxMarketLadderLimit)
	pool, err := s.marketLimitPool(ctx)
	if err != nil {
		return nil, err
	}
	if len(pool.Stocks) == 0 {
		return []LimitUpStock{}, fmt.Errorf("%w: 涨停池当日无数据", ErrNoData)
	}
	if limit > len(pool.Stocks) {
		limit = len(pool.Stocks)
	}
	return append([]LimitUpStock(nil), pool.Stocks[:limit]...), nil
}

// MarketLongHu 返回龙虎榜（同一代码合并原因，按净买入绝对值降序）。
func (s *Service) MarketLongHu(ctx context.Context, limit int) ([]LongHuRow, error) {
	limit = clampMarketLimit(limit, defaultMarketLongHuLimit, maxMarketLongHuLimit)
	if v, ok := s.cache.get(cacheKeyMarketLongHu); ok {
		if rows, ok := v.([]LongHuRow); ok {
			if limit > len(rows) {
				limit = len(rows)
			}
			return append([]LongHuRow(nil), rows[:limit]...), nil
		}
	}

	rows, err := s.fetchMarketLongHu(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []LongHuRow{}, fmt.Errorf("%w: 龙虎榜当日无数据", ErrNoData)
	}
	s.cache.put(cacheKeyMarketLongHu, append([]LongHuRow(nil), rows...), ttlMarketBlock)
	if limit > len(rows) {
		limit = len(rows)
	}
	return append([]LongHuRow(nil), rows[:limit]...), nil
}

// MarketSnapshot 一次返回四块数据；单块失败只降级该块并写入 warnings。
func (s *Service) MarketSnapshot(ctx context.Context, ladderLimit, longhuLimit int) (MarketSnapshot, error) {
	ladderLimit = clampMarketLimit(ladderLimit, defaultMarketLadderLimit, maxMarketLadderLimit)
	longhuLimit = clampMarketLimit(longhuLimit, defaultMarketLongHuLimit, maxMarketLongHuLimit)

	var (
		indices    []MarketIndex
		indicesErr error
		pool       marketLimitPoolData
		poolErr    error
		dist       marketChangeDist
		distErr    error
		longhu     []LongHuRow
		longhuErr  error
		hot        []HotStock
		hotErr     error
	)

	var wg sync.WaitGroup
	wg.Add(5)
	go func() { defer wg.Done(); indices, indicesErr = s.MarketIndices(ctx) }()
	go func() { defer wg.Done(); pool, poolErr = s.marketLimitPool(ctx) }()
	go func() { defer wg.Done(); dist, distErr = s.marketChangeDistribution(ctx) }()
	go func() { defer wg.Done(); longhu, longhuErr = s.MarketLongHu(ctx, longhuLimit) }()
	go func() { defer wg.Done(); hot, hotErr = s.MarketHot(ctx, "hour", defaultThsHot) }()
	wg.Wait()

	breadth := buildMarketBreadth(dist, pool)
	if turnover := marketTurnover(indices); turnover > 0 {
		breadth.Amount = turnover
	}
	if breadth.Date == "" {
		breadth.Date = s.now().Format("2006-01-02")
	}

	ladder := []LimitUpStock{}
	if poolErr == nil && len(pool.Stocks) > 0 {
		n := ladderLimit
		if n > len(pool.Stocks) {
			n = len(pool.Stocks)
		}
		ladder = append(ladder, pool.Stocks[:n]...)
	}
	if longhu == nil {
		longhu = []LongHuRow{}
	}
	if hot == nil {
		hot = []HotStock{}
	}
	if indices == nil {
		indices = []MarketIndex{}
	}

	parts := marketSnapshotParts{
		Indices:    indices,
		Breadth:    breadth,
		Ladder:     ladder,
		LongHu:     longhu,
		Hot:        hot,
		IndicesErr: indicesErr,
		LadderErr:  poolErr,
		LongHuErr:  longhuErr,
		HotErr:     hotErr,
		Hints:      append([]string(nil), pool.Warnings...),
	}
	switch {
	case distErr != nil && poolErr != nil:
		parts.BreadthErr = distErr
	case distErr != nil:
		parts.Hints = append(parts.Hints, "涨跌家数暂不可用：涨跌停与炸板统计仍来自同花顺")
	case poolErr != nil:
		parts.Hints = append(parts.Hints, "涨跌停与炸板统计暂不可用：涨跌家数仍来自新浪全市场快照")
	}
	if hotErr != nil {
		parts.Hints = append(parts.Hints, "同花顺热门榜暂不可用，稍后刷新重试")
	}
	if len(longhu) > 0 {
		today := s.now().Format("2006-01-02")
		if longhu[0].Date != "" && longhu[0].Date != today {
			parts.Hints = append(parts.Hints, fmt.Sprintf("龙虎榜展示最近交易日 %s（当日暂无数据）", longhu[0].Date))
		}
	}
	return assembleMarketSnapshot(s.now(), parts), nil
}

// marketSnapshotParts 是快照组装的输入，独立成结构便于纯函数测试降级文案。
type marketSnapshotParts struct {
	Indices    []MarketIndex
	Breadth    MarketBreadth
	Ladder     []LimitUpStock
	LongHu     []LongHuRow
	Hot        []HotStock
	IndicesErr error
	BreadthErr error
	LadderErr  error
	LongHuErr  error
	HotErr     error
	Hints      []string
}

// assembleMarketSnapshot 组装快照并生成确定性顺序的 warnings（同一文案只出现一次）。
func assembleMarketSnapshot(now time.Time, parts marketSnapshotParts) MarketSnapshot {
	snapshot := MarketSnapshot{
		Indices:   parts.Indices,
		Breadth:   parts.Breadth,
		Ladder:    parts.Ladder,
		LongHu:    parts.LongHu,
		Hot:       parts.Hot,
		UpdatedAt: now.Format(time.RFC3339),
	}
	if snapshot.Indices == nil {
		snapshot.Indices = []MarketIndex{}
	}
	if snapshot.Ladder == nil {
		snapshot.Ladder = []LimitUpStock{}
	}
	if snapshot.Hot == nil {
		snapshot.Hot = []HotStock{}
	}
	if snapshot.LongHu == nil {
		snapshot.LongHu = []LongHuRow{}
	}

	seen := map[string]bool{}
	add := func(text string) {
		text = strings.TrimSpace(text)
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		snapshot.Warnings = append(snapshot.Warnings, text)
	}
	if parts.IndicesErr != nil {
		if len(parts.Indices) > 0 {
			add("部分指数数据缺失（数据源响应不完整）")
		} else {
			add("指数数据暂不可用（腾讯/新浪均未返回）")
		}
	}
	if parts.BreadthErr != nil {
		add("涨跌家数或涨跌停统计暂不可用（数据源异常）")
	}
	if parts.LadderErr != nil {
		if errors.Is(parts.LadderErr, ErrNoData) {
			add("涨停池当日无数据")
		} else {
			add("涨停梯队暂不可用（同花顺涨停池未返回）")
		}
	} else if len(parts.Ladder) == 0 {
		add("涨停池当日无数据")
	}
	if parts.LongHuErr != nil {
		if errors.Is(parts.LongHuErr, ErrNoData) {
			add("龙虎榜当日无数据")
		} else {
			add("龙虎榜暂不可用（同花顺龙虎榜表格未返回）")
		}
	} else if len(parts.LongHu) == 0 {
		add("龙虎榜当日无数据")
	}
	for _, hint := range parts.Hints {
		add(hint)
	}
	return snapshot
}

// buildMarketBreadth 合并涨跌分布与涨停池统计，炸板率按「炸板 / (涨停 + 炸板)」计算。
func buildMarketBreadth(dist marketChangeDist, pool marketLimitPoolData) MarketBreadth {
	breadth := MarketBreadth{
		Date:      dist.Date,
		Up:        dist.Up,
		Down:      dist.Down,
		Flat:      dist.Flat,
		LimitUp:   pool.LimitUp,
		LimitDown: pool.LimitDown,
		Broken:    pool.Broken,
		Amount:    dist.Amount,
		Source:    joinMarketSources(dist, pool),
	}
	if breadth.Date == "" || pool.Date > breadth.Date {
		breadth.Date = pool.Date
	}
	breadth.BrokenRate = brokenRate(pool.Broken, pool.LimitUp)
	return breadth
}

// joinMarketSources 记录宽度数据的实际来源，便于 Agent 判断口径。
// 涨跌家数优先用 dist.Source（sina 全市场 / ths-index 单市场降级），
// 涨跌停用 pool.Source（ths）。
func joinMarketSources(dist marketChangeDist, pool marketLimitPoolData) string {
	parts := make([]string, 0, 2)
	if dist.Up+dist.Down+dist.Flat > 0 {
		source := dist.Source
		if source == "" {
			source = sourceSina
		}
		parts = append(parts, source)
	}
	if pool.Source != "" {
		parts = append(parts, pool.Source)
	}
	return strings.Join(parts, "+")
}

// brokenRate 计算炸板率（0~100，保留一位）。分母为「涨停 + 炸板」，与同花顺封板率互补。
func brokenRate(broken, limitUp int) float64 {
	total := broken + limitUp
	if total <= 0 {
		return 0
	}
	return math.Round(float64(broken)/float64(total)*1000) / 10
}

// marketTurnover 汇总两市成交额（只取上证指数与深证成指，北证不计入「两市」）。
func marketTurnover(indices []MarketIndex) float64 {
	var total float64
	for _, item := range indices {
		if marketAmountCodes[item.Code] {
			total += item.Amount
		}
	}
	return total
}

// clampMarketLimit 把 limit 收敛到 [1, max]，非正数用默认值。
func clampMarketLimit(limit, def, max int) int {
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	if limit < 1 {
		limit = 1
	}
	return limit
}

// ---------- 涨跌家数（新浪全市场快照） ----------

// marketChangeDistribution 抓取并缓存涨跌家数分布。
//
// 主链路：新浪全市场快照并发扫描（56 页 x 100，并发 8），统计涨/跌/平与成交额。
// 降级：整轮扫描失败（第 1 页就挂）时用同花顺 realhead 的 37/38/39 三个字段。
// 降级口径必须说清：这三个字段只覆盖**单一市场**（上证指数的 realhead 不含深市家数），
// 所以降级时上涨+下跌+平盘的家数会明显小于全市场，Source 会标 ths-index（见 fallbackSource）。
func (s *Service) marketChangeDistribution(ctx context.Context) (marketChangeDist, error) {
	if v, ok := s.cache.get(cacheKeyMarketChange); ok {
		if dist, ok := v.(marketChangeDist); ok {
			return dist, nil
		}
	}
	dist, err := s.changeDistFromSina(ctx)
	if err == nil {
		s.cache.put(cacheKeyMarketChange, dist, ttlMarketBlock)
		return dist, nil
	}
	s.log.Warn("新浪全市场快照不可用，涨跌家数降级为同花顺单市场口径", "error", err)
	dist, fallbackErr := s.changeDistFromTHSIndex(ctx)
	if fallbackErr != nil {
		return marketChangeDist{}, fmt.Errorf("涨跌家数取数失败（新浪 %v；同花顺 %v）", err, fallbackErr)
	}
	s.cache.put(cacheKeyMarketChange, dist, ttlMarketBlock)
	return dist, nil
}

// changeDistFromSina 用新浪全市场快照统计涨跌家数与成交额。
func (s *Service) changeDistFromSina(ctx context.Context) (marketChangeDist, error) {
	snapshot, err := s.fetchMarketSnapshot(ctx)
	if err != nil {
		return marketChangeDist{}, err
	}
	dist := marketChangeDist{Date: s.now().Format("2006-01-02"), Source: sourceSina}
	var amount float64
	for _, row := range snapshot.Rows {
		// 停牌股 trade=0 且 changepercent=0，不能算进「平盘」，否则平盘家数会被抬高几百只。
		if row.Price <= 0 && row.PrevClose > 0 {
			continue
		}
		amount += row.Amount
		switch {
		case row.ChangePct > 0:
			dist.Up++
		case row.ChangePct < 0:
			dist.Down++
		default:
			dist.Flat++
		}
	}
	if dist.Up+dist.Down+dist.Flat == 0 {
		return marketChangeDist{}, fmt.Errorf("%w: 新浪全市场快照统计为 0", ErrNoData)
	}
	dist.Amount = amount
	// 有失败页时记一笔降级事实（家数会偏少），调用方在 warnings 里体现。
	dist.Partial = len(snapshot.FailedPages) > 0
	return dist, nil
}

// changeDistFromTHSIndex 是同花顺 realhead 的降级口径。
//
// items 37/38/39 分别是上证指数的 上涨/平盘/下跌 家数（实测 2360/622/1656 只覆盖沪市）。
// **它只覆盖单一市场**：不含深市、北交所，所以总家数远小于全市场，
// 只能在「新浪整轮不可用」时用来给出方向性判断，不能当成完整宽度。
func (s *Service) changeDistFromTHSIndex(ctx context.Context) (marketChangeDist, error) {
	body, err := s.client.get(ctx, request{source: sourceTHS, url: thsRealheadURL("hs_1A0001")})
	if err != nil {
		return marketChangeDist{}, err
	}
	code := Code{Raw: "sh000001", Symbol: "000001", Market: contract.MarketSH, Kind: KindIndex}
	head, err := parseTHSRealhead(body, code)
	if err != nil {
		return marketChangeDist{}, err
	}
	if head.Up+head.Down+head.Flat == 0 {
		return marketChangeDist{}, fmt.Errorf("%w: 同花顺指数没有涨跌家数字段", ErrNoData)
	}
	return marketChangeDist{
		Date:    s.now().Format("2006-01-02"),
		Up:      head.Up,
		Down:    head.Down,
		Flat:    head.Flat,
		Partial: true,
		Source:  sourceTHS + "-index",
	}, nil
}

// ---------- 涨停池 / 涨停梯队 ----------

// marketLimitPool 抓取并缓存涨停池。
//
// 同花顺 limit_up_pool 是唯一来源（实测可用，含涨停原因与连板数）。
// 失败时不再回退其它源的题材池：本层按错误返回，由 MarketSnapshot 写进 warnings；
// 用「另一个源的降级数据」冒充涨停池，会让涨停数/炸板数/连板梯队三块口径互相打架。
func (s *Service) marketLimitPool(ctx context.Context) (marketLimitPoolData, error) {
	if v, ok := s.cache.get(cacheKeyMarketPool); ok {
		if data, ok := v.(marketLimitPoolData); ok {
			return cloneLimitPool(data), nil
		}
	}
	// 官方 API 优先（字段更全：连板数/封单额/涨停原因都直接给），失败再抓网页同花顺。
	if data, err := s.fetchLimitPoolFuyao(ctx); err == nil {
		s.cache.put(cacheKeyMarketPool, cloneLimitPool(data), ttlMarketBlock)
		return cloneLimitPool(data), nil
	} else {
		s.log.Debug("官方涨停池失败，降级到网页同花顺", "error", err)
	}
	data, err := s.fetchLimitPoolTHS(ctx)
	if err != nil {
		return marketLimitPoolData{}, fmt.Errorf("datasource: 涨停池取数失败（官方 API + 同花顺 limit_up_pool 均失败）: %w", err)
	}
	s.cache.put(cacheKeyMarketPool, cloneLimitPool(data), ttlMarketBlock)
	return cloneLimitPool(data), nil
}

// cloneLimitPool 深拷贝切片，避免缓存被调用方改写。
func cloneLimitPool(data marketLimitPoolData) marketLimitPoolData {
	out := data
	out.Stocks = append([]LimitUpStock(nil), data.Stocks...)
	out.Warnings = append([]string(nil), data.Warnings...)
	return out
}

// thsLimitPoolURL 拼同花顺涨停池地址。
// field 是上游字段编号（330324=连板、330329=涨停原因、133971=封单额、1968584=换手率），
// 不传 field 时只有基础字段与汇总，无法构建梯队。
func thsLimitPoolURL() string {
	query := url.Values{}
	query.Set("page", "1")
	query.Set("limit", strconv.Itoa(thsLimitPoolLimit))
	query.Set("field", "10,9001,9002,330323,330324,330325,330329,199112,133971,133970,1968584")
	query.Set("filter", "HS,GEM2STAR")
	query.Set("order_field", "330324")
	query.Set("order_type", "0")
	return "https://data.10jqka.com.cn/dataapi/limit_up/limit_up_pool?" + query.Encode()
}

// thsLimitPoolResp 是同花顺涨停池响应。
type thsLimitPoolResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
	Data       *struct {
		Page struct {
			Limit int `json:"limit"`
			Total int `json:"total"`
			Count int `json:"count"`
			Page  int `json:"page"`
		} `json:"page"`
		Date           string                  `json:"date"`
		Info           []thsLimitStock         `json:"info"`
		LimitUpCount   map[string]thsLimitStat `json:"limit_up_count"`
		LimitDownCount map[string]thsLimitStat `json:"limit_down_count"`
	} `json:"data"`
}

// thsLimitStat 是涨停/跌停汇总里 today/yesterday 的一项。
type thsLimitStat struct {
	Num        int     `json:"num"`
	HistoryNum int     `json:"history_num"`
	Rate       float64 `json:"rate"`
	OpenNum    int     `json:"open_num"`
}

// thsLimitStock 是同花顺涨停池的一行；时间字段是 Unix 秒（字符串或数字都可能）。
type thsLimitStock struct {
	Code             string        `json:"code"`
	Name             string        `json:"name"`
	Latest           float64       `json:"latest"`
	ChangeRate       float64       `json:"change_rate"`
	HighDays         string        `json:"high_days"`
	HighDaysValue    marketFlexInt `json:"high_days_value"`
	FirstLimitUpTime marketFlexInt `json:"first_limit_up_time"`
	LastLimitUpTime  marketFlexInt `json:"last_limit_up_time"`
	ReasonType       string        `json:"reason_type"`
	TurnoverRate     float64       `json:"turnover_rate"`
	OrderAmount      float64       `json:"order_amount"`
	OpenNum          int           `json:"open_num"`
}

// marketFlexInt 兼容上游把整数写成字符串或 null 的情况。
type marketFlexInt int64

// UnmarshalJSON 解析数字、数字字符串与 null。
func (m *marketFlexInt) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		*m = 0
		return nil
	}
	if v, err := strconv.ParseInt(text, 10, 64); err == nil {
		*m = marketFlexInt(v)
		return nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("datasource: 无法解析整数 %q", text)
	}
	*m = marketFlexInt(int64(v))
	return nil
}

// fetchLimitPoolTHS 抓同花顺涨停池并归一化为梯队 + 汇总统计。
func (s *Service) fetchLimitPoolTHS(ctx context.Context) (marketLimitPoolData, error) {
	body, err := s.client.get(ctx, request{
		source:  sourceTHS,
		url:     thsLimitPoolURL(),
		referer: "https://data.10jqka.com.cn/",
	})
	if err != nil {
		return marketLimitPoolData{}, err
	}
	return parseTHSLimitPool(body)
}

// parseTHSLimitPool 解析同花顺涨停池响应。
func parseTHSLimitPool(body []byte) (marketLimitPoolData, error) {
	var resp thsLimitPoolResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return marketLimitPoolData{}, fmt.Errorf("datasource: 同花顺涨停池格式异常: %w", err)
	}
	if resp.StatusCode != 0 {
		msg := strings.TrimSpace(resp.StatusMsg)
		if msg == "" {
			msg = "上游返回错误"
		}
		return marketLimitPoolData{}, fmt.Errorf("datasource: 同花顺涨停池: %s", msg)
	}
	if resp.Data == nil {
		return marketLimitPoolData{}, fmt.Errorf("%w: 同花顺涨停池 data 为空", ErrEmptyResponse)
	}

	data := marketLimitPoolData{
		Date:   compactDate(resp.Data.Date),
		Source: sourceTHS,
	}
	if len(resp.Data.Info) > 0 {
		data.Stocks = make([]LimitUpStock, 0, len(resp.Data.Info))
		for _, row := range resp.Data.Info {
			code := strings.TrimSpace(row.Code)
			name := strings.TrimSpace(row.Name)
			if code == "" || name == "" {
				continue
			}
			boards := parseBoards(row.HighDays, int64(row.HighDaysValue))
			if boards < 1 {
				boards = 1
			}
			data.Stocks = append(data.Stocks, LimitUpStock{
				Code:       code,
				Name:       name,
				Price:      row.Latest,
				ChangePct:  row.ChangeRate,
				Boards:     boards,
				FirstTime:  unixClock(int64(row.FirstLimitUpTime)),
				LastTime:   unixClock(int64(row.LastLimitUpTime)),
				Reason:     strings.TrimSpace(row.ReasonType),
				Amount:     row.OrderAmount,
				AmountKind: "封单额",
				Turnover:   row.TurnoverRate,
			})
		}
		sortLadder(data.Stocks)
	}

	today := resp.Data.LimitUpCount["today"]
	data.LimitUp = today.Num
	if data.LimitUp == 0 {
		data.LimitUp = len(data.Stocks)
	}
	data.Broken = today.OpenNum
	data.LimitDown = resp.Data.LimitDownCount["today"].Num
	return data, nil
}

// parseBoards 从 high_days_value 取连板数：高 16 位是连板、低 16 位是统计天数
// （实测 131075 = 2<<16 | 3 对应「3天2板」）。缺失时退回解析 “3天2板”/“首板” 文案。
func parseBoards(highDays string, raw int64) int {
	if raw > 0 {
		if boards := int(raw >> 16); boards > 0 {
			return boards
		}
	}
	text := strings.TrimSpace(highDays)
	if text == "" {
		return 0
	}
	if strings.Contains(text, "首板") {
		return 1
	}
	idx := strings.LastIndex(text, "板")
	if idx <= 0 {
		return 0
	}
	end := idx
	start := end
	for start > 0 && text[start-1] >= '0' && text[start-1] <= '9' {
		start--
	}
	if start == end {
		return 0
	}
	value, err := strconv.Atoi(text[start:end])
	if err != nil {
		return 0
	}
	return value
}

// sortLadder 按连板数降序，同板数按首次封板时间升序、代码升序。
func sortLadder(stocks []LimitUpStock) {
	sort.SliceStable(stocks, func(i, j int) bool {
		if stocks[i].Boards != stocks[j].Boards {
			return stocks[i].Boards > stocks[j].Boards
		}
		ti, tj := stocks[i].FirstTime, stocks[j].FirstTime
		if ti != tj {
			if ti == "" {
				return false
			}
			if tj == "" {
				return true
			}
			return ti < tj
		}
		return stocks[i].Code < stocks[j].Code
	})
}

// ---------- 时间与日期 ----------

// compactDate 把 20261111 或 2026-11-11 统一成 2026-11-11；无法识别返回空串。
func compactDate(raw string) string {
	text := strings.TrimSpace(raw)
	if len(text) == 10 && text[4] == '-' && text[7] == '-' {
		return text
	}
	if len(text) != 8 {
		return ""
	}
	if _, err := time.ParseInLocation("20060102", text, time.FixedZone("CST", 8*3600)); err != nil {
		return ""
	}
	return text[0:4] + "-" + text[4:6] + "-" + text[6:8]
}

// unixClock 把同花顺的 Unix 秒转成 HH:MM:SS（北京时间）。
func unixClock(sec int64) string {
	if sec <= 0 {
		return ""
	}
	if sec > 1_000_000_000_000 {
		sec /= 1000
	}
	return time.Unix(sec, 0).In(time.FixedZone("CST", 8*3600)).Format("15:04:05")
}
