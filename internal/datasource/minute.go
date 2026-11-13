package datasource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 当日分时（分钟线）链路与口径。
//
// 主链路：腾讯 web.ifzq.gtimg.cn/appstock/app/minute/query。
// 行格式是「HHMM 价格 累计成交量(手) 累计成交额(元)」，必须做差分才能得到分钟量，
// 均价用「累计成交额 / (累计成交量 × 100)」还原；响应还会带 15:00 之后的盘后数据，需要截断。
//
// 降级链路：新浪 1 分钟 K 线（CN_MarketData.getKLineData?scale=1）。
// 它比腾讯更稳（腾讯会对非浏览器来源返回 WAF 501），但只回最近 N 根、可能跨到前一交易日，
// 所以只保留最后一根所在日期。
//
// 缓存 60s：盘中分时一分钟才变一次，30s 级轮询没必要重复打上游。
const (
	ttlMinute       = 60 * time.Second
	txMinuteURLBase = "https://web.ifzq.gtimg.cn/appstock/app/minute/query?code="
	txMinuteReferer = "https://gu.qq.com/"
	// 腾讯 minute 行字段下标：时间、价格、累计成交量、累计成交额。
	txMinuteIdxTime   = 0
	txMinuteIdxPrice  = 1
	txMinuteIdxVolCum = 2
	txMinuteIdxAmtCum = 3
	txMinuteMinFields = 4
	// 腾讯 qt 快照下标（与 tencent.go 的 txQuote 一致）。
	txMinuteQtName      = 1
	txMinuteQtPrevClose = 4
	txMinuteQtTime      = 30
)

// minuteService 是本包对分时扩展的自我约束；消费方（plugins/kline）按同样的结构断言取用，
// 不扩大 contract.DataService，避免所有 stub / 测试桩连锁改动。
type minuteService interface {
	Minute(ctx context.Context, code string) (contract.MinuteSeries, error)
}

var _ minuteService = (*Service)(nil)

// Minute 返回个股/指数当日分时序列。
// 链路：内存缓存（60s）→ 腾讯 minute（主）→ 新浪 1 分钟 K 线（兜底）；
// 两条都失败时返回可操作中文错误。
func (s *Service) Minute(ctx context.Context, code string) (contract.MinuteSeries, error) {
	parsed, err := ParseCode(code)
	if err != nil {
		return contract.MinuteSeries{}, err
	}
	if parsed.Kind == KindSector || parsed.Kind == KindConcept {
		return contract.MinuteSeries{}, fmt.Errorf("%w: %s 是板块/概念，分时线只支持个股与指数", ErrNoData, parsed.Raw)
	}
	symbol := parsed.MarketSymbol()
	if symbol == "" {
		return contract.MinuteSeries{}, fmt.Errorf("%w: %s 没有行情代码，取不到分时走势", ErrNoData, parsed.Raw)
	}
	cacheKey := "minute:" + parsed.ID()
	if cached, ok := s.cache.get(cacheKey); ok {
		if series, isSeries := cached.(contract.MinuteSeries); isSeries && len(series.Points) > 0 {
			return series, nil
		}
	}

	failures := make([]string, 0, 2)
	series, txErr := s.tencentMinute(ctx, parsed, symbol)
	if txErr == nil {
		s.cache.put(cacheKey, series, ttlMinute)
		return series, nil
	}
	failures = append(failures, "腾讯: "+txErr.Error())
	series, sinaErr := s.sinaMinute(ctx, parsed, symbol)
	if sinaErr == nil {
		s.cache.put(cacheKey, series, ttlMinute)
		return series, nil
	}
	failures = append(failures, "新浪: "+sinaErr.Error())
	return contract.MinuteSeries{}, fmt.Errorf(
		"%w: %s 当日没有分时数据（可能停牌、未开盘或代码有误；%s）；可在交易时段重试",
		ErrNoData, parsed.Raw, strings.Join(failures, "；"),
	)
}

// tencentMinute 走腾讯 minute 端点兜底；成交量/额做差分还原成分钟口径。
func (s *Service) tencentMinute(ctx context.Context, code Code, symbol string) (contract.MinuteSeries, error) {
	body, err := s.client.get(ctx, request{
		source:  sourceTencent,
		url:     txMinuteURLBase + url.QueryEscape(symbol),
		referer: txMinuteReferer,
	})
	if err != nil {
		return contract.MinuteSeries{}, err
	}
	data, err := parseTencentMinute(body, symbol)
	if err != nil {
		return contract.MinuteSeries{}, err
	}
	name := data.Name
	if name == "" {
		name = s.CachedName(code)
	}
	return contract.MinuteSeries{
		Code:      code.Symbol,
		Name:      name,
		Date:      data.Date,
		PrevClose: data.PrevClose,
		Points:    data.Points,
		Source:    sourceTencent,
	}, nil
}

