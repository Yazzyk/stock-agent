// Package datasource 是 A 股数据层：统一 HTTP 客户端 + 多源降级 + 归一化 + 缓存 + 自选同步。
//
// 设计取向是「诚实优先」：
//   - 成交量一律以「手」对外，各源原始单位（股/手）在解析处换算，绝不靠调用方猜；
//   - 上游只有同花顺 / 腾讯 / 新浪三家，**没有任何东财接口**；
//   - 复权：前复权与后复权都走腾讯 fqkline（qfqday / hfqday 两套键），
//     绝不用前复权冒充后复权。实测同花顺 v6 line 返回的本身就是前复权序列
//     （证据见 tencent_kline.go 顶部），不要再按「同花顺是不复权」这个错误前提写降级；
//   - 资金流只有新浪一口径：个股当日 rN_in-rN_out，多日/板块用 ssl_* 系列，
//     其中板块榜是「全部资金净额」而不是「主力净额」（见 sina_flow.go 顶部）；
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
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 可以判定（errors.Is）的业务错误。插件与工具据此给出可操作提示。
var (
	// ErrAdjustUnsupported 表示目标数据源给不出可信的复权序列（qfq/hfq 都只有腾讯 fqkline 一条链路）。
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

// （腾讯 fqkline 只有日/周/月，其余周期走同花顺或直接报 ErrPeriodUnsupported。）
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
	// webLoginInvalid 记住「上一次在线请求被服务端判定登录态失效」的事实（不落盘）。
	// 没有它，过期 cookie 会让诊断一直报 ok，前端就会反复自动同步、反复失败。
	webLoginInvalid string
	webLoginMu      sync.Mutex
	// fy 是官方金融数据 API 的配置缓存（Key 来源、baseUrl、开关）。
	fy fuyaoState
	// q 是官方快照的进程内辅助状态（名称缓存、落盘节流）。
	q quoteState
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
		q:      newQuoteState(),
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

	// 官方 API 优先（一次批量取齐），失败静默降级到腾讯/新浪 + 网页同花顺。
	// 关键优化（任务 1）：官方命中时不再无条件再打一次腾讯批量；只有官方缺失的
	// 代码、或真的缺名称时才走兜底源，避免每次刷新固定两个 RTT。
	official, missing := s.fuyaoSnapshotWithFallback(ctx, parsed)
	var fallback map[string]contract.Quote
	if len(missing) > 0 {
		fallback = s.batchFallbackQuotes(ctx, missing)
	}
	// 只有走官方降级路径的代码才逐只抓同花顺；官方已命中的不重复抓。
	useTHSPerCode := len(missing) <= thsPerCodeLimit
	out := make([]contract.Quote, 0, len(parsed))
	for _, code := range parsed {
		if quote, ok := official[code.ID()]; ok {
			// 官方快照不带名称：fuyaoSnapshotWithFallback 已用本地缓存/一次批量补齐；
			// 仍为空时保留空名字（不阻断价格展示），下次列表会再补。
			quote = normalizeQuoteState(quote)
			s.cache.put("quote:"+code.ID(), quote, ttlQuote)
			if s.shouldPersistQuote(code, quote) {
				s.saveCachedQuote(code, quote)
			}
			out = append(out, quote)
			continue
		}
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
	// 落盘节流：自选面板 5 秒轮询时不再每轮写 50 次 sqlite。
	if s.shouldPersistQuote(code, quote) {
		s.saveCachedQuote(code, quote)
	}
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
// 复权口径（2026-11-12 真实网络复核，腾讯 fqkline 的 qfq 与 hfq 两套键都可用）：
//   - qfq：腾讯 fqkline qfq 单源；失败即返回 ErrAdjustUnsupported（由 plugins/kline 降级为不复权）。
//   - hfq：腾讯 fqkline hfq 单源（键 hfqday/hfqweek/hfqmonth）。
//     实测 param=sh600000,day,,,30,hfq 返回 data.sh600000.hfqday，价格高于原始序列（后复权口径），
//     所以「后复权只有东财能给」这个前提已经不成立，不需要也不允许拿前复权冒充。
//   - none：腾讯原始序列 → 新浪日线 → 同花顺兜底。
//     老顺序是「同花顺优先」，但实测 THS v6 line 返回的**就是前复权序列**（除权日之前的价格
//     已下调，与腾讯 qfq 一致，与腾讯原始价差约 28 元/股），拿它当不复权会让研判/回测
//     建立在已调整过的价格上。THS 现在只作最后兜底（覆盖指数/周/月且最稳），
//     走到它时口径已降级，上层需要按 warning 如实说明。
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

	// 官方 API 优先：日/周/月都支持，且 none/forward/backward 三档复权都可信。
	// 分钟线官方不提供（ErrPeriodUnsupported），直接落到原有链路。
	if bars, err := s.fuyaoBars(ctx, parsed, p, limit, adj); err == nil {
		s.cache.put(cacheKey, bars, ttlBars)
		return lastBars(bars, limit), nil
	} else if !errors.Is(err, ErrPeriodUnsupported) && !errors.Is(err, ErrNoData) {
		s.log.Debug("官方 K 线失败，降级到现有链路", "code", parsed.Raw, "period", p, "adjust", adj, "error", err)
	}

	switch adj {
	case contract.AdjustQfq:
		bars, txErr := s.tencentBarsQfq(ctx, parsed, p, limit)
		if txErr == nil {
			s.cache.put(cacheKey, bars, ttlBars)
			return lastBars(bars, limit), nil
		}
		return nil, fmt.Errorf("%w（前复权走腾讯 fqkline，本次失败：%v）；请改用不复权", ErrAdjustUnsupported, txErr)
	case contract.AdjustHfq:
		// 后复权同样由腾讯 fqkline 提供（键 hfqday/hfqweek/hfqmonth），不再依赖东财。
		bars, txErr := s.tencentBarsHfq(ctx, parsed, p, limit)
		if txErr == nil {
			s.cache.put(cacheKey, bars, ttlBars)
			return lastBars(bars, limit), nil
		}
		return nil, fmt.Errorf("%w（后复权走腾讯 fqkline hfq，本次失败：%v）；请改用前复权或不复权", ErrAdjustUnsupported, txErr)
	}

	var (
		bars   []contract.Bar
		srcErr []string
	)
	// 不复权顺序：腾讯原始序列 → 新浪日线 → 同花顺兜底。
	//
	// 老顺序是「同花顺优先」，但实测同花顺 v6 line 返回的是**前复权**序列（证据见
	// tencent_kline.go 顶部），拿它当「不复权」会让技术研判/回测建立在已经除权调整过的价格上。
	// 所以顺序改成先取真正的不复权源；新浪只覆盖日线，放在腾讯之后。
	// 同花顺保留为最后兜底（它覆盖指数/周/月且最稳），但只有在前面全部失败时才会用到，
	// 那时口径是降级事实而不是承诺 —— 本层没有字段能标注，上层（plugins/kline）需按 warning 说明。
	if bars, err = s.tencentBarsRaw(ctx, parsed, p, limit); err == nil {
		s.cache.put(cacheKey, bars, ttlBars)
		return lastBars(bars, limit), nil
	} else {
		srcErr = append(srcErr, "腾讯原始: "+err.Error())
	}
	if symbol := parsed.MarketSymbol(); symbol != "" && p == contract.PeriodDay {
		if bars, err = s.sinaBars(ctx, symbol, limit); err == nil {
			s.cache.put(cacheKey, bars, ttlBars)
			return lastBars(bars, limit), nil
		}
		srcErr = append(srcErr, "新浪: "+err.Error())
	}
	if symbol := parsed.THSSymbol(); symbol != "" {
		bars, err = s.thsBars(ctx, parsed, p, limit)
		if err == nil {
			s.cache.put(cacheKey, bars, ttlBars)
			return lastBars(bars, limit), nil
		}
		srcErr = append(srcErr, "同花顺: "+err.Error())
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
		// last.js 固定回最近 140 根；调用方要多少给多少，多的截掉。
		return lastBars(data.Bars, limit), nil
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

// StockFundFlow 返回个股最新资金流，来源是新浪 MoneyFlow.ssi_ssfx_flzjtj（唯一来源）。
//
// 口径说明（与旧东财口径的差异，前端/Agent 需要知道）：
//   - SuperNet/LargeNet/MediumNet/SmallNet = 新浪 r0/r1/r2/r3 的 (in - out)，即「超大/大/中/小单净额」，
//     与东财同名口径一致（都是净额，单位元）；
//   - MainNet = r0_net + r1_net（超大单 + 大单），与东财「主力净额 = 大单 + 超大单」同义；
//   - 新浪没有「主力净占比」字段，MainPct 用 MainNet / |netamount 当量| 计算，
//     分母是新浪的「全部资金净额」，与东财 f184 的分母（成交额）不同，只能作强弱参考。
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

// FundFlowRank 返回板块或概念的资金流排名（主力净流入降序），等价于 FundFlowRankBy(..., "in", ...)。
// kind 为空时默认 sector（product 要求空参数不报错）；limit<=0 默认 10。
func (s *Service) FundFlowRank(ctx context.Context, kind string, limit int) ([]contract.FundFlow, error) {
	return s.FundFlowRankBy(ctx, kind, "in", limit)
}

// FundFlowRankBy 返回板块或概念的资金榜：direction=in 为净流入降序，
// out 为净流出（净额升序，最负在前），all 兼容旧行为（净额降序）。
// kind 为空默认 sector，direction 为空默认 in，limit<=0 默认 10，上限 100。
//
// 数据源固定为新浪 MoneyFlow.ssl_bkzj_bk（fenlei=0 行业 / 1 概念）。
// 口径差异（重要）：MainNet 是新浪的「全部资金净额 netamount」（inamount - outamount），
// 不是旧东财口径的「主力净额（大单 + 超大单）」。新浪这个榜单没有主力/超大单拆分，
// 因此 SuperNet/LargeNet 等字段留 0；前端与 Agent 需要把榜单读成「资金净额榜」而不是「主力榜」。
// MainPct 来自 ratioamount（净额 / 成交额），ChangePct 来自 avg_changeratio，两者都乘 100 转成百分数。
func (s *Service) FundFlowRankBy(ctx context.Context, kind, direction string, limit int) ([]contract.FundFlow, error) {
	normalized, err := normalizeSectorKind(kind)
	if err != nil {
		return nil, err
	}
	dir, err := normalizeRankDirection(direction)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = defaultRankLimit
	}
	if limit > maxRankLimit {
		limit = maxRankLimit
	}
	cacheKey := fmt.Sprintf("flow:rank:%s:%s:%d", normalized, dir, limit)
	if cached, ok := s.cache.get(cacheKey); ok {
		if items, isItems := cached.([]contract.FundFlow); isItems {
			return items, nil
		}
	}
	out, err := s.rankFromSina(ctx, normalized, dir, limit)
	if err != nil {
		return nil, fmt.Errorf("取板块资金流排名失败: %w", err)
	}
	s.cache.put(cacheKey, out, ttlFlow)
	return out, nil
}

// normalizeRankDirection 归一资金榜方向：空串默认 in，只接受 in/out/all。
func normalizeRankDirection(direction string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "", "in":
		return "in", nil
	case "out":
		return "out", nil
	case "all":
		return "all", nil
	default:
		return "", fmt.Errorf("datasource: direction 只支持 in/out/all，实际 %q", direction)
	}
}

// Sectors 返回板块或概念列表（含涨跌幅与净流入）。kind 为空时默认 sector。
//
// 数据源是新浪 Market_Center.getHQNodes 的节点树：
//   - 行业板块取「新浪行业」这一组（new_xxxx 形如 new_dzxx 电子行业）；
//   - 概念板块取「概念板块」这一组（gn_xxxx 形如 gn_hwqc 华为汽车）。
//
// 返回的 Code 就是新浪 category —— 这是本应用唯一认可的板块代码形态，
// 可以直接传给 FundFlowRankBy / SectorFundFlowHistory / SectorFundFlowBatch。
// 旧东财 BK 代码（BK1201）已经不受支持，调用那些接口传 BK 代码会得到可操作的中文错误。
// 涨跌幅与净流入不在目录接口里，需要实时数值时交给 FundFlowRankBy（同一套新浪口径）。
func (s *Service) Sectors(ctx context.Context, kind string) ([]contract.Sector, error) {
	normalized, err := normalizeSectorKind(kind)
	if err != nil {
		return nil, err
	}
	cacheKey := "sectors:" + string(normalized)
	if cached, ok := s.cache.get(cacheKey); ok {
		if items, isItems := cached.([]contract.Sector); isItems {
			return items, nil
		}
	}
	entries, err := s.sinaSectorCatalog(ctx)
	if err != nil {
		return nil, err
	}
	rows := entries[normalized]
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: 新浪板块目录里没有 %s 分类", ErrNoData, normalized)
	}
	out := make([]contract.Sector, 0, len(rows))
	for _, row := range rows {
		out = append(out, contract.Sector{
			Code: row.Code,
			Name: row.Name,
			Kind: string(normalized),
		})
	}
	s.cache.put(cacheKey, out, ttlSinaFlow*5)
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

// Search 按关键词搜代码：腾讯 smartbox（唯一来源）。
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

	body, err := s.client.get(ctx, request{source: sourceTencent, url: txSuggestURL(text)})
	if err != nil {
		return nil, err
	}
	suggestions := parseTxSuggest(body)

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
	// 官方 API 可能「已配置但还没被调用过」：用配置状态覆盖“未使用”，
	// 让设置页一眼看出 Key 是否已生效（调用失败时仍保留 client 记录的错误）。
	if note, ok := out[sourceFuyao]; ok && (note == "未使用" || note == "") {
		out[sourceFuyao] = s.fuyaoHealth()
	}
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
