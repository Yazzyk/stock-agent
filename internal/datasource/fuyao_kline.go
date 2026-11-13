package datasource

// 官方历史 K 线（A 股 / 指数 / 场内基金）与除复权。
//
// 官方原生批量接口单次最多 10 个代码，因此这里按 10/批切分，复权与周期整批共用；
// A 股批量支持 1d/1w/1mo，指数与基金批量当前只支持 1d（传其它周期由官方返回 1003，
// 调用方应回退到单标的接口或本地聚合）。
//
// 单位：volume=股、turnover=元（与官方一致；数据层内部的 contract.Bar 才折算成手）。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	// fuyaoKlineChunk 是官方批量 K 线单次代码上限。
	fuyaoKlineChunk = 10
	// fuyaoKlineConcurrency 是批量 K 线并发上限。
	fuyaoKlineConcurrency = 4
	// fuyaoKlineDefaultLimit 是默认返回根数。
	fuyaoKlineDefaultLimit = 250
	// fuyaoKlineMaxLimit 是单次返回根数硬上限（约 8 年日线）。
	fuyaoKlineMaxLimit = 2000
)

// fuyaoBarGroup 是批量 K 线响应里的一只标的（官方结构：{thscode,timestamp,item:[]}）。
type fuyaoBarGroup struct {
	Thscode   string         `json:"thscode"`
	Timestamp int64          `json:"timestamp"`
	Item      []fuyaoBarItem `json:"item"`
}

// fuyaoBarBatchData 是批量 K 线的数据体。
type fuyaoBarBatchData struct {
	Interval string          `json:"interval"`
	Adjust   string          `json:"adjust"`
	Item     []fuyaoBarGroup `json:"item"`
}

// fuyaoKlineEndpoint 返回资产类型对应的批量历史 K 线端点与目录上限。
func fuyaoKlineEndpoint(asset string) (path string, intervalOK map[string]bool, err error) {
	switch strings.ToLower(strings.TrimSpace(asset)) {
	case "", "stock", "a-share", "ashare":
		return "/api/a-share/prices/historical-batch", map[string]bool{"1d": true, "1w": true, "1mo": true}, nil
	case "index":
		return "/api/a-share-index/prices/historical-batch", map[string]bool{"1d": true}, nil
	case "fund", "etf", "lof":
		return "/api/fund/market/historical-batch", map[string]bool{"1d": true}, nil
	}
	return "", nil, fmt.Errorf("%w: 未知资产类型 %q（可用 stock/index/fund）", contract.ErrFuyaoUnsupported, asset)
}

