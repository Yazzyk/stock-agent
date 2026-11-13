package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 腾讯 K 线（web.ifzq.gtimg.cn/appstock/app/fqkline/get），三个口径共用同一个端点，
// 只有 param 末尾的复权后缀与响应键名不同：
//   - 前复权：param=<symbol>,<period>,,,<count>,qfq → 键 qfqday/qfqweek/qfqmonth；
//   - 后复权：param=<symbol>,<period>,,,<count>,hfq → 键 hfqday/hfqweek/hfqmonth；
//   - 不复权：param=<symbol>,<period>,,,<count>,  → 键 day/week/month。
//
// 后复权是 2026-11-12 真实网络复核出来的能力：param=sh600000,day,,,30,hfq 会返回
// data.sh600000.hfqday，同一交易日价格明显高于原始序列（600000 在 2026-11-05 原始 10.20 / hfq 137.358），
// 说明它按累计分红送转把历史价格上推，是真正的后复权序列，可以直接用于长期收益/回撤测算。
// 因此「后复权只有东财能给」这个旧前提已经不成立。
//
// 口径证据（2026-11-10 复核，600519 贵州茅台，除权日 2026-06-26，除权前的价格才应被调整）：
//
//	日期        同花顺 line   腾讯 qfq    腾讯原始
//	2026-06-24  1179.66      1179.656   1207.68
//	2026-06-25  1184.08      1184.076   1212.10
//	2026-11-10  1439.00      1439.000   1439.000  （除权之后两者相同）
//
// 结论：**同花顺 v6 line 返回的就是前复权序列**（除权日之前的价格已经下调，与腾讯 qfq 完全一致，
// 差异只剩四舍五入），与腾讯原始价相差约 28 元。所以：
//   - adj=qfq 降级到腾讯 qfq 不会造成口径突变；
//   - adj=none 绝不能再拿 THS 当首选（它给的是 qfq），必须先用腾讯原始键；
//   - THS 只作最后兜底，且调用方需要知道那时口径已经降级（见 Bars 注释）。
//
// 三个口径都来自同一个端点；无论取哪个，都绝不用 qfq 冒充 hfq（两者数值差异巨大）。
const (
	txFqKlineURLBase = "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param="
	txFqKlineReferer = "https://gu.qq.com/"
)

// 腾讯 fqkline 一行字段顺序：日期, 开, 收, 高, 低, 成交量(手)。
// 注意是「开收高低」，与同花顺的「开高低收」不同——写错会把高低点算反。
const (
	txKlineIdxDate   = 0
	txKlineIdxOpen   = 1
	txKlineIdxClose  = 2
	txKlineIdxHigh   = 3
	txKlineIdxLow    = 4
	txKlineIdxVolume = 5
	txKlineMinFields = 6
)

// txKlineMode 决定从响应里认哪一组键。
type txKlineMode int

const (
	txKlineQfqOnly    txKlineMode = iota // 只认 qfq 前缀（个股前复权）
	txKlineQfqThenRaw                    // 先 qfq 再原始（指数：无复权概念，腾讯只回原始键）
	txKlineRawOnly                       // 只认原始键（不复权）
	txKlineHfqOnly                       // 只认 hfq 前缀（个股后复权）
)

// txFqKlinePayload 是 fqkline 响应的最小形状。
type txFqKlinePayload struct {
	Code int                        `json:"code"`
	Msg  string                     `json:"msg"`
	Data map[string]txFqKlineSeries `json:"data"`
}

// txFqKlineSeries 是单个标的的序列容器：复权键带 qfq/hfq 前缀，原始键不带。
type txFqKlineSeries struct {
	QfqDay   [][]any `json:"qfqday"`
	QfqWeek  [][]any `json:"qfqweek"`
	QfqMonth [][]any `json:"qfqmonth"`
	HfqDay   [][]any `json:"hfqday"`
	HfqWeek  [][]any `json:"hfqweek"`
	HfqMonth [][]any `json:"hfqmonth"`
	Day      [][]any `json:"day"`
	Week     [][]any `json:"week"`
	Month    [][]any `json:"month"`
}

// txKlineAdjust 是腾讯 fqkline 的复权后缀（直接拼在 param 末尾）。
type txKlineAdjust string

const (
	txAdjustQfq txKlineAdjust = "qfq"
	txAdjustHfq txKlineAdjust = "hfq"
	txAdjustRaw txKlineAdjust = ""
)

// txFqKlinePeriod 把应用内周期映射成腾讯的位置参数与响应键前缀。
// 只支持日/周/月：腾讯没有分钟级 K 线序列。
func txFqKlinePeriod(p contract.Period) (param string, ok bool) {
	switch p {
	case contract.PeriodDay:
		return "day", true
	case contract.PeriodWeek:
		return "week", true
	case contract.PeriodMonth:
		return "month", true
	}
	return "", false
}

// keyPrefix 返回该复权方式在响应键里的前缀（qfq / hfq / 空）。
func (a txKlineAdjust) keyPrefix() string { return string(a) }

