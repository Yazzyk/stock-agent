// Package datasource 是 A 股数据层：统一 HTTP 客户端 + 多源降级 + 归一化 + 缓存 + 自选同步。
//
// 设计取向是「诚实优先」：
//   - 成交量一律以「手」对外，各源原始单位（股/手）在解析处换算，绝不靠调用方猜；
//   - 复权只有东财 fqt 能提供真序列，同花顺 v6 line 是不复权，请求 qfq/hfq 时宁可报错也不冒充；
//   - 部分失败返回成功的部分 + 可判定的 ErrPartial，不把整批结果吞掉。
//
// 上游端点与实测结论见 docs/PLAN.md §4 与本包 testdata/ 下的真实响应样本。
package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 可以判定（errors.Is）的业务错误。插件与工具据此给出可操作提示。
var (
	// ErrAdjustUnsupported 表示目标数据源给不出可信的复权序列（目前只有东财 fqt 可以）。
	ErrAdjustUnsupported = errors.New("datasource: 当前行情源不提供复权序列")
	// ErrPeriodUnsupported 表示该周期没有可靠数据源。
	ErrPeriodUnsupported = errors.New("datasource: 当前行情源不支持该周期")
	// ErrPartial 表示部分代码取数失败，返回值里仍含成功项。
	ErrPartial = errors.New("datasource: 部分代码取数失败")
	// ErrNoData 表示整批都没有取到数据。
	ErrNoData = errors.New("datasource: 没有取到数据")
)

const (
	defaultBarLimit  = 250
	maxBarLimit      = 2000
	defaultRankLimit = 10
	maxRankLimit     = 100
	defaultSearchCap = 10
	maxSearchCap     = 50
	maxQuoteCodes    = 50
	// thsPerCodeLimit 是「逐只抓同花顺 realhead」的代码数上限。
	// 超过这个数量就只走批量兜底源（腾讯/新浪一次请求）：同花顺没有批量接口，
	// 逐只抓 50 只按 4 QPS 要 12 秒，面板根本等不起；批量源价格与名称同样可信。
	thsPerCodeLimit = 10
	// nameBatchSize 是补名称时每批请求的代码数。
	nameBatchSize = 60
	// nameMaxCodes 是单次同步最多回填名称的代码数（防止一次同步打出几十个请求）。
	nameMaxCodes = 300
)

// 同花顺周期代码（实测：01 日、11 周、21 月、41 30分钟、50 60分钟）。
var thsPeriodCodes = map[contract.Period]string{
	contract.PeriodDay:   "01",
	contract.PeriodWeek:  "11",
	contract.PeriodMonth: "21",
	contract.PeriodMin60: "50",
	contract.PeriodMin30: "41",
}

// 东财周期代码（klt）。
var emPeriodCodes = map[contract.Period]int{
	contract.PeriodDay:   101,
	contract.PeriodWeek:  102,
	contract.PeriodMonth: 103,
	contract.PeriodMin60: 60,
	contract.PeriodMin30: 30,
	contract.PeriodMin15: 15,
	contract.PeriodMin5:  5,
	contract.PeriodMin1:  1,
}

// Config 是数据层装配参数（bootstrap 用，签名已冻结见 docs/API.md §8）。
type Config struct {
	Guard       contract.Guard
	Store       contract.Store
	Logger      *slog.Logger
	DataDir     string
	HTTPTimeout time.Duration
}

// Service 实现 contract.DataService。
type Service struct {
	cfg    Config
	log    *slog.Logger
	client *client
	cache  *cache
	now    func() time.Time
	dir    string
	// home 用于定位同花顺容器；生产取用户主目录，测试可注入临时目录。
	home string
}

var _ contract.DataService = (*Service)(nil)

