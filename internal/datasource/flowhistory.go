package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 多日资金流与批量资金流的边界（契约见 docs/ITERATION-2026-11.md §3.2）：
// 多日默认 20 日、上限 120 日；批量单次 <=50 只；快照缓存 60s（盘中主力净额变化快，再长就是过期数字）。
//
// 数据源全部是新浪 MoneyFlow：
//   - 当日个股：ssi_ssfx_flzjtj（r0~r3 的 in/out）；
//   - 多日个股：ssl_qsfx_zjlrqs（按日期倒序，含 netamount/r0_net/turnover/changeratio）；
//   - 板块多日：ssl_bkzj_zjlrqs?bankuai=<category>。
//
// 批量没有对应的新浪批量端点，改成「并发逐只调当日接口」（并发上限 6）；
// 失败项按 ErrPartial 语义返回成功项 + 失败列表，与旧东财批量接口的返回形状保持一致。
const (
	defaultFlowDays = 20
	maxFlowDays     = 120
	maxFlowCodes    = 50
	ttlFlowHistory  = 60 * time.Second
	ttlFlowSnapshot = 60 * time.Second
	// flowSnapshotWorkers 是批量资金流的并发上限。新浪个股接口对高频请求会限流，
	// 6 路 + 底层 4 QPS 令牌桶实测能稳定跑完 50 只（约 3~5 秒）。
	flowSnapshotWorkers = 6
)

// FundFlowHistoryService 是可选的扩展接口：消费者（fundflow 插件）用类型断言取用，
// 不去扩大 contract.DataService，避免所有 stub 连锁改动。
var _ contract.FundFlowHistoryService = (*Service)(nil)

// sinaFlowHistoryURL 组装个股多日资金流地址（接口按日期倒序返回）。
func sinaFlowHistoryURL(symbol string, days int) string {
	if days <= 0 {
		days = defaultFlowDays
	}
	query := url.Values{}
	query.Set("page", "1")
	query.Set("num", strconv.Itoa(days))
	query.Set("sort", "opendate")
	query.Set("asc", "0")
	query.Set("daima", symbol)
	return "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_qsfx_zjlrqs?" + query.Encode()
}

// sinaStockFlowHistoryRaw 是新浪个股多日资金流的一行。
type sinaStockFlowHistoryRaw struct {
	OpenDate   string `json:"opendate"`
	Trade      string `json:"trade"`
	ChangeRate string `json:"changeratio"`
	Turnover   string `json:"turnover"`
	NetAmount  string `json:"netamount"`
	Ratio      string `json:"ratioamount"`
	R0Net      string `json:"r0_net"`
	R0Ratio    string `json:"r0_ratio"`
}

// parseSinaStockFlowHistory 解析新浪个股多日资金流（倒序 -> 升序，与旧东财链路一致）。
//
// 口径（与旧东财 daykline 的差异，必须说清）：
//   - MainNet = r0_net + 由 netamount 推出的「大单净额」不可得，所以 MainNet 直接取 r0_net（超大单净额），
//     同时把 netamount（全部资金净额）留在 MainPct 的分母语义之外不对外暴露 —— 见下方实现；
//   - 新浪只给 r0（超大单）一个拆分字段，Large/Medium/Small 三档没有逐日数据，留 0；
//   - MainPct 用 r0_ratio（超大单净额占成交额比例）乘 100；
//   - Close 取 trade，ChangePct 取 changeratio 乘 100（接口给的是小数比例）。
//
// 因此「主力净额」在新旧口径之间不是同一个数：旧口径是大单 + 超大单，新口径只有超大单。
// 这一点在上层（fundflow 插件与 Agent 提示）必须显式说明，不能当成同一条曲线对比。
func parseSinaStockFlowHistory(body []byte, code string, days int) (contract.FundFlowHistory, error) {
	payload := strings.TrimSpace(string(body))
	if payload == "" || payload == "null" || payload == "[]" {
		return contract.FundFlowHistory{}, ErrEmptyResponse
	}
	var rows []sinaStockFlowHistoryRaw
	if err := json.Unmarshal([]byte(payload), &rows); err != nil {
		return contract.FundFlowHistory{}, fmt.Errorf("datasource: 新浪多日资金流格式异常: %w", err)
	}
	points := make([]contract.FundFlowPoint, 0, len(rows))
	for _, row := range rows {
		date := strings.TrimSpace(row.OpenDate)
		if date == "" {
			continue
		}
		points = append(points, contract.FundFlowPoint{
			Date:      date,
			MainNet:   parseFloatString(row.R0Net),
			MainPct:   parseFloatString(row.R0Ratio) * 100,
			Close:     parseFloatString(row.Trade),
			ChangePct: parseFloatString(row.ChangeRate) * 100,
		})
	}
	if len(points) == 0 {
		return contract.FundFlowHistory{}, ErrEmptyResponse
	}
	// 上游倒序 -> 升序。
	for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
		points[i], points[j] = points[j], points[i]
	}
	if days > 0 && len(points) > days {
		points = points[len(points)-days:]
	}
	return contract.FundFlowHistory{
		Code:   code,
		Name:   code,
		Days:   days,
		Source: sourceSina,
		Points: points,
	}, nil
}