// txKlineURL 拼接腾讯 K 线地址：<symbol>,<period>,,,<count>[,qfq|hfq]。
// 参数是逗号分隔的位置参数，必须保留字面逗号，不能 query-escape
// （转义后腾讯会把整串当一个字段，解析不出来）。
func txKlineURL(symbol string, p contract.Period, limit int, adjust txKlineAdjust) (string, error) {
	param, ok := txFqKlinePeriod(p)
	if !ok {
		return "", fmt.Errorf("%w: 腾讯 K 线只支持日/周/月，实际 %s", ErrPeriodUnsupported, p)
	}
	suffix := ","
	if adjust != txAdjustRaw {
		suffix = "," + string(adjust)
	}
	return fmt.Sprintf("%s%s,%s,,,%d%s", txFqKlineURLBase, symbol, param, limit, suffix), nil
}

// txFqKlineURL 是前复权地址（保留给前复权调用点与用例）。
func txFqKlineURL(symbol string, p contract.Period, limit int) (string, error) {
	return txKlineURL(symbol, p, limit, txAdjustQfq)
}

// txHfqKlineURL 是后复权地址（param 末尾 hfq，响应键 hfqday/hfqweek/hfqmonth）。
func txHfqKlineURL(symbol string, p contract.Period, limit int) (string, error) {
	return txKlineURL(symbol, p, limit, txAdjustHfq)
}

// txRawKlineURL 是不复权地址（末尾留空表示不要求复权）。
func txRawKlineURL(symbol string, p contract.Period, limit int) (string, error) {
	return txKlineURL(symbol, p, limit, txAdjustRaw)
}

// parseTxFqKline 解析腾讯前复权响应（只认带 qfq 前缀的键）。
func parseTxFqKline(body []byte, symbol string, p contract.Period) ([]contract.Bar, error) {
	return parseTxKlineRows(body, symbol, p, txKlineQfqOnly)
}

// parseTxHfqKline 解析腾讯后复权响应（只认带 hfq 前缀的键）。
func parseTxHfqKline(body []byte, symbol string, p contract.Period) ([]contract.Bar, error) {
	return parseTxKlineRows(body, symbol, p, txKlineHfqOnly)
}

// parseTxTolerantKline 解析「qfq 优先、缺失再 hfq、再原始」的响应。
func parseTxTolerantKline(body []byte, symbol string, p contract.Period) ([]contract.Bar, error) {
	return parseTxKlineRows(body, symbol, p, txKlineQfqThenRaw)
}

// parseTxRawKline 解析腾讯原始（不复权）响应（只认 day/week/month 键）。
func parseTxRawKline(body []byte, symbol string, p contract.Period) ([]contract.Bar, error) {
	return parseTxKlineRows(body, symbol, p, txKlineRawOnly)
}

// parseTxKlineRows 解析 fqkline 响应，mode 决定认哪组键。
//
// txKlineQfqThenRaw 只给指数用：指数没有复权概念，腾讯对 sh000001/sz399001 只回 day/week/month，
// 此时原始序列与前复权等价；个股不走这条回退，避免把原始价当 qfq。
func parseTxKlineRows(body []byte, symbol string, p contract.Period, mode txKlineMode) ([]contract.Bar, error) {
	param, ok := txFqKlinePeriod(p)
	if !ok {
		return nil, fmt.Errorf("%w: 腾讯 K 线只支持日/周/月，实际 %s", ErrPeriodUnsupported, p)
	}
	var payload txFqKlinePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("datasource: 腾讯 K 线格式异常: %w", err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("datasource: 腾讯 K 线返回 code=%d %s", payload.Code, strings.TrimSpace(payload.Msg))
	}
	series, exists := payload.Data[symbol]
	if !exists {
		return nil, fmt.Errorf("%w: 腾讯 K 线没有 %s 的数据", ErrEmptyResponse, symbol)
	}
	rows := series.rows(param, mode)
	if len(rows) == 0 {
		return nil, ErrEmptyResponse
	}
	bars := make([]contract.Bar, 0, len(rows))
	for _, row := range rows {
		if len(row) < txKlineMinFields {
			continue
		}
		date := strings.TrimSpace(anyStr(row[txKlineIdxDate]))
		open := anyNum(row[txKlineIdxOpen])
		closePrice := anyNum(row[txKlineIdxClose])
		high := anyNum(row[txKlineIdxHigh])
		low := anyNum(row[txKlineIdxLow])
		// 腾讯成交量单位是「手」，与契约一致，不再换算。
		volume := anyNum(row[txKlineIdxVolume])
		if date == "" || open <= 0 || closePrice <= 0 || high <= 0 || low <= 0 {
			continue
		}
		// 腾讯不返回成交额：用「收盘价 × 成交量(手) × 100」估算，单位元。
		// 这是**估算值**——真实成交额按逐笔价格累计，与「收盘价×量」会有偏差，别当精确数用。
		amount := closePrice * volume * 100
		bars = append(bars, contract.Bar{
			Date: date, Open: open, High: high, Low: low, Close: closePrice,
			Volume: volume, Amount: amount,
		})
	}
	if len(bars) == 0 {
		return nil, ErrEmptyResponse
	}
	return bars, nil
}