// New 组装数据服务。Guard 必填（所有出网请求都要过白名单）。
func New(cfg Config) (contract.DataService, error) {
	if cfg.Guard == nil {
		return nil, errors.New("datasource: 需要 Guard（网络白名单）")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now
	home, _ := os.UserHomeDir()
	s := &Service{
		cfg:    cfg,
		log:    logger,
		client: newClient(cfg, now),
		cache:  newCache(now),
		now:    now,
		dir:    strings.TrimSpace(cfg.DataDir),
		home:   home,
	}
	return s, nil
}

// ---------- 快照 ----------

// Quote 返回一批代码的最新快照。
//
// 链路：内存缓存 → 腾讯批量快照（交叉校验基准）→ 同花顺 realhead（主链路，校验通过才采信）
// → 新浪兜底 → store 上次成功快照（stale=true）。
// 部分失败时返回成功项与 ErrPartial，全失败才返回 ErrNoData。
func (s *Service) Quote(ctx context.Context, codes []string) ([]contract.Quote, error) {
	if len(codes) == 0 {
		return nil, errors.New("datasource: 需要至少一个代码")
	}
	if len(codes) > maxQuoteCodes {
		return nil, fmt.Errorf("datasource: 一次最多查询 %d 个代码，实际 %d 个", maxQuoteCodes, len(codes))
	}

	parsed := make([]Code, 0, len(codes))
	failures := make([]string, 0, 4)
	for _, raw := range codes {
		code, err := ParseCode(raw)
		if err != nil {
			failures = append(failures, raw)
			continue
		}
		parsed = append(parsed, code)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("%w: 全部代码无法识别（%s）", ErrNoData, strings.Join(failures, ", "))
	}

	fallback := s.batchFallbackQuotes(ctx, parsed)
	useTHSPerCode := len(parsed) <= thsPerCodeLimit
	out := make([]contract.Quote, 0, len(parsed))
	for _, code := range parsed {
		quote, err := s.quoteOne(ctx, code, fallback[code.ID()], useTHSPerCode)
		if err != nil {
			s.log.Warn("快照取数失败", "code", code.Raw, "error", err)
			failures = append(failures, code.Raw)
			continue
		}
		out = append(out, quote)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoData, strings.Join(failures, ", "))
	}
	if len(failures) > 0 {
		return out, fmt.Errorf("%w: %s", ErrPartial, strings.Join(failures, ", "))
	}
	return out, nil
}

// batchFallbackQuotes 用腾讯（再新浪）一次抓多个代码，既做交叉校验基准也做降级来源。
func (s *Service) batchFallbackQuotes(ctx context.Context, codes []Code) map[string]contract.Quote {
	out := make(map[string]contract.Quote, len(codes))
	symbols := make([]string, 0, len(codes))
	bySymbol := make(map[string]Code, len(codes))
	kinds := make(map[string]Kind, len(codes))
	for _, code := range codes {
		symbol := code.MarketSymbol()
		if symbol == "" {
			continue
		}
		if _, exists := bySymbol[symbol]; exists {
			continue
		}
		symbols = append(symbols, symbol)
		bySymbol[symbol] = code
		kinds[symbol] = code.Kind
	}
	if len(symbols) == 0 {
		return out
	}

	if body, err := s.client.get(ctx, request{source: sourceTencent, url: txQuoteURL(symbols), gbk: true}); err == nil {
		for symbol, tx := range parseTxQuotes(body) {
			code, ok := bySymbol[symbol]
			if !ok {
				continue
			}
			quote := txQuoteToContract(tx, code.Market)
			quote.Code = code.Symbol
			out[code.ID()] = quote
		}
	} else {
		s.log.Debug("腾讯快照失败，转新浪兜底", "error", err)
	}
	if len(out) == len(bySymbol) {
		return out
	}

	missing := make([]string, 0, len(bySymbol))
	for symbol := range bySymbol {
		if _, ok := out[bySymbol[symbol].ID()]; !ok {
			missing = append(missing, symbol)
		}
	}
	if len(missing) == 0 {
		return out
	}
	if body, err := s.client.get(ctx, request{
		source:  sourceSina,
		url:     sinaQuoteURL(missing),
		referer: "https://finance.sina.com.cn/",
		gbk:     true,
	}); err == nil {
		for symbol, sinaQuote := range parseSinaQuotes(body, kinds) {
			code, ok := bySymbol[symbol]
			if !ok {
				continue
			}
			if _, exists := out[code.ID()]; exists {
				continue
			}
			quote := contract.Quote{
				Code:      code.Symbol,
				Name:      sinaQuote.Name,
				Market:    code.Market,
				Price:     sinaQuote.Price,
				PrevClose: sinaQuote.PrevClose,
				Open:      sinaQuote.Open,
				High:      sinaQuote.High,
				Low:       sinaQuote.Low,
				Volume:    sinaQuote.VolumeHand,
				Amount:    sinaQuote.Amount,
				Time:      sinaQuote.Time,
				Source:    sourceSina,
			}
			if quote.PrevClose > 0 {
				quote.Change = quote.Price - quote.PrevClose
				quote.ChangePct = quote.Change / quote.PrevClose * 100
			}
			out[code.ID()] = quote
		}
	} else {
		s.log.Debug("新浪快照失败", "error", err)
	}
	return out
}

