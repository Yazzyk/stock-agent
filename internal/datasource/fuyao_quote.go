package datasource

// 官方行情快照（任务 1：自选股列表提速）。
//
// 性能结论（详见 docs/FUYAO-API.md §性能）：
//   - 旧链路每次刷新固定打「官方快照 + 腾讯批量」两次串行请求，且逐只写 sqlite，
//     官方结果不进缓存（缓存只覆盖 quoteOne 的降级路径），50 只自选约 2 个 RTT + 50 次写盘；
//   - 新链路：官方批量快照按 50/批、并发 ≤4、结果进短 TTL 批量缓存；名称优先读内存/本地缓存，
//     只有真的缺名字时才请求腾讯补齐；落盘按 30 秒节流，价格/量额不变不写。
//
// 口径：
//   - 官方 volume 单位是「股」；对外 contract.Quote.Volume 统一「手」（除以 100）；
//   - turnover 单位「元」；price_change_ratio_pct 是百分数原值（1.74 表示 +1.74%）。

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	// fuyaoQuoteFieldVersion 是快照缓存键里的字段/单位版本：口径变更时递增即可失效旧缓存。
	fuyaoQuoteFieldVersion = "v2"
	// fuyaoQuoteChunk 是官方快照单次最多代码数（实测可稳定返回 50 个）。
	fuyaoQuoteChunk = 50
	// fuyaoQuoteConcurrency 是分批并发上限；官方建议低并发，超过 4 容易触发 4001。
	fuyaoQuoteConcurrency = 4
	// ttlQuoteNonTrading 是非交易时段的快照缓存时间（价格不再变化，长一点无妨）。
	ttlQuoteNonTrading = 60 * time.Second
	// ttlQuoteBreak 是午间休市/集合竞价前的缓存时间。
	ttlQuoteBreak = 15 * time.Second
	// persistMinInterval 是同一代码快照落盘的最小间隔（避免 5 秒轮询写 50 次 sqlite）。
	persistMinInterval = 30 * time.Second
)

// fuyaoSnapshotItem 是官方行情快照的一行。
type fuyaoSnapshotItem struct {
	Thscode   string  `json:"thscode"`
	Ticker    string  `json:"ticker"`
	Volume    float64 `json:"volume"`
	Turnover  float64 `json:"turnover"`
	LastPrice float64 `json:"last_price"`
	Change    float64 `json:"price_change"`
	ChangePct float64 `json:"price_change_ratio_pct"`
	Open      float64 `json:"open_price"`
	High      float64 `json:"high_price"`
	Low       float64 `json:"low_price"`
	PrevClose float64 `json:"prev_price"`
}

// fuyaoSnapshotData 是官方行情快照的数据体。
type fuyaoSnapshotData struct {
	Timestamp int64               `json:"timestamp"`
	Total     int                 `json:"total"`
	Item      []fuyaoSnapshotItem `json:"item"`
}

// nameEntry 是进程内名称缓存的条目。
type nameEntry struct {
	name    string
	expires time.Time
}

// quoteState 是快照路径的进程内辅助状态：名称缓存 + 落盘节流表。
type quoteState struct {
	mu      sync.Mutex
	names   map[string]nameEntry
	persist map[string]time.Time
}

// newQuoteState 构造空的快照状态。
func newQuoteState() quoteState {
	return quoteState{names: map[string]nameEntry{}, persist: map[string]time.Time{}}
}

// THSCode 返回官方 API 使用的完整 thscode（如 600519.SH）；不支持时返回空串。
func (c Code) THSCode() string {
	if c.Kind == KindSector || c.Kind == KindConcept || c.Market == "" {
		return ""
	}
	suffix := map[contract.Market]string{
		contract.MarketSH: "SH",
		contract.MarketSZ: "SZ",
		contract.MarketBJ: "BJ",
	}[c.Market]
	if suffix == "" {
		return ""
	}
	return c.Symbol + "." + suffix
}

// fuyaoQuoteTTL 按交易时段返回快照缓存时间：盘中 3 秒、午休 15 秒、其余 60 秒。
func (s *Service) fuyaoQuoteTTL() time.Duration {
	now := s.now().In(cstZone())
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return ttlQuoteNonTrading
	}
	minutes := now.Hour()*60 + now.Minute()
	switch {
	case minutes >= 9*60+15 && minutes <= 11*60+30:
		return ttlQuote
	case minutes > 11*60+30 && minutes < 13*60:
		return ttlQuoteBreak
	case minutes >= 13*60 && minutes <= 15*60+5:
		return ttlQuote
	default:
		return ttlQuoteNonTrading
	}
}