// FundFlowHistory 返回个股最近 days 个交易日的资金流明细（单位：元）。
// 数据源是新浪 ssl_qsfx_zjlrqs；失败就明确报错（没有第二条多日来源）。
func (s *Service) FundFlowHistory(ctx context.Context, code string, days int) (contract.FundFlowHistory, error) {
	parsed, err := ParseCode(code)
	if err != nil {
		return contract.FundFlowHistory{}, err
	}
	if days <= 0 {
		days = defaultFlowDays
	}
	if days > maxFlowDays {
		days = maxFlowDays
	}
	symbol := parsed.MarketSymbol()
	if symbol == "" {
		return contract.FundFlowHistory{}, fmt.Errorf("%w: %s 不是个股/指数代码，取不到多日资金流", ErrNoData, code)
	}
	cacheKey := fmt.Sprintf("flow:hist:%s:%d", parsed.ID(), days)
	if cached, ok := s.cache.get(cacheKey); ok {
		if history, isHistory := cached.(contract.FundFlowHistory); isHistory {
			return history, nil
		}
	}
	body, err := s.client.get(ctx, request{
		source:  sourceSina,
		url:     sinaFlowHistoryURL(symbol, days),
		referer: sinaFlowReferer,
		gbk:     true,
	})
	if err != nil {
		return contract.FundFlowHistory{}, err
	}
	history, err := parseSinaStockFlowHistory(body, parsed.Symbol, days)
	if err != nil {
		return contract.FundFlowHistory{}, err
	}
	if name := s.CachedName(parsed); name != "" {
		history.Name = name
	}
	s.cache.put(cacheKey, history, ttlFlowHistory)
	return history, nil
}

// FundFlowSnapshot 批量返回个股当日资金流：并发逐只调新浪当日接口（<=50 只，60s 缓存）。
// 部分失败返回成功项 + ErrPartial，全失败才返回 ErrNoData。
//
// 为什么不用批量端点：旧东财 ulist.np 一次能给 50 只，但新浪没有等价的批量资金流接口，
// 只能并发逐只抓。并发上限 flowSnapshotWorkers 与底层 4 QPS 令牌桶共同保证不打爆上游。
func (s *Service) FundFlowSnapshot(ctx context.Context, codes []string) ([]contract.FundFlow, error) {
	if len(codes) == 0 {
		return nil, errors.New("datasource: 需要至少一个代码")
	}
	if len(codes) > maxFlowCodes {
		return nil, fmt.Errorf("datasource: 一次最多查询 %d 个代码，实际 %d 个", maxFlowCodes, len(codes))
	}
	failures := make([]string, 0, 4)
	parsed := make([]Code, 0, len(codes))
	seen := make(map[string]bool, len(codes))
	for _, raw := range codes {
		code, err := ParseCode(raw)
		if err != nil {
			failures = append(failures, raw)
			continue
		}
		if seen[code.ID()] {
			continue
		}
		seen[code.ID()] = true
		parsed = append(parsed, code)
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoData, strings.Join(failures, ", "))
	}

	byID := make(map[string]contract.FundFlow, len(parsed))
	pending := make([]Code, 0, len(parsed))
	for _, code := range parsed {
		if cached, ok := s.cache.get(flowSnapshotKey(code)); ok {
			if flow, isFlow := cached.(contract.FundFlow); isFlow {
				byID[code.ID()] = flow
				continue
			}
		}
		pending = append(pending, code)
	}
	if len(pending) > 0 {
		var (
			mu  sync.Mutex
			wg  sync.WaitGroup
			sem = make(chan struct{}, flowSnapshotWorkers)
		)
		for _, code := range pending {
			wg.Add(1)
			go func(code Code) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				flow, err := s.StockFundFlow(ctx, code.Raw)
				if err != nil {
					s.log.Debug("批量资金流逐只失败", "code", code.Raw, "error", err)
					mu.Lock()
					failures = append(failures, code.Raw)
					mu.Unlock()
					return
				}
				flow.Kind = string(KindStock)
				s.cache.put(flowSnapshotKey(code), flow, ttlFlowSnapshot)
				mu.Lock()
				byID[code.ID()] = flow
				mu.Unlock()
			}(code)
		}
		wg.Wait()
	}

	out := make([]contract.FundFlow, 0, len(parsed))
	for _, code := range parsed {
		if flow, ok := byID[code.ID()]; ok {
			out = append(out, flow)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoData, strings.Join(failures, ", "))
	}
	if len(failures) > 0 {
		return out, fmt.Errorf("%w: %s", ErrPartial, strings.Join(failures, ", "))
	}
	return out, nil
}

func flowSnapshotKey(code Code) string { return "flow:snap:" + code.ID() }