// quoteOne 取单个代码的快照：同花顺优先，交叉校验不过则用兜底源。
func (s *Service) quoteOne(ctx context.Context, code Code, fallback contract.Quote, useTHS bool) (contract.Quote, error) {
	cacheKey := "quote:" + code.ID()
	if cached, ok := s.cache.get(cacheKey); ok {
		if quote, isQuote := cached.(contract.Quote); isQuote {
			return quote, nil
		}
	}

	var (
		quote  contract.Quote
		source string
		err    error
	)
	if symbol := code.THSSymbol(); useTHS && symbol != "" {
		if quote, err = s.fetchTHSQuote(ctx, code, symbol); err == nil {
			source = sourceTHS
			if fallback.Name != "" || fallback.Price > 0 {
				if ok, reason := crossValidate(quote, fallback); !ok {
					s.log.Warn("同花顺快照与兜底源不一致，改用兜底源", "code", code.Raw, "reason", reason)
					quote, source = fallback, fallback.Source
				}
			}
		} else {
			s.log.Debug("同花顺快照失败", "code", code.Raw, "error", err)
		}
	}
	if quote.Name == "" && fallback.Name != "" {
		quote, source = fallback, fallback.Source
	}
	if quote.Name == "" || quote.Price <= 0 && quote.PrevClose <= 0 {
		// 所有实时源都失败：用 store 里上次成功快照兜底，并显式标记 stale
		if cached, err := s.loadCachedQuote(code); err == nil {
			cached.Stale = true
			return cached, nil
		}
		return contract.Quote{}, fmt.Errorf("%w: %s", ErrNoData, code.Raw)
	}
	if quote.Source == "" {
		quote.Source = source
	}
	quote = normalizeQuoteState(quote)
	s.cache.put(cacheKey, quote, ttlQuote)
	s.saveCachedQuote(code, quote)
	return quote, nil
}

// fetchTHSQuote 抓同花顺 realhead 并转成契约 Quote。
func (s *Service) fetchTHSQuote(ctx context.Context, code Code, symbol string) (contract.Quote, error) {
	body, err := s.client.get(ctx, request{source: sourceTHS, url: thsRealheadURL(symbol)})
	if err != nil {
		return contract.Quote{}, err
	}
	head, err := parseTHSRealhead(body, code)
	if err != nil {
		return contract.Quote{}, err
	}
	if head.Price <= 0 && head.PrevClose <= 0 {
		return contract.Quote{}, fmt.Errorf("%w: 同花顺快照无有效价格", ErrNoData)
	}
	return contract.Quote{
		Code:      code.Symbol,
		Name:      head.Name,
		Market:    head.Market,
		Price:     head.Price,
		PrevClose: head.PrevClose,
		Open:      head.Open,
		High:      head.High,
		Low:       head.Low,
		Change:    head.Change,
		ChangePct: head.ChangePct,
		Volume:    head.VolumeHand,
		Amount:    head.Amount,
		Turnover:  head.Turnover,
		Amplitude: head.Amplitude,
		Time:      head.Time,
		Source:    sourceTHS,
	}, nil
}