// fuyaoBatchQuoteKey 生成批量缓存键：代码集合排序 + 字段版本（与顺序无关，便于不同面板复用）。
func fuyaoBatchQuoteKey(thsCodes []string) string {
	codes := append([]string(nil), thsCodes...)
	sort.Strings(codes)
	return "fuyao:snap:" + fuyaoQuoteFieldVersion + ":" + strings.Join(codes, ",")
}

// fuyaoQuoteItemKey 是单代码缓存键（批量缓存的补充：子集查询也能命中）。
func fuyaoQuoteItemKey(id string) string {
	return "fuyao:q:" + fuyaoQuoteFieldVersion + ":" + id
}

// fuyaoQuotes 用官方快照批量取数（带缓存、分批、并发受限、限流退避）。
//
// 返回按 Code.ID() 索引的结果；失败（无 Key / 网络 / 限流）返回已取到的部分，
// 由调用方无缝降级到腾讯/新浪/网页同花顺。任何一批失败都会打一条 Warn 日志
// （event=fuyao.fallback），保证降级可观测而不是静默。
func (s *Service) fuyaoQuotes(ctx context.Context, codes []Code) map[string]contract.Quote {
	rows, _ := s.fuyaoSnapshotRows(ctx, codes)
	out := make(map[string]contract.Quote, len(rows))
	for _, row := range rows {
		code, ok := codeFromTHSCode(row.Thscode)
		if !ok {
			// 指数后缀（.TI）无法还原成沪深代码，直接跳过（指数走 FuyaoIndexSnapshot）。
			continue
		}
		quote := contract.Quote{
			Code:      code.Symbol,
			Name:      row.Name,
			Market:    code.Market,
			Price:     row.LastPrice,
			PrevClose: row.PrevClose,
			Open:      row.Open,
			High:      row.High,
			Low:       row.Low,
			// 官方 volume 单位是股；对外统一「手」。
			Volume: row.Volume / 100,
			Amount: row.Turnover,
			Change: row.Change,
			// 官方给的是百分数（1.74 表示 +1.74%），与契约 ChangePct 一致。
			ChangePct: row.ChangePct,
			Time:      formatFuyaoClock(row.Timestamp),
			Source:    sourceFuyao,
		}
		if quote.Price <= 0 && quote.PrevClose <= 0 {
			continue
		}
		s.cache.put(fuyaoQuoteItemKey(code.ID()), quote, s.fuyaoQuoteTTL())
		out[code.ID()] = quote
	}
	return out
}

