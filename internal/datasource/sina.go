package datasource

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 新浪 hq.sinajs.cn 字段下标（个股与指数同为：名称,今开,昨收,现价,最高,最低,...）：
//
//	0 名称   1 今开   2 昨收   3 现价   4 最高   5 最低
//	8 成交量  9 成交额 30 日期   31 时间
//
// 单位陷阱（已实测）：个股第 8 位是「股」（600000 → 47,782,652），
// 指数第 8 位是「手」（sh000001 → 404,865,177，与腾讯一致），因此按 kind 分开换算。
const (
	sinaIdxName      = 0
	sinaIdxOpen      = 1
	sinaIdxPrevClose = 2
	sinaIdxPrice     = 3
	sinaIdxHigh      = 4
	sinaIdxLow       = 5
	sinaIdxVolume    = 8
	sinaIdxAmount    = 9
	sinaIdxDate      = 30
	sinaIdxTime      = 31
)

// sinaQuote 是一次新浪快照的解析结果。
type sinaQuote struct {
	Symbol     string
	Name       string
	Price      float64
	PrevClose  float64
	Open       float64
	High       float64
	Low        float64
	VolumeHand float64
	Amount     float64
	Time       string
	Valid      bool
}

// parseSinaQuotes 解析新浪批量快照，kinds 用于决定成交量单位（个股是股、指数是手）。
func parseSinaQuotes(body []byte, kinds map[string]Kind) map[string]sinaQuote {
	out := map[string]sinaQuote{}
	text := string(body)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		head := strings.TrimSpace(line[:eq])
		head = strings.TrimPrefix(head, "var hq_str_")
		payload := strings.Trim(strings.TrimSpace(line[eq+1:]), "\";")
		if payload == "" {
			continue
		}
		fields := strings.Split(payload, ",")
		if len(fields) <= sinaIdxTime {
			continue
		}
		symbol := strings.ToLower(strings.TrimSpace(head))
		quote := sinaQuote{
			Symbol:    symbol,
			Name:      strings.TrimSpace(fields[sinaIdxName]),
			Open:      numOrZero(fields[sinaIdxOpen]),
			PrevClose: numOrZero(fields[sinaIdxPrevClose]),
			Price:     numOrZero(fields[sinaIdxPrice]),
			High:      numOrZero(fields[sinaIdxHigh]),
			Low:       numOrZero(fields[sinaIdxLow]),
			Amount:    numOrZero(fields[sinaIdxAmount]),
		}
		if raw, ok := parseNum(fields[sinaIdxVolume]); ok {
			if kinds[symbol] == KindIndex {
				quote.VolumeHand = raw // 指数已经是手
			} else {
				quote.VolumeHand = raw / 100 // 个股是股
			}
		}
		date := strings.TrimSpace(fields[sinaIdxDate])
		clock := strings.TrimSpace(fields[sinaIdxTime])
		quote.Time = cstStamp(date + " " + clock)
		if quote.Name == "" || (quote.Price <= 0 && quote.PrevClose <= 0) {
			continue
		}
		quote.Valid = true
		out[symbol] = quote
	}
	return out
}

// sinaQuoteURL 拼接批量快照地址。
func sinaQuoteURL(symbols []string) string {
	return "https://hq.sinajs.cn/list=" + strings.Join(symbols, ",")
}

// sinaMoneyFlow 是新浪个股资金流的解析结果（r0 超大 / r1 大 / r2 中 / r3 小）。
type sinaMoneyFlow struct {
	Name       string
	Price      float64
	ChangePct  float64
	SuperNet   float64
	LargeNet   float64
	MediumNet  float64
	SmallNet   float64
	MainNet    float64
	MainPct    float64
	AmountYuan float64
}

// parseSinaMoneyFlow 解析 MoneyFlow.ssi_ssfx_flzjtj。
//
// 该接口的 r0/r1/r2/r3 是「买入额+卖出额」，净额必须用 rN_in - rN_out 计算；
// netamount 是各级净额之和，可用来交叉校验（实测完全相等）。
func parseSinaMoneyFlow(body []byte) (sinaMoneyFlow, error) {
	payload := strings.TrimSpace(string(body))
	if payload == "" {
		return sinaMoneyFlow{}, ErrEmptyResponse
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return sinaMoneyFlow{}, fmt.Errorf("datasource: 新浪资金流格式异常: %w", err)
	}
	get := func(key string) float64 {
		value, ok := raw[key]
		if !ok {
			return 0
		}
		text, ok := value.(string)
		if !ok {
			if num, ok := value.(float64); ok {
				return num
			}
			return 0
		}
		return numOrZero(text)
	}
	out := sinaMoneyFlow{
		Name:       strings.TrimSpace(fmt.Sprint(raw["name"])),
		Price:      get("trade"),
		ChangePct:  get("changeratio") * 100, // 接口给的是小数比例
		SuperNet:   get("r0_in") - get("r0_out"),
		LargeNet:   get("r1_in") - get("r1_out"),
		MediumNet:  get("r2_in") - get("r2_out"),
		SmallNet:   get("r3_in") - get("r3_out"),
		MainNet:    (get("r0_in") - get("r0_out")) + (get("r1_in") - get("r1_out")),
		AmountYuan: get("netamount"),
	}
	if out.Name == "" {
		return sinaMoneyFlow{}, fmt.Errorf("%w: 新浪资金流缺少名称", ErrEmptyResponse)
	}
	return out, nil
}

// parseSinaKline 解析新浪日 K 线（day,open,high,low,close,volume）。
// 新浪 K 线的 volume 一律是「股」（指数同口径：sh000001 给 40486517700），故统一 /100。
func parseSinaKline(body []byte) ([]contract.Bar, error) {
	payload := strings.TrimSpace(string(body))
	if payload == "" || payload == "null" {
		return nil, ErrEmptyResponse
	}
	var rows []struct {
		Day    string `json:"day"`
		Open   string `json:"open"`
		High   string `json:"high"`
		Low    string `json:"low"`
		Close  string `json:"close"`
		Volume string `json:"volume"`
	}
	if err := json.Unmarshal([]byte(payload), &rows); err != nil {
		return nil, fmt.Errorf("datasource: 新浪 K 线格式异常: %w", err)
	}
	bars := make([]contract.Bar, 0, len(rows))
	for _, row := range rows {
		open, ok1 := parseNum(row.Open)
		high, ok2 := parseNum(row.High)
		low, ok3 := parseNum(row.Low)
		closePrice, ok4 := parseNum(row.Close)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		volumeShares, _ := parseNum(row.Volume)
		bars = append(bars, contract.Bar{
			Date:   strings.TrimSpace(row.Day),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closePrice,
			Volume: volumeShares / 100,
		})
	}
	if len(bars) == 0 {
		return nil, ErrEmptyResponse
	}
	return bars, nil
}

// sinaKlineURL 拼接新浪日 K 线地址（scale=240 表示日线）。
func sinaKlineURL(symbol string, limit int) string {
	return fmt.Sprintf("https://quotes.sina.cn/cn/api/json_v2.php/CN_MarketDataService.getKLineData?symbol=%s&scale=240&ma=no&datalen=%d", symbol, limit)
}

// sinaMoneyFlowURL 拼接新浪个股资金流地址。
func sinaMoneyFlowURL(symbol string) string {
	return "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssi_ssfx_flzjtj?daima=" + symbol
}