// crossValidate 用腾讯/新浪快照校验同花顺的关键字段。
// 价差 0.5% 以内、量差 20% 以内视为一致；越界说明某一源明显过期，宁可换源也不采纳可疑值。
func crossValidate(primary, fallback contract.Quote) (bool, string) {
	if fallback.Price <= 0 || primary.Price <= 0 {
		return true, ""
	}
	priceGap := abs(primary.Price-fallback.Price) / fallback.Price
	if priceGap > 0.005 {
		return false, fmt.Sprintf("价格差异 %.2f%%（同花顺 %.2f / 兜底 %.2f）", priceGap*100, primary.Price, fallback.Price)
	}
	if primary.Volume > 0 && fallback.Volume > 0 {
		volumeGap := abs(primary.Volume-fallback.Volume) / fallback.Volume
		if volumeGap > 0.2 {
			return false, fmt.Sprintf("成交量差异 %.1f%%（同花顺 %.0f 手 / 兜底 %.0f 手）", volumeGap*100, primary.Volume, fallback.Volume)
		}
	}
	return true, ""
}

// normalizeQuoteState 处理停牌/未开盘：价格缺失用昨收兜底，成交量与成交额都为 0 视为停牌并标 stale。
func normalizeQuoteState(quote contract.Quote) contract.Quote {
	if quote.Price <= 0 && quote.PrevClose > 0 {
		quote.Price = quote.PrevClose
		quote.Stale = true
	}
	if quote.Volume == 0 && quote.Amount == 0 {
		quote.Stale = true
	}
	if quote.Change == 0 && quote.PrevClose > 0 && quote.Price > 0 {
		quote.Change = quote.Price - quote.PrevClose
	}
	if quote.ChangePct == 0 && quote.PrevClose > 0 {
		quote.ChangePct = quote.Change / quote.PrevClose * 100
	}
	return quote
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// fetchStockNames 用批量快照（腾讯优先、新浪兜底）给一批代码取名称。
//
// 为什么单独做一条路径：自选同步要一次给上百只票补名字，逐只抓同花顺在
// 4 QPS 限速下要几十秒；批量源一次能拿 60 只，且名称字段与逐只抓的一致。
func (s *Service) fetchStockNames(ctx context.Context, codes []Code) map[string]string {
	out := make(map[string]string, len(codes))
	if len(codes) == 0 || ctx.Err() != nil {
		return out
	}
	if len(codes) > nameMaxCodes {
		codes = codes[:nameMaxCodes]
	}
	for start := 0; start < len(codes); start += nameBatchSize {
		end := start + nameBatchSize
		if end > len(codes) {
			end = len(codes)
		}
		chunk := codes[start:end]
		symbols := make([]string, 0, len(chunk))
		bySymbol := make(map[string]Code, len(chunk))
		kinds := make(map[string]Kind, len(chunk))
		for _, code := range chunk {
			symbol := code.MarketSymbol()
			if symbol == "" {
				continue
			}
			if _, exists := bySymbol[symbol]; exists {
				continue
			}
			symbols = append(symbols, symbol)
			bySymbol[symbol] = code
			kinds[symbol] = code.Kind
		}
		if len(symbols) == 0 {
			continue
		}
		if body, err := s.client.get(ctx, request{source: sourceTencent, url: txQuoteURL(symbols), gbk: true}); err == nil {
			for symbol, quote := range parseTxQuotes(body) {
				code, ok := bySymbol[symbol]
				if !ok || quote.Name == "" {
					continue
				}
				out[code.ID()] = quote.Name
			}
		}
		missing := make([]string, 0, len(bySymbol))
		for symbol := range bySymbol {
			if _, ok := out[bySymbol[symbol].ID()]; !ok {
				missing = append(missing, symbol)
			}
		}
		if len(missing) == 0 {
			continue
		}
		body, err := s.client.get(ctx, request{
			source:  sourceSina,
			url:     sinaQuoteURL(missing),
			referer: "https://finance.sina.com.cn/",
			gbk:     true,
		})
		if err != nil {
			continue
		}
		for symbol, quote := range parseSinaQuotes(body, kinds) {
			code, ok := bySymbol[symbol]
			if !ok || quote.Name == "" {
				continue
			}
			out[code.ID()] = quote.Name
		}
	}
	return out
}

// saveCachedName 把名称写进 market_cache，离线时自选列表也能显示名字。
func (s *Service) saveCachedName(code Code, name string) {
	if s.cfg.Store == nil || name == "" {
		return
	}
	raw, err := json.Marshal(map[string]string{"code": code.Symbol, "name": name})
	if err != nil {
		return
	}
	if err := s.cfg.Store.Put(bucketMarketCache, prefixSymbolName+code.ID(), raw); err != nil {
		s.log.Debug("缓存名称写入失败", "code", code.Raw, "error", err)
	}
}

// ---------- K 线 ----------

// Bars 返回指定周期 K 线，按时间升序，体积单位手。
//
// 复权说明：只有东财 fqt 能给真复权序列；同花顺 v6 line 是不复权。
// 因此 adj=qfq/hfq 时若东财不可用，返回 ErrAdjustUnsupported 而不是拿不复权数据冒充。
func (s *Service) Bars(ctx context.Context, code string, p contract.Period, limit int, adj contract.Adjust) ([]contract.Bar, error) {
	parsed, err := ParseCode(code)
	if err != nil {
		return nil, err
	}
	if p == "" {
		p = contract.PeriodDay
	}
	if limit <= 0 {
		limit = defaultBarLimit
	}
	if limit > maxBarLimit {
		limit = maxBarLimit
	}
	cacheKey := "bars:" + parsed.ID() + ":" + string(p) + ":" + string(adj)
	if cached, ok := s.cache.get(cacheKey); ok {
		if bars, isBars := cached.([]contract.Bar); isBars && len(bars) >= limit {
			return lastBars(bars, limit), nil
		}
	}

	if adj != contract.AdjustNone {
		bars, err := s.eastMoneyBars(ctx, parsed, p, limit, adj)
		if err == nil {
			s.cache.put(cacheKey, bars, ttlBars)
			return lastBars(bars, limit), nil
		}
		return nil, fmt.Errorf("%w（%s 复权需要东财 K 线接口，本次失败：%v）；请改用不复权", ErrAdjustUnsupported, adj, err)
	}

	var (
		bars   []contract.Bar
		srcErr []string
	)
	if symbol := parsed.THSSymbol(); symbol != "" {
		bars, err = s.thsBars(ctx, parsed, p, limit)
		if err == nil {
			s.cache.put(cacheKey, bars, ttlBars)
			return lastBars(bars, limit), nil
		}
		srcErr = append(srcErr, "同花顺: "+err.Error())
	}
	if bars, err = s.eastMoneyBars(ctx, parsed, p, limit, contract.AdjustNone); err == nil {
		s.cache.put(cacheKey, bars, ttlBars)
		return lastBars(bars, limit), nil
	} else {
		srcErr = append(srcErr, "东财: "+err.Error())
	}
	if symbol := parsed.MarketSymbol(); symbol != "" && p == contract.PeriodDay {
		if bars, err = s.sinaBars(ctx, symbol, limit); err == nil {
			s.cache.put(cacheKey, bars, ttlBars)
			return lastBars(bars, limit), nil
		}
		srcErr = append(srcErr, "新浪: "+err.Error())
	}
	return nil, fmt.Errorf("%w: %s %s（%s）", ErrNoData, parsed.Raw, p, strings.Join(srcErr, "；"))
}

// thsBars 从同花顺拉 K 线。
// 日线走「按年文件」拼接（能覆盖任意长度且不需要 all.js 的差分压缩格式），
// 周/月/分钟线走 last.js（最近 140 根，足够算 MA60）。
func (s *Service) thsBars(ctx context.Context, code Code, p contract.Period, limit int) ([]contract.Bar, error) {
	symbol := code.THSSymbol()
	periodCode, ok := thsPeriodCodes[p]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPeriodUnsupported, p)
	}
	if p != contract.PeriodDay {
		body, err := s.client.get(ctx, request{source: sourceTHS, url: thsLineURL(symbol, periodCode, "last")})
		if err != nil {
			return nil, err
		}
		data, err := parseTHSLine(body, code)
		if err != nil {
			return nil, err
		}
		return data.Bars, nil
	}

	// 日线：从当年往回逐年抓，直到凑够 limit 或最多回看 5 年
	var collected [][]contract.Bar
	year := s.now().Year()
	for fetched := 0; fetched < 5 && len(lastBars(flattenBars(collected), limit)) < limit; fetched++ {
		body, err := s.client.get(ctx, request{
			source: sourceTHS,
			url:    thsLineURL(symbol, periodCode, fmt.Sprintf("%d", year)),
		})
		if err != nil {
			if len(collected) > 0 {
				break
			}
			return nil, err
		}
		data, err := parseTHSLine(body, code)
		if err != nil {
			if len(collected) > 0 {
				break
			}
			return nil, err
		}
		collected = append([][]contract.Bar{data.Bars}, collected...)
		year--
	}
	merged := flattenBars(collected)
	if len(merged) == 0 {
		return nil, ErrEmptyResponse
	}
	return merged, nil
}