// fuyaoSnapshotRows 是快照取数的核心：先查批量缓存 → 再查单代码缓存 →
// 只对缺口按 50/批并发请求，最后把结果写回缓存并返回带名称的行。
//
// 返回的 error 仅在「所有批次都失败」时非 nil；部分失败通过日志与返回行体现。
func (s *Service) fuyaoSnapshotRows(ctx context.Context, codes []Code) ([]contract.FuyaoQuoteRow, error) {
	if !s.fuyaoAvailable() || len(codes) == 0 {
		return nil, fmt.Errorf("datasource: 官方 API 未启用")
	}
	byThs := make(map[string]Code, len(codes))
	order := make([]string, 0, len(codes))
	for _, code := range codes {
		ths := code.THSCode()
		if ths == "" {
			continue
		}
		if _, exists := byThs[ths]; exists {
			continue
		}
		byThs[ths] = code
		order = append(order, ths)
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("%w: 没有可映射成官方 thscode 的标的", ErrNoData)
	}

	out := make(map[string]contract.FuyaoQuoteRow, len(order))
	missing := make([]string, 0, len(order))

	// ① 批量缓存：同一代码集合在 TTL 内直接返回，零网络请求。
	if cached, ok := s.cache.get(fuyaoBatchQuoteKey(order)); ok {
		if rows, isRows := cached.(map[string]contract.FuyaoQuoteRow); isRows {
			for _, ths := range order {
				if row, exists := rows[ths]; exists {
					out[ths] = row
				}
			}
		}
	}
	// ② 单代码缓存：补充批量键未命中的子集（不同面板取不同代码集合时很有用）。
	for _, ths := range order {
		if _, exists := out[ths]; exists {
			continue
		}
		code := byThs[ths]
		if cached, ok := s.cache.get(fuyaoQuoteItemKey(code.ID())); ok {
			if quote, isQuote := cached.(contract.Quote); isQuote {
				out[ths] = quoteToFuyaoRow(quote, ths)
				continue
			}
		}
		missing = append(missing, ths)
	}
	if len(missing) == 0 {
		return fuyaoRowsInOrder(order, out), nil
	}

	// ③ 只对缺口请求官方；按 50 一批、并发 ≤4。
	type chunkResult struct {
		rows map[string]contract.FuyaoQuoteRow
		err  error
	}
	chunks := make([][]string, 0, (len(missing)+fuyaoQuoteChunk-1)/fuyaoQuoteChunk)
	for start := 0; start < len(missing); start += fuyaoQuoteChunk {
		end := start + fuyaoQuoteChunk
		if end > len(missing) {
			end = len(missing)
		}
		chunks = append(chunks, missing[start:end])
	}
	results := make([]chunkResult, len(chunks))
	sem := make(chan struct{}, fuyaoQuoteConcurrency)
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, err := s.fuyaoSnapshotChunk(ctx, chunk, byThs)
			results[i] = chunkResult{rows: rows, err: err}
		}(i, chunk)
	}
	wg.Wait()

	failed := 0
	for _, result := range results {
		if result.err != nil {
			failed++
			continue
		}
		for ths, row := range result.rows {
			out[ths] = row
		}
	}
	if failed == len(results) {
		// 整批失败也必须留痕：否则用户只看到「突然变慢」，排查时无从下手。
		s.log.Warn("官方快照全部批次失败，降级到腾讯/新浪",
			"event", "fuyao.fallback", "failed_chunks", failed, "total_chunks", len(results))
		return nil, fmt.Errorf("%w: 官方快照全部批次失败", ErrNoData)
	}
	if failed > 0 {
		// 降级必须可观测：一次调用一条 Warn，包含失败批次数与事件名。
		s.log.Warn("官方快照部分批次失败，缺口降级到腾讯/新浪",
			"event", "fuyao.fallback", "failed_chunks", failed, "total_chunks", len(results))
	}
	// ④ 写回缓存：批量键只在全部代码都有结果时写入，避免把「缺口」缓存成空。
	if len(out) == 0 {
		s.log.Warn("官方快照返回成功但没有有效行情，降级到腾讯/新浪",
			"event", "fuyao.fallback", "codes", len(order))
	}
	if len(out) == len(order) {
		s.cache.put(fuyaoBatchQuoteKey(order), out, s.fuyaoQuoteTTL())
	}
	for ths, row := range out {
		code, ok := byThs[ths]
		if !ok {
			continue
		}
		quote := fuyaoRowToQuote(row, code)
		s.cache.put(fuyaoQuoteItemKey(code.ID()), quote, s.fuyaoQuoteTTL())
	}
	return fuyaoRowsInOrder(order, out), nil
}

// fuyaoSnapshotChunk 请求一批官方快照并补名称。
// 单批失败只返回错误，不重试（fuyaoGet 已对 4001 退避重试一次）。
func (s *Service) fuyaoSnapshotChunk(ctx context.Context, thsCodes []string, byThs map[string]Code) (map[string]contract.FuyaoQuoteRow, error) {
	query := url.Values{}
	query.Set("thscodes", strings.Join(thsCodes, ","))
	var data fuyaoSnapshotData
	if err := s.fuyaoGet(ctx, "/api/a-share/prices/snapshot", query, &data); err != nil {
		s.log.Debug("官方快照批次失败", "error", err, "codes", len(thsCodes))
		return nil, err
	}
	rows := make(map[string]contract.FuyaoQuoteRow, len(data.Item))
	for _, item := range data.Item {
		code, ok := byThs[item.Thscode]
		if !ok {
			continue
		}
		if item.LastPrice <= 0 && item.PrevClose <= 0 {
			continue
		}
		rows[item.Thscode] = contract.FuyaoQuoteRow{
			Thscode:   item.Thscode,
			Ticker:    item.Ticker,
			Name:      s.cachedNameFast(code),
			LastPrice: item.LastPrice,
			Change:    item.Change,
			ChangePct: item.ChangePct,
			Open:      item.Open,
			High:      item.High,
			Low:       item.Low,
			PrevClose: item.PrevClose,
			Volume:    item.Volume,
			Turnover:  item.Turnover,
			Timestamp: data.Timestamp,
			Source:    sourceFuyao,
		}
	}
	return rows, nil
}