// sinaMinute 走新浪 1 分钟 K 线兜底：CN_MarketData.getKLineData?scale=1。
// 返回的是最近 N 根分钟线，可能跨到前一交易日；这里只保留最后一根所在日期。
// 新浪 volume 是股数、amount 是元，这里把 volume 换成手，并用累计额/累计股数算均价。
func (s *Service) sinaMinute(ctx context.Context, code Code, symbol string) (contract.MinuteSeries, error) {
	body, err := s.client.get(ctx, request{
		source:  sourceSina,
		url:     sinaMinuteURL(symbol),
		referer: "https://finance.sina.com.cn",
	})
	if err != nil {
		return contract.MinuteSeries{}, err
	}
	points, date, err := parseSinaMinute(body)
	if err != nil {
		return contract.MinuteSeries{}, err
	}
	prevClose := 0.0
	// 昨收用现有多源快照补；失败时退回首根开盘价（仅影响基线，不影响价格曲线）。
	if quotes, qerr := s.Quote(ctx, []string{code.Symbol}); qerr == nil {
		for _, q := range quotes {
			if q.Code == code.Symbol && q.PrevClose > 0 {
				prevClose = q.PrevClose
				break
			}
		}
	}
	if prevClose <= 0 {
		prevClose = points[0].Price
	}
	return contract.MinuteSeries{
		Code:      code.Symbol,
		Name:      s.CachedName(code),
		Date:      date,
		PrevClose: prevClose,
		Points:    points,
		Source:    sourceSina,
	}, nil
}

// sinaMinuteURL 组装新浪 1 分钟 K 线地址；250 根覆盖完整交易日并留出跨日余量。
func sinaMinuteURL(symbol string) string {
	return "https://quotes.sina.cn/cn/api/json_v2.php/CN_MarketData.getKLineData?symbol=" +
		url.QueryEscape(symbol) + "&scale=1&ma=no&datalen=250"
}