func flattenBars(groups [][]contract.Bar) []contract.Bar {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	out := make([]contract.Bar, 0, total)
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func lastBars(bars []contract.Bar, limit int) []contract.Bar {
	if limit <= 0 || len(bars) <= limit {
		return bars
	}
	return bars[len(bars)-limit:]
}

// eastMoneyBars 从东财拉 K 线（唯一能提供复权的来源）。
func (s *Service) eastMoneyBars(ctx context.Context, code Code, p contract.Period, limit int, adj contract.Adjust) ([]contract.Bar, error) {
	secid := code.SecID()
	if secid == "" {
		return nil, fmt.Errorf("%w: %s", ErrNoData, code.Raw)
	}
	klt, ok := emPeriodCodes[p]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrPeriodUnsupported, p)
	}
	adjust := 0
	switch adj {
	case contract.AdjustQfq:
		adjust = 1
	case contract.AdjustHfq:
		adjust = 2
	}
	body, err := s.client.get(ctx, request{
		source:  sourceEastMoney,
		url:     emKlineURL(secid, klt, adjust, limit),
		referer: "https://quote.eastmoney.com/",
	})
	if err != nil {
		return nil, err
	}
	data, err := parseEMKline(body)
	if err != nil {
		return nil, err
	}
	return data.Bars, nil
}