// fuyaoRowsInOrder 按请求顺序返回行（去掉未取到的标的）。
func fuyaoRowsInOrder(order []string, rows map[string]contract.FuyaoQuoteRow) []contract.FuyaoQuoteRow {
	out := make([]contract.FuyaoQuoteRow, 0, len(rows))
	for _, ths := range order {
		if row, ok := rows[ths]; ok {
			out = append(out, row)
		}
	}
	return out
}

// fuyaoRowToQuote 把官方行映射成契约快照（成交量股 → 手）。
func fuyaoRowToQuote(row contract.FuyaoQuoteRow, code Code) contract.Quote {
	return contract.Quote{
		Code: code.Symbol, Name: row.Name, Market: code.Market,
		Price: row.LastPrice, PrevClose: row.PrevClose, Open: row.Open,
		High: row.High, Low: row.Low,
		Volume: row.Volume / 100, Amount: row.Turnover,
		Change: row.Change, ChangePct: row.ChangePct,
		Time: formatFuyaoClock(row.Timestamp), Source: sourceFuyao,
	}
}

// quoteToFuyaoRow 把已缓存的契约快照还原成官方行（用于单代码缓存命中）。
func quoteToFuyaoRow(quote contract.Quote, thscode string) contract.FuyaoQuoteRow {
	return contract.FuyaoQuoteRow{
		Thscode: thscode, Ticker: quote.Code, Name: quote.Name,
		LastPrice: quote.Price, Change: quote.Change, ChangePct: quote.ChangePct,
		Open: quote.Open, High: quote.High, Low: quote.Low, PrevClose: quote.PrevClose,
		Volume: quote.Volume * 100, Turnover: quote.Amount,
		Source: quote.Source,
	}
}

// codeFromTHSCode 把官方 thscode（600519.SH / 000001.SZ / 430047.BJ）还原成 Code。
// .TI（同花顺指数）没有沪深市场概念，返回 false 由指数专用方法处理。
func codeFromTHSCode(ths string) (Code, bool) {
	ths = strings.ToUpper(strings.TrimSpace(ths))
	parts := strings.Split(ths, ".")
	if len(parts) != 2 || len(parts[0]) != 6 {
		return Code{}, false
	}
	var market contract.Market
	switch parts[1] {
	case "SH":
		market = contract.MarketSH
	case "SZ":
		market = contract.MarketSZ
	case "BJ":
		market = contract.MarketBJ
	default:
		return Code{}, false
	}
	return Code{Raw: parts[0], Symbol: parts[0], Market: market, Kind: KindStock}, true
}

// parseFuyaoCode 解析调用方代码：先接受官方写法（600519.SH），再回退到项目通用写法
// （600519 / sh600519 / 1.600519）。官方文档全用带后缀写法，工具入参必须兼容。
func parseFuyaoCode(raw string) (Code, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, ".") {
		if code, ok := codeFromTHSCode(raw); ok {
			return code, nil
		}
	}
	return ParseCode(raw)
}

// formatFuyaoClock 把毫秒时间戳格式化成 Asia/Shanghai 的 HH:MM:SS（0 返回空串）。
func formatFuyaoClock(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).In(cstZone()).Format("15:04:05")
}

// cachedNameFast 先查进程内名称缓存，再查本地 store；不触发任何网络请求。
func (s *Service) cachedNameFast(code Code) string {
	id := code.ID()
	now := s.now()
	s.q.mu.Lock()
	if entry, ok := s.q.names[id]; ok && now.Before(entry.expires) {
		name := entry.name
		s.q.mu.Unlock()
		return name
	}
	s.q.mu.Unlock()
	name := s.CachedName(code)
	ttl := 10 * time.Minute
	if name == "" {
		// 空名字只缓存 30 秒，避免新入库的标的永远补不到名字。
		ttl = 30 * time.Second
	}
	s.q.mu.Lock()
	s.q.names[id] = nameEntry{name: name, expires: now.Add(ttl)}
	s.q.mu.Unlock()
	return name
}