// mode.keyPrefix 返回该模式在响应键里认的前缀。
func (m txKlineMode) keyPrefix() string {
	switch m {
	case txKlineQfqOnly:
		return string(txAdjustQfq)
	case txKlineHfqOnly:
		return string(txAdjustHfq)
	}
	return ""
}

// pickKey 按响应键名取行。
func (s txFqKlineSeries) pickKey(name string) [][]any {
	switch name {
	case "qfqday":
		return s.QfqDay
	case "qfqweek":
		return s.QfqWeek
	case "qfqmonth":
		return s.QfqMonth
	case "hfqday":
		return s.HfqDay
	case "hfqweek":
		return s.HfqWeek
	case "hfqmonth":
		return s.HfqMonth
	case "day":
		return s.Day
	case "week":
		return s.Week
	case "month":
		return s.Month
	}
	return nil
}

// rows 按 mode 取出该周期的行。
func (s txFqKlineSeries) rows(param string, mode txKlineMode) [][]any {
	if mode == txKlineRawOnly {
		return s.pickKey(param)
	}
	if out := s.pickKey(mode.keyPrefix() + param); len(out) > 0 {
		return out
	}
	if mode == txKlineQfqThenRaw {
		return s.pickKey(param)
	}
	return nil
}

// tencentBarsQfq 取腾讯前复权 K 线（qfq 口径的唯一来源）。
// 只覆盖日/周/月；指数没有复权概念，腾讯对它只回原始序列键，这里按代码类别放行。
func (s *Service) tencentBarsQfq(ctx context.Context, code Code, p contract.Period, limit int) ([]contract.Bar, error) {
	symbol := code.MarketSymbol()
	if symbol == "" {
		return nil, fmt.Errorf("%w: %s 没有腾讯行情代码", ErrNoData, code.Raw)
	}
	address, err := txFqKlineURL(symbol, p, limit)
	if err != nil {
		return nil, err
	}
	body, err := s.client.get(ctx, request{source: sourceTencent, url: address, referer: txFqKlineReferer})
	if err != nil {
		return nil, err
	}
	bars, err := parseTxFqKline(body, symbol, p)
	if err == nil {
		return bars, nil
	}
	if code.Kind == KindIndex && errors.Is(err, ErrEmptyResponse) {
		// 指数没有分红送转，原始序列与前复权等价，可以安全采信。
		if rawBars, rawErr := parseTxKlineRows(body, symbol, p, txKlineQfqThenRaw); rawErr == nil {
			return rawBars, nil
		}
	}
	return nil, err
}

// tencentBarsHfq 取腾讯后复权 K 线（param 末尾 hfq）。hfq 口径的唯一来源。
// 指数没有分红送转，后复权与前复权/原始序列等价；腾讯对指数可能只回原始键，
// 这时按指数放行原始序列（不是拿 qfq 冒充 hfq，指数三者本就相同）。
func (s *Service) tencentBarsHfq(ctx context.Context, code Code, p contract.Period, limit int) ([]contract.Bar, error) {
	symbol := code.MarketSymbol()
	if symbol == "" {
		return nil, fmt.Errorf("%w: %s 没有腾讯行情代码", ErrNoData, code.Raw)
	}
	address, err := txHfqKlineURL(symbol, p, limit)
	if err != nil {
		return nil, err
	}
	body, err := s.client.get(ctx, request{source: sourceTencent, url: address, referer: txFqKlineReferer})
	if err != nil {
		return nil, err
	}
	bars, err := parseTxHfqKline(body, symbol, p)
	if err == nil {
		return bars, nil
	}
	if code.Kind == KindIndex && errors.Is(err, ErrEmptyResponse) {
		if rawBars, rawErr := parseTxKlineRows(body, symbol, p, txKlineQfqThenRaw); rawErr == nil {
			return rawBars, nil
		}
	}
	return nil, err
}

// tencentBarsRaw 取腾讯原始（不复权）K 线：adj=none 的真不复权来源，
// 绝不能拿前复权（同花顺 v6 line 就是 qfq）冒充它。只覆盖日/周/月。
func (s *Service) tencentBarsRaw(ctx context.Context, code Code, p contract.Period, limit int) ([]contract.Bar, error) {
	symbol := code.MarketSymbol()
	if symbol == "" {
		return nil, fmt.Errorf("%w: %s 没有腾讯行情代码", ErrNoData, code.Raw)
	}
	address, err := txRawKlineURL(symbol, p, limit)
	if err != nil {
		return nil, err
	}
	body, err := s.client.get(ctx, request{source: sourceTencent, url: address, referer: txFqKlineReferer})
	if err != nil {
		return nil, err
	}
	return parseTxRawKline(body, symbol, p)
}