// sinaBars 是新浪日 K 线兜底（sina.cn 不在默认白名单内，被 Guard 拒绝时按失败处理）。
func (s *Service) sinaBars(ctx context.Context, symbol string, limit int) ([]contract.Bar, error) {
	body, err := s.client.get(ctx, request{
		source:  sourceSina,
		url:     sinaKlineURL(symbol, limit),
		referer: "https://finance.sina.com.cn/",
	})
	if err != nil {
		return nil, err
	}
	return parseSinaKline(body)
}

// ---------- 资金流 ----------

// StockFundFlow 返回个股最新资金流：东财优先，新浪兜底。
func (s *Service) StockFundFlow(ctx context.Context, code string) (contract.FundFlow, error) {
	parsed, err := ParseCode(code)
	if err != nil {
		return contract.FundFlow{}, err
	}
	cacheKey := "flow:stock:" + parsed.ID()
	if cached, ok := s.cache.get(cacheKey); ok {
		if flow, isFlow := cached.(contract.FundFlow); isFlow {
			return flow, nil
		}
	}

	if secid := parsed.SecID(); secid != "" {
		body, err := s.client.get(ctx, request{
			source:  sourceEastMoney,
			url:     emFFlowURL(secid),
			referer: "https://quote.eastmoney.com/",
		})
		if err == nil {
			flow, parseErr := parseEMFFlow(body)
			if parseErr == nil {
				out := contract.FundFlow{
					Code:      parsed.Symbol,
					Name:      flow.Name,
					Kind:      string(KindStock),
					Date:      flow.Date,
					MainNet:   flow.MainNet,
					SuperNet:  flow.SuperNet,
					LargeNet:  flow.LargeNet,
					MediumNet: flow.MediumNet,
					SmallNet:  flow.SmallNet,
					Source:    sourceEastMoney,
				}
				s.cache.put(cacheKey, out, ttlFlow)
				return out, nil
			}
			s.log.Debug("东财资金流解析失败", "code", code, "error", parseErr)
		} else {
			s.log.Debug("东财资金流失败，转新浪兜底", "code", code, "error", err)
		}
	}

	symbol := parsed.MarketSymbol()
	if symbol == "" {
		return contract.FundFlow{}, fmt.Errorf("%w: %s", ErrNoData, code)
	}
	body, err := s.client.get(ctx, request{source: sourceSina, url: sinaMoneyFlowURL(symbol)})
	if err != nil {
		return contract.FundFlow{}, err
	}
	flow, err := parseSinaMoneyFlow(body)
	if err != nil {
		return contract.FundFlow{}, err
	}
	out := contract.FundFlow{
		Code:      parsed.Symbol,
		Name:      flow.Name,
		Kind:      string(KindStock),
		MainNet:   flow.MainNet,
		SuperNet:  flow.SuperNet,
		LargeNet:  flow.LargeNet,
		MediumNet: flow.MediumNet,
		SmallNet:  flow.SmallNet,
		ChangePct: flow.ChangePct,
		Source:    sourceSina,
	}
	if flow.AmountYuan != 0 {
		out.MainPct = out.MainNet / abs(flow.AmountYuan) * 100
	}
	s.cache.put(cacheKey, out, ttlFlow)
	return out, nil
}