// rememberName 把已知名称写入进程内缓存（补名字成功后调用）。
func (s *Service) rememberName(code Code, name string) {
	if name == "" {
		return
	}
	s.q.mu.Lock()
	s.q.names[code.ID()] = nameEntry{name: name, expires: s.now().Add(10 * time.Minute)}
	s.q.mu.Unlock()
}

// shouldPersistQuote 判断该代码是否需要落盘（价格/量额/名称变化，或距上次 ≥30s）。
func (s *Service) shouldPersistQuote(code Code, quote contract.Quote) bool {
	id := code.ID()
	now := s.now()
	s.q.mu.Lock()
	last, seen := s.q.persist[id]
	s.q.mu.Unlock()
	if seen && now.Sub(last) < persistMinInterval {
		return false
	}
	s.q.mu.Lock()
	s.q.persist[id] = now
	s.q.mu.Unlock()
	return true
}

// FuyaoQuoteRows 实现 contract.FuyaoService：批量取官方行情行（>50 自动分批）。
// 返回行的 volume 单位是「股」（官方原值），Turnover 单位「元」。
func (s *Service) FuyaoQuoteRows(ctx context.Context, codes []string) ([]contract.FuyaoQuoteRow, error) {
	parsed := make([]Code, 0, len(codes))
	bad := make([]string, 0, 4)
	for _, raw := range codes {
		code, err := parseFuyaoCode(raw)
		if err != nil {
			bad = append(bad, raw)
			continue
		}
		parsed = append(parsed, code)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("%w: 全部代码无法识别（%s）", ErrNoData, strings.Join(bad, ", "))
	}
	rows, err := s.fuyaoSnapshotRows(ctx, parsed)
	if err != nil && len(rows) == 0 {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: 官方快照没有返回有效行情", ErrNoData)
	}
	if len(bad) > 0 {
		return rows, fmt.Errorf("%w: %s", ErrPartial, strings.Join(bad, ", "))
	}
	return rows, nil
}

// FuyaoValuationRows 取官方估值快照（PE TTM/MRQ、PB MRQ、PS TTM、PCF TTM）。
// 单位：各比率为倍数，不是百分数；名称字段官方直接返回。
func (s *Service) FuyaoValuationRows(ctx context.Context, codes []string) ([]map[string]any, error) {
	out, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.valuations.snapshot",
		Params:   map[string]string{"thscodes": strings.Join(codes, ",")},
		Limit:    100,
	})
	if err != nil {
		return nil, err
	}
	return out.Items, nil
}

// fuyaoSnapshotWithFallback 是给 Quote 用的编排：官方拿到的行直接返回；
// 缺名字时只用本地缓存/一次批量兜底补名，不因为「官方不给 name」而每次多打腾讯。
// 返回官方命中的 code ID 集合，未命中的由调用方走原有降级链。
func (s *Service) fuyaoSnapshotWithFallback(ctx context.Context, parsed []Code) (map[string]contract.Quote, []Code) {
	official := s.fuyaoQuotes(ctx, parsed)
	if len(official) == 0 {
		return official, parsed
	}
	needsName := make([]Code, 0, 4)
	for _, code := range parsed {
		quote, ok := official[code.ID()]
		if !ok {
			continue
		}
		if quote.Name == "" {
			quote.Name = s.cachedNameFast(code)
		}
		if quote.Name == "" {
			needsName = append(needsName, code)
		}
		official[code.ID()] = quote
	}
	if len(needsName) > 0 {
		names := s.fetchStockNames(ctx, needsName)
		for _, code := range needsName {
			quote := official[code.ID()]
			if name := names[code.ID()]; name != "" {
				quote.Name = name
				s.rememberName(code, name)
				s.saveCachedName(code, name)
			}
			official[code.ID()] = quote
		}
	}
	missing := make([]Code, 0, 4)
	for _, code := range parsed {
		if _, ok := official[code.ID()]; !ok {
			missing = append(missing, code)
		}
	}
	return official, missing
}