// parseSinaMinute 解析新浪分钟线并只保留最后一个交易日。
func parseSinaMinute(body []byte) ([]contract.MinutePoint, string, error) {
	var rows []struct {
		Day    string `json:"day"`
		Close  string `json:"close"`
		Volume string `json:"volume"`
		Amount string `json:"amount"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, "", fmt.Errorf("datasource: 新浪分时格式异常: %w", err)
	}
	if len(rows) == 0 {
		return nil, "", ErrEmptyResponse
	}
	last := rows[len(rows)-1].Day
	date := strings.SplitN(strings.TrimSpace(last), " ", 2)[0]
	points := make([]contract.MinutePoint, 0, len(rows))
	var cumShares, cumAmount float64
	for _, row := range rows {
		parts := strings.SplitN(strings.TrimSpace(row.Day), " ", 2)
		if len(parts) != 2 || parts[0] != date {
			continue
		}
		clock := parts[1]
		if len(clock) >= 5 {
			clock = clock[:5]
		}
		price := parseFloatString(row.Close)
		shares := parseFloatString(row.Volume)
		amount := parseFloatString(row.Amount)
		if price <= 0 {
			continue
		}
		cumShares += shares
		cumAmount += amount
		avg := 0.0
		if cumShares > 0 && cumAmount > 0 {
			avg = cumAmount / cumShares
		}
		points = append(points, contract.MinutePoint{
			Time:   clock,
			Price:  price,
			Avg:    avg,
			Volume: shares / 100, // 新浪给的是股，统一换算成手
			Amount: amount,
		})
	}
	if len(points) == 0 {
		return nil, "", ErrEmptyResponse
	}
	return points, date, nil
}

// validClock 校验 HH:MM 是否落在 A 股连续竞价时段（09:30-11:30 / 13:00-15:00）。
func validClock(clock string) bool {
	if len(clock) != 5 || clock[2] != ':' {
		return false
	}
	var hour, minute int
	if _, err := fmt.Sscanf(clock, "%02d:%02d", &hour, &minute); err != nil {
		return false
	}
	total := hour*60 + minute
	return (total >= 9*60+30 && total <= 11*60+30) || (total >= 13*60 && total <= 15*60)
}

// txMinuteData 是腾讯分时的解析结果。
type txMinuteData struct {
	Date      string
	Name      string
	PrevClose float64
	Points    []contract.MinutePoint
}

// parseTencentMinute 解析腾讯 minute 响应。
// 行里的成交量/额是**累计值**，这里差分回分钟值；13:00 这根与新浪 13:01 同属午后第一分钟，
// 为了避免横轴重复槽位，只用作差分基准而不输出；15:00 之后的盘后数据直接丢弃。
func parseTencentMinute(body []byte, symbol string) (txMinuteData, error) {
	var envelope struct {
		Code int `json:"code"`
		Data map[string]struct {
			Data struct {
				Rows []string `json:"data"`
				Date string   `json:"date"`
			} `json:"data"`
			Qt map[string]json.RawMessage `json:"qt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return txMinuteData{}, fmt.Errorf("datasource: 腾讯分时格式异常: %w", err)
	}
	node, ok := envelope.Data[symbol]
	if !ok || len(node.Data.Rows) == 0 {
		return txMinuteData{}, ErrEmptyResponse
	}
	out := txMinuteData{Date: normalizeTxDate(node.Data.Date)}
	if raw, ok := node.Qt[symbol]; ok {
		var quote []any
		if json.Unmarshal(raw, &quote) == nil {
			if len(quote) > txMinuteQtName {
				out.Name = strings.TrimSpace(fmt.Sprint(quote[txMinuteQtName]))
			}
			if len(quote) > txMinuteQtPrevClose {
				out.PrevClose, _ = parseNum(fmt.Sprint(quote[txMinuteQtPrevClose]))
			}
			if out.Date == "" && len(quote) > txMinuteQtTime {
				stamp := fmt.Sprint(quote[txMinuteQtTime])
				if len(stamp) >= 8 {
					out.Date = normalizeTxDate(stamp[:8])
				}
			}
		}
	}
	out.Points = make([]contract.MinutePoint, 0, len(node.Data.Rows))
	var prevVol, prevAmt float64
	for _, row := range node.Data.Rows {
		fields := strings.Fields(row)
		if len(fields) < txMinuteMinFields {
			continue
		}
		clock, ok := txClock(fields[txMinuteIdxTime])
		if !ok {
			continue
		}
		price, ok := parseNum(fields[txMinuteIdxPrice])
		if !ok || price <= 0 {
			continue
		}
		volCum, _ := parseNum(fields[txMinuteIdxVolCum])
		amtCum, _ := parseNum(fields[txMinuteIdxAmtCum])
		// 累计值偶尔会回退（源端修正），出现时按 0 处理，避免出现负成交量柱。
		vol := volCum - prevVol
		amt := amtCum - prevAmt
		prevVol, prevAmt = volCum, amtCum
		if vol < 0 {
			vol = 0
		}
		if amt < 0 {
			amt = 0
		}
		point := contract.MinutePoint{Time: clock, Price: price, Volume: vol, Amount: amt}
		if volCum > 0 && amtCum > 0 {
			point.Avg = amtCum / (volCum * 100)
		}
		// 13:00 只作为差分基准，不输出（与新浪 13:01 对齐）；15:00 之后直接结束。
		if clock == "13:00" {
			continue
		}
		if clock > "15:00" {
			break
		}
		out.Points = append(out.Points, point)
	}
	if len(out.Points) == 0 {
		return txMinuteData{}, ErrEmptyResponse
	}
	return out, nil
}

// txClock 把腾讯的 "0930" 转成 "09:30"；只接受交易时段。
func txClock(raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if len(text) != 4 {
		return "", false
	}
	var hour, minute int
	if _, err := fmt.Sscanf(text, "%02d%02d", &hour, &minute); err != nil {
		return "", false
	}
	clock := fmt.Sprintf("%02d:%02d", hour, minute)
	return clock, validClock(clock)
}

// normalizeTxDate 把腾讯的 yyyyMMdd 转成 yyyy-MM-dd；无法识别时原样返回。
func normalizeTxDate(raw string) string {
	text := strings.TrimSpace(raw)
	if len(text) < 8 {
		return ""
	}
	date := text[:8]
	if _, err := time.Parse("20060102", date); err != nil {
		return ""
	}
	return date[:4] + "-" + date[4:6] + "-" + date[6:8]
}