// 板块/概念资金流的多日查询边界：天数与个股共用 20/120 口径；
// 批量一次最多 20 个板块，并发 4 路。
const (
	defaultSectorFlowDays = defaultFlowDays
	maxSectorFlowDays     = maxFlowDays
	maxSectorFlowCodes    = 20
	sectorFlowWorkers     = 4
)

// legacyBKSectorPattern 只用于识别已下线的东财板块代码（BK1201），命中时给可操作的中文错误。
var legacyBKSectorPattern = regexp.MustCompile("(?i)^(?:90\\.)?BK\\d{1,6}$")

// errLegacySectorCode 是旧东财 BK 代码的统一提示：告诉用户去 sector.list 取新浪 category。
var errLegacySectorCode = errors.New(
	"板块代码已切换为新浪口径（如 new_dzxx），请用 sector.list 选择",
)

// normalizeSectorCategory 校验并归一新浪板块标识：小写字母/数字/下划线，1~40 位，且至少含一个字母。
// 纯数字是股票代码（如 600519），不能当板块 category 打到新浪。
func normalizeSectorCategory(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("板块代码不能为空（新浪 category，如 new_dzxx）")
	}
	if legacyBKSectorPattern.MatchString(text) {
		return "", fmt.Errorf("%w: %q", errLegacySectorCode, raw)
	}
	category, err := normalizeSinaCategory(text)
	if err != nil {
		return "", err
	}
	if strings.IndexFunc(category, unicode.IsLetter) < 0 {
		return "", fmt.Errorf("板块代码 %q 不是新浪口径：需要形如 new_dzxx / gn_hwqc 的标识", raw)
	}
	return category, nil
}

// normalizeSectorDays 收敛天数：默认 20 日、上限 120 日，与个股多日资金流保持一致。
func normalizeSectorDays(days int) int {
	if days <= 0 {
		return defaultSectorFlowDays
	}
	if days > maxSectorFlowDays {
		return maxSectorFlowDays
	}
	return days
}

// SectorFundFlowHistory 返回板块/概念最近 days 个交易日的资金流明细（单位：元）。
// 数据源是新浪 ssl_bkzj_zjlrqs?bankuai=<category>；传入旧 BK 代码会返回可操作的中文错误。
func (s *Service) SectorFundFlowHistory(ctx context.Context, code string, days int) (contract.FundFlowHistory, error) {
	days = normalizeSectorDays(days)
	category, err := normalizeSectorCategory(code)
	if err != nil {
		return contract.FundFlowHistory{}, err
	}
	return s.sectorHistorySina(ctx, category, days, "")
}

// SectorFundFlowBatch 并发拉取多个板块/概念的多日资金流（并发 <=4）：
// 单个失败跳过并以 ErrPartial 标记，全部失败才返回 ErrNoData；
// 结果顺序与入参一致（去重后），非法代码计入失败列表而不是整批拒绝。
func (s *Service) SectorFundFlowBatch(ctx context.Context, codes []string, days int) ([]contract.FundFlowHistory, error) {
	if len(codes) == 0 {
		return nil, errors.New("datasource: 需要至少一个板块代码（新浪 category，如 new_dzxx）")
	}
	if len(codes) > maxSectorFlowCodes {
		return nil, fmt.Errorf("datasource: 一次最多查询 %d 个板块，实际 %d 个", maxSectorFlowCodes, len(codes))
	}
	days = normalizeSectorDays(days)
	unique := make([]string, 0, len(codes))
	failures := make([]string, 0, 4)
	seen := make(map[string]bool, len(codes))
	for _, raw := range codes {
		category, err := normalizeSectorCategory(raw)
		if err != nil {
			failures = append(failures, strings.TrimSpace(raw))
			continue
		}
		if seen[category] {
			continue
		}
		seen[category] = true
		unique = append(unique, category)
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("%w: 板块代码都不合法（%s）；%w", ErrNoData, strings.Join(failures, ", "), errLegacySectorCode)
	}

	results := make([]contract.FundFlowHistory, len(unique))
	failed := make([]bool, len(unique))
	sem := make(chan struct{}, sectorFlowWorkers)
	var wg sync.WaitGroup
	for i, category := range unique {
		wg.Add(1)
		go func(i int, category string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			history, err := s.SectorFundFlowHistory(ctx, category, days)
			if err != nil {
				s.log.Debug("板块资金流失败，跳过", "code", category, "error", err)
				failed[i] = true
				return
			}
			results[i] = history
		}(i, category)
	}
	wg.Wait()

	out := make([]contract.FundFlowHistory, 0, len(unique))
	for i := range results {
		if failed[i] {
			failures = append(failures, unique[i])
			continue
		}
		out = append(out, results[i])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s 都没取到板块资金流，请检查代码或稍后重试", ErrNoData, strings.Join(failures, ", "))
	}
	if len(failures) > 0 {
		return out, fmt.Errorf("%w: %s", ErrPartial, strings.Join(failures, ", "))
	}
	return out, nil
}