// FundFlowRank 返回板块或概念的资金流排名（主力净流入降序）。
// kind 为空时默认 sector（product 要求空参数不报错）；limit<=0 默认 10。
func (s *Service) FundFlowRank(ctx context.Context, kind string, limit int) ([]contract.FundFlow, error) {
	normalized, err := normalizeSectorKind(kind)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultRankLimit
	}
	if limit > maxRankLimit {
		limit = maxRankLimit
	}
	cacheKey := fmt.Sprintf("flow:rank:%s:%d", normalized, limit)
	if cached, ok := s.cache.get(cacheKey); ok {
		if items, isItems := cached.([]contract.FundFlow); isItems {
			return items, nil
		}
	}
	body, err := s.client.get(ctx, request{
		source:  sourceEastMoney,
		url:     emSectorFlowURL(normalized, limit),
		referer: "https://data.eastmoney.com/",
	})
	if err != nil {
		return nil, err
	}
	rows, err := parseEMBkzj(body)
	if err != nil {
		return nil, err
	}
	out := make([]contract.FundFlow, 0, limit)
	for i, row := range rows {
		if len(out) >= limit {
			break
		}
		out = append(out, contract.FundFlow{
			Code:    row.Code,
			Name:    row.Name,
			Kind:    string(normalized),
			MainNet: row.MainNet,
			Rank:    i + 1,
			Source:  sourceEastMoney,
		})
	}
	s.cache.put(cacheKey, out, ttlFlow)
	return out, nil
}

// Sectors 返回板块或概念列表（含涨跌幅与主力净额）。kind 为空时默认 sector。
func (s *Service) Sectors(ctx context.Context, kind string) ([]contract.Sector, error) {
	normalized, err := normalizeSectorKind(kind)
	if err != nil {
		return nil, err
	}
	const sectorLimit = 100
	cacheKey := "sectors:" + string(normalized)
	if cached, ok := s.cache.get(cacheKey); ok {
		if items, isItems := cached.([]contract.Sector); isItems {
			return items, nil
		}
	}
	body, err := s.client.get(ctx, request{
		source:  sourceEastMoney,
		url:     emSectorListURL(normalized, sectorLimit),
		referer: "https://quote.eastmoney.com/",
	})
	if err != nil {
		return nil, err
	}
	rows, err := parseEMClist(body)
	if err != nil {
		return nil, err
	}
	out := make([]contract.Sector, 0, len(rows))
	for _, row := range rows {
		out = append(out, contract.Sector{
			Code:      row.Code,
			Name:      row.Name,
			Kind:      string(normalized),
			ChangePct: row.ChangePct,
			MainNet:   row.MainNet,
		})
	}
	s.cache.put(cacheKey, out, ttlFlow)
	return out, nil
}

// normalizeSectorKind 归一板块类别：空串默认 sector —— product 明确要求空参数不报错。
func normalizeSectorKind(kind string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "sector", "bk", "industry", "行业":
		return KindSector, nil
	case "concept", "概念":
		return KindConcept, nil
	default:
		return "", fmt.Errorf("datasource: kind 只支持 sector/concept，实际 %q", kind)
	}
}

// ---------- 搜索 ----------

