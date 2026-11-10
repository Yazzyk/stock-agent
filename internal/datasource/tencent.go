package datasource

import (
	"strconv"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 腾讯行情字段下标（qt.gtimg.cn，A 股/指数同构，已用 600000 与 sh000001 实测核对）：
//
//	1  名称          3  现价         4  昨收        5  今开
//	6  成交量(手)     30 时间(yyyyMMddHHmmss)        31 涨跌额
//	32 涨跌幅%       33 最高         34 最低        35 现价/成交量(手)/成交额(元)
//	36 成交量(手)    37 成交额(万元)  38 换手率%     43 振幅%
//
// 关键单位事实：腾讯的成交量**已经是手**（600000 → 477827，新浪同刻给 47782652 股），
// 所以腾讯快照不能再除 100，否则会把成交量缩小两个数量级。
const (
	txIdxName      = 1
	txIdxPrice     = 3
	txIdxPrevClose = 4
	txIdxOpen      = 5
	txIdxVolume    = 6
	txIdxTime      = 30
	txIdxChange    = 31
	txIdxChangePct = 32
	txIdxHigh      = 33
	txIdxLow       = 34
	txIdxTriple    = 35
	txIdxAmountWan = 37
	txIdxTurnover  = 38
	txIdxAmplitude = 43
)

// txQuote 是一次腾讯快照的解析结果。
type txQuote struct {
	Symbol     string // sh600000
	Code       string
	Name       string
	Price      float64
	PrevClose  float64
	Open       float64
	High       float64
	Low        float64
	Change     float64
	ChangePct  float64
	VolumeHand float64 // 手
	Amount     float64 // 元
	Turnover   float64
	Amplitude  float64
	Time       string // RFC3339
}

// parseTxQuotes 解析 qt.gtimg.cn 的响应。返回键为 market symbol（sh600000）。
func parseTxQuotes(body []byte) map[string]txQuote {
	out := map[string]txQuote{}
	text := string(body)
	for _, chunk := range strings.Split(text, ";") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		eq := strings.Index(chunk, "=")
		if eq <= 0 {
			continue
		}
		head := strings.TrimSpace(chunk[:eq])
		head = strings.TrimPrefix(head, "v_")
		payload := strings.Trim(strings.TrimSpace(chunk[eq+1:]), "\"")
		if payload == "" || payload == "1" {
			continue
		}
		fields := strings.Split(payload, "~")
		if len(fields) <= txIdxLow {
			continue
		}
		quote := txQuote{
			Symbol:    strings.ToLower(head),
			Code:      strings.TrimSpace(fields[2]),
			Name:      strings.TrimSpace(fields[txIdxName]),
			Price:     numOrZero(fields[txIdxPrice]),
			PrevClose: numOrZero(fields[txIdxPrevClose]),
			Open:      numOrZero(fields[txIdxOpen]),
			Change:    numOrZero(fields[txIdxChange]),
			ChangePct: numOrZero(fields[txIdxChangePct]),
			High:      numOrZero(fields[txIdxHigh]),
			Low:       numOrZero(fields[txIdxLow]),
		}
		if v, ok := parseNum(fields[txIdxVolume]); ok {
			quote.VolumeHand = v // 已经是手
		}
		if len(fields) > txIdxAmountWan {
			if wan, ok := parseNum(fields[txIdxAmountWan]); ok {
				quote.Amount = wan * 10000
			}
		}
		// 35 字段含精确成交额（元），有就用它覆盖万元近似值
		if len(fields) > txIdxTriple {
			if parts := strings.Split(fields[txIdxTriple], "/"); len(parts) >= 3 {
				if amount, ok := parseNum(parts[2]); ok {
					quote.Amount = amount
				}
			}
		}
		if len(fields) > txIdxTurnover {
			quote.Turnover = numOrZero(fields[txIdxTurnover])
		}
		if len(fields) > txIdxAmplitude {
			quote.Amplitude = numOrZero(fields[txIdxAmplitude])
		}
		if len(fields) > txIdxTime {
			quote.Time = txStamp(fields[txIdxTime])
		}
		if quote.Name == "" {
			continue
		}
		out[quote.Symbol] = quote
	}
	return out
}

// txStamp 解析腾讯的 20261110161459 时间戳。
func txStamp(raw string) string {
	text := strings.TrimSpace(raw)
	if len(text) < 14 {
		return ""
	}
	return cstStamp(text[0:4] + "-" + text[4:6] + "-" + text[6:8] + " " +
		text[8:10] + ":" + text[10:12] + ":" + text[12:14])
}

// txQuoteURL 拼接批量快照地址。
func txQuoteURL(symbols []string) string {
	return "https://qt.gtimg.cn/q=" + strings.Join(symbols, ",")
}

// parseTxSuggest 解析腾讯 smartbox 搜索建议：v_hint="sh~600000~浦发银行~pfyh~GP-A^sz~000001~..."。
func parseTxSuggest(body []byte) []StockSuggestion {
	out := make([]StockSuggestion, 0, 8)
	text := string(body)
	start := strings.Index(text, "\"")
	end := strings.LastIndex(text, "\"")
	if start < 0 || end <= start {
		return out
	}
	content := text[start+1 : end]
	// smartbox 的中文是 \uXXXX 转义，按 JSON 字符串反转义一次
	if unescaped, err := strconv.Unquote("\"" + content + "\""); err == nil {
		content = unescaped
	}
	for _, item := range strings.Split(content, "^") {
		fields := strings.Split(item, "~")
		if len(fields) < 3 {
			continue
		}
		market := strings.ToLower(strings.TrimSpace(fields[0]))
		code := strings.TrimSpace(fields[1])
		name := strings.TrimSpace(fields[2])
		if market == "" || code == "" || name == "" {
			continue
		}
		out = append(out, StockSuggestion{Code: code, Name: name, Market: contract.Market(market)})
	}
	return out
}

// txQuoteToContract 把腾讯快照转成契约 Quote。
func txQuoteToContract(q txQuote, market contract.Market) contract.Quote {
	return contract.Quote{
		Code:      q.Code,
		Name:      q.Name,
		Market:    market,
		Price:     q.Price,
		PrevClose: q.PrevClose,
		Open:      q.Open,
		High:      q.High,
		Low:       q.Low,
		Change:    q.Change,
		ChangePct: q.ChangePct,
		Volume:    q.VolumeHand,
		Amount:    q.Amount,
		Turnover:  q.Turnover,
		Amplitude: q.Amplitude,
		Time:      q.Time,
		Source:    sourceTencent,
	}
}