// FuyaoKline 批量取官方历史 K 线。
//
// 参数：
//   - codes：股票/指数/基金代码（支持 600519、sh600519、600519.SH 等写法）；
//   - interval：1d / 1w / 1mo（指数与基金只支持 1d）；
//   - start/end：时间窗口；为零时按 limit 估算（日线约 1.6×limit 自然日）；
//   - adjust：forward/none/backward（默认 forward）；
//   - asset：stock（默认）/index/fund。
//
// 返回按代码顺序排列；某个代码无数据时保留空 Bars（与官方批量语义一致）。
func (s *Service) FuyaoKline(ctx context.Context, codes []string, interval string, start, end time.Time, adjust, asset string, limit int) ([]contract.FuyaoBarsSeries, error) {
	path, intervalOK, err := fuyaoKlineEndpoint(asset)
	if err != nil {
		return nil, err
	}
	interval = strings.TrimSpace(interval)
	if interval == "" {
		interval = "1d"
	}
	if !intervalOK[interval] {
		return nil, fmt.Errorf("%w: %s 资产当前只支持 1d K 线", contract.ErrFuyaoUnsupported, strings.TrimSpace(asset))
	}
	adjust = strings.ToLower(strings.TrimSpace(adjust))
	switch adjust {
	case "":
		adjust = "forward"
	case "qfq":
		adjust = "forward"
	case "hfq":
		adjust = "backward"
	case "forward", "backward", "none":
	default:
		return nil, fmt.Errorf("datasource: adjust 只支持 forward/none/backward，实际 %q", adjust)
	}
	if limit <= 0 {
		limit = fuyaoKlineDefaultLimit
	}
	if limit > fuyaoKlineMaxLimit {
		limit = fuyaoKlineMaxLimit
	}
	if end.IsZero() {
		// 默认窗口按「今天 23:59:59」对齐，保证同一交易日内多次调用缓存键稳定。
		now := s.now().In(cstZone())
		end = time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, cstZone())
	}
	if start.IsZero() {
		start = end.AddDate(0, 0, -limit*2-10)
	}
	if !end.After(start) {
		return nil, fmt.Errorf("datasource: end 必须晚于 start")
	}

	thsCodes := make([]string, 0, len(codes))
	byThs := make(map[string]bool, len(codes))
	for _, raw := range codes {
		code, err := parseFuyaoCode(raw)
		ths := ""
		if err == nil {
			ths = code.THSCode()
		}
		// 同花顺指数用 .TI 后缀，ParseCode 不识别；指数资产允许直接透传官方写法。
		if ths == "" && strings.EqualFold(strings.TrimSpace(asset), "index") {
			upper := strings.ToUpper(strings.TrimSpace(raw))
			if len(strings.Split(upper, ".")) == 2 && len(upper) >= 9 {
				ths = upper
				err = nil
			}
		}
		if err != nil {
			return nil, err
		}
		if ths == "" {
			return nil, fmt.Errorf("%w: %s 无法映射成官方 thscode", contract.ErrFuyaoUnsupported, raw)
		}
		if !byThs[ths] {
			byThs[ths] = true
			thsCodes = append(thsCodes, ths)
		}
	}
	if len(thsCodes) == 0 {
		return nil, fmt.Errorf("%w: 没有可查询的代码", ErrNoData)
	}

	// 缓存键：端点 + 周期 + 复权 + 时间窗 + 代码集合（排序后），避免不同面板互相污染。
	sortedCodes := append([]string(nil), thsCodes...)
	sort.Strings(sortedCodes)
	cacheKey := fmt.Sprintf("fuyao:kline:%s:%s:%s:%d:%d:%s",
		path, interval, adjust, start.UnixMilli(), end.UnixMilli(), strings.Join(sortedCodes, ","))
	if cached, ok := s.cache.get(cacheKey); ok {
		if series, isSeries := cached.([]contract.FuyaoBarsSeries); isSeries {
			return trimSeries(series, limit), nil
		}
	}

	type chunkResult struct {
		groups []fuyaoBarGroup
		err    error
	}
	var chunks [][]string
	for i := 0; i < len(thsCodes); i += fuyaoKlineChunk {
		j := i + fuyaoKlineChunk
		if j > len(thsCodes) {
			j = len(thsCodes)
		}
		chunks = append(chunks, thsCodes[i:j])
	}
	results := make([]chunkResult, len(chunks))
	sem := make(chan struct{}, fuyaoKlineConcurrency)
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk []string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var data fuyaoBarBatchData
			query := map[string]string{
				"thscodes": strings.Join(chunk, ","),
				"interval": interval,
				"start":    fmt.Sprintf("%d", start.UnixMilli()),
				"end":      fmt.Sprintf("%d", end.UnixMilli()),
				"adjust":   adjust,
			}
			err := s.fuyaoGetWith(ctx, path, query, &data)
			results[i] = chunkResult{groups: data.Item, err: err}
		}(i, chunk)
	}
	wg.Wait()

	byThsSeries := make(map[string]contract.FuyaoBarsSeries, len(thsCodes))
	var firstErr error
	for _, result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		for _, group := range result.groups {
			bars := make([]contract.FuyaoBarRow, 0, len(group.Item))
			for _, item := range group.Item {
				if item.DateMS <= 0 || item.Close <= 0 {
					continue
				}
				bars = append(bars, contract.FuyaoBarRow{
					DateMS: item.DateMS,
					Date:   time.UnixMilli(item.DateMS).In(cstZone()).Format("2006-01-02"),
					Open:   item.Open, High: item.High, Low: item.Low, Close: item.Close,
					Volume: item.Volume, Turn: item.Turn,
				})
			}
			byThsSeries[strings.ToUpper(group.Thscode)] = contract.FuyaoBarsSeries{
				Thscode: group.Thscode, Interval: interval, Adjust: adjust,
				Timestamp: group.Timestamp, Bars: bars,
			}
		}
	}
	if len(byThsSeries) == 0 && firstErr != nil {
		return nil, firstErr
	}
	out := make([]contract.FuyaoBarsSeries, 0, len(thsCodes))
	for _, ths := range thsCodes {
		series, ok := byThsSeries[strings.ToUpper(ths)]
		if !ok {
			series = contract.FuyaoBarsSeries{Thscode: ths, Interval: interval, Adjust: adjust}
		}
		out = append(out, series)
	}
	s.cache.put(cacheKey, out, ttlFuyaoBars)
	return trimSeries(out, limit), nil
}

// trimSeries 把每条序列裁剪到最近 limit 根（官方按升序返回）。
func trimSeries(series []contract.FuyaoBarsSeries, limit int) []contract.FuyaoBarsSeries {
	if limit <= 0 {
		return series
	}
	out := make([]contract.FuyaoBarsSeries, len(series))
	for i, item := range series {
		out[i] = item
		if len(item.Bars) > limit {
			out[i].Bars = item.Bars[len(item.Bars)-limit:]
		}
	}
	return out
}

// fuyaoGetWith 是给「参数是 map[string]string」场景的便捷封装（内部仍走 fuyaoGet）。
func (s *Service) fuyaoGetWith(ctx context.Context, path string, params map[string]string, out any) error {
	values := make(mapToValues, len(params))
	for k, v := range params {
		values[k] = []string{v}
	}
	return s.fuyaoGet(ctx, path, values, out)
}

// mapToValues 是 url.Values 的别名，避免在调用点反复 import net/url。
type mapToValues = map[string][]string

// FuyaoAdjustFactors 取单只 A 股的复权因子事件流（分红/送股/配股）。
// 字段：ex_date_ms=除权日毫秒戳；dividend_per_share=每股分红（元）；
// per_share_bonus=每股送转（股）。需要复权后价格时优先用 K 线的 adjust 参数。
func (s *Service) FuyaoAdjustFactors(ctx context.Context, code string) ([]map[string]any, error) {
	parsed, err := parseFuyaoCode(code)
	if err != nil {
		return nil, err
	}
	ths := parsed.THSCode()
	if ths == "" {
		return nil, fmt.Errorf("%w: %s 无法映射成官方 thscode", contract.ErrFuyaoUnsupported, code)
	}
	data, err := s.fuyaoQueryChecked(ctx, contract.FuyaoQuery{
		Endpoint: "a-share.corporate-actions.adjustment-factors",
		Params:   map[string]string{"thscode": ths},
		Limit:    200,
	})
	if err != nil {
		return nil, err
	}
	return data.Items, nil
}