// Search 按关键词搜代码：东财 suggest 优先，腾讯 smartbox 兜底。
func (s *Service) Search(ctx context.Context, keyword string) ([]contract.StockRef, error) {
	text := strings.TrimSpace(keyword)
	if text == "" {
		return nil, errors.New("datasource: 搜索关键词不能为空")
	}
	cacheKey := "search:" + text
	if cached, ok := s.cache.get(cacheKey); ok {
		if items, isItems := cached.([]contract.StockRef); isItems {
			return items, nil
		}
	}

	var suggestions []StockSuggestion
	if body, err := s.client.get(ctx, request{source: sourceEastMoney, url: emSuggestURL(text, defaultSearchCap)}); err == nil {
		if parsed, parseErr := parseEMSuggest(body); parseErr == nil {
			suggestions = parsed
		} else {
			s.log.Debug("东财搜索解析失败，转腾讯兜底", "error", parseErr)
		}
	} else {
		s.log.Debug("东财搜索失败，转腾讯兜底", "error", err)
	}
	if len(suggestions) == 0 {
		body, err := s.client.get(ctx, request{source: sourceTencent, url: txSuggestURL(text)})
		if err != nil {
			return nil, err
		}
		suggestions = parseTxSuggest(body)
	}

	out := make([]contract.StockRef, 0, len(suggestions))
	seen := make(map[string]bool, len(suggestions))
	for _, item := range suggestions {
		if len(out) >= maxSearchCap {
			break
		}
		code, err := ParseCode(string(item.Market) + item.Code)
		if err != nil {
			continue
		}
		if seen[code.ID()] {
			continue
		}
		seen[code.ID()] = true
		ref := contract.StockRef{Code: code.Symbol, Name: item.Name, Market: code.Market}
		if code.Kind == KindIndex {
			ref.Kind = string(KindIndex)
		}
		out = append(out, ref)
	}
	s.cache.put(cacheKey, out, time.Minute)
	return out, nil
}

// ---------- 健康 ----------

// Health 返回各数据源最近一次调用结果（不发新请求，避免状态栏刷新打出网络风暴）。
func (s *Service) Health(_ context.Context) map[string]string {
	out := s.client.health()
	out[sourceStore] = s.storeHealth()
	out["cache"] = fmt.Sprintf("%d 项", s.cache.size())
	return out
}

func (s *Service) storeHealth() string {
	if s.cfg.Store == nil {
		return "未接入（用量与自选不落盘）"
	}
	return "可用：" + s.cachePathHint()
}

func (s *Service) cachePathHint() string {
	if s.dir == "" {
		return "未设置数据目录"
	}
	return s.dir
}

// ---------- store 快照兜底 ----------

const (
	bucketMarketCache = "market_cache"
	prefixCachedQuote = "quote/"
	prefixSymbolName  = "name/"
)

func (s *Service) saveCachedQuote(code Code, quote contract.Quote) {
	if s.cfg.Store == nil || quote.Name == "" {
		return
	}
	if raw, err := json.Marshal(quote); err == nil {
		if err := s.cfg.Store.Put(bucketMarketCache, prefixCachedQuote+code.ID(), raw); err != nil {
			s.log.Debug("缓存快照写入失败", "code", code.Raw, "error", err)
		}
	}
	if name, err := json.Marshal(map[string]string{"code": code.Symbol, "name": quote.Name}); err == nil {
		if err := s.cfg.Store.Put(bucketMarketCache, prefixSymbolName+code.ID(), name); err != nil {
			s.log.Debug("缓存名称写入失败", "code", code.Raw, "error", err)
		}
	}
}

func (s *Service) loadCachedQuote(code Code) (contract.Quote, error) {
	if s.cfg.Store == nil {
		return contract.Quote{}, contract.ErrNotFound
	}
	raw, err := s.cfg.Store.Get(bucketMarketCache, prefixCachedQuote+code.ID())
	if err != nil {
		return contract.Quote{}, err
	}
	var quote contract.Quote
	if err := json.Unmarshal(raw, &quote); err != nil {
		return contract.Quote{}, err
	}
	quote.Stale = true
	quote.Source = sourceStore
	return quote, nil
}

// CachedName 返回本地缓存的名称（离线时给自选列表补名字）。
func (s *Service) CachedName(code Code) string {
	if s.cfg.Store == nil {
		return ""
	}
	raw, err := s.cfg.Store.Get(bucketMarketCache, prefixSymbolName+code.ID())
	if err != nil {
		return ""
	}
	var payload struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	return payload.Name
}
