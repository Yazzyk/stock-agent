package datasource

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 同花顺 realhead 的 items 字段号（全部用真实响应与腾讯/新浪交叉校验过，
// fixture 见 testdata/ths_realhead_*.js）：
//
//	6  = 昨收        7  = 今开        8  = 最高        9  = 最低
//	10 = 现价        13 = 成交量(股)   19 = 成交额(元)
//	199112 = 涨跌幅%  264648 = 涨跌额  1968584 = 换手%
//	526792 = 振幅%   1771976 = 量比
//	37 = 上涨家数  38 = 平盘家数  39 = 下跌家数（只有指数才有；**只覆盖单一市场**）
//
// 校验样例（2026-11-10 收盘，600000）：现价 10.20、昨收 10.10、开 10.06、
// 高 10.24、低 10.00、量 47,782,652 股、额 485,074,220 元 —— 与腾讯
// `qt.gtimg.cn` 和新浪 `hq.sinajs.cn` 完全一致。
const (
	thsItemPrevClose   = "6"
	thsItemOpen        = "7"
	thsItemHigh        = "8"
	thsItemLow         = "9"
	thsItemPrice       = "10"
	thsItemMarketID    = "12"
	thsItemVolume      = "13"
	thsItemAmount      = "19"
	thsItemLimitUp     = "69"
	thsItemLimitDown   = "70"
	thsItemChangePct   = "199112"
	thsItemChange      = "264648"
	thsItemTurnover    = "1968584"
	thsItemAmplitude   = "526792"
	thsItemVolumeRatio = "1771976"
	// 指数专属的涨跌家数：37 上涨 / 38 下跌 / 39 平盘。
	// 实测（2026-11-11 收盘，hs_1A0001）37=2360、38=1656、39=622，
	// 与沪市「涨多跌少 + 平盘几百只」的常识一致；若按 38=平盘 解读会得到 1656 只平盘，明显不合理。
	// 注意它只统计该指数所属市场（上证 -> 沪市），不是全市场口径，只能作降级用。
	thsItemUpCount   = "37"
	thsItemDownCount = "38"
	thsItemFlatCount = "39"
)

// 同花顺 marketid → 交易所（实测：17 沪股、33 深股、16 沪指、32 深指、48 板块）。
var thsMarketIDs = map[string]contract.Market{
	"17": contract.MarketSH,
	"33": contract.MarketSZ,
	"16": contract.MarketSH,
	"32": contract.MarketSZ,
}

// thsRealhead 是一次同花顺快照的解析结果。
type thsRealhead struct {
	Code        string
	Name        string
	Market      contract.Market
	Kind        Kind
	Price       float64
	PrevClose   float64
	Open        float64
	High        float64
	Low         float64
	Change      float64
	ChangePct   float64
	VolumeHand  float64 // 手（原始为股，已除 100）
	Amount      float64 // 元
	Turnover    float64
	Amplitude   float64
	VolumeRatio float64
	LimitUp     float64
	LimitDown   float64
	// Up/Flat/Down 是指数口径的涨跌家数（个股没有这三个字段，值为 0）。
	Up     int
	Flat   int
	Down   int
	Time   string // RFC3339（+08:00）
	Status string // 同花顺 stockStatus 原值：闭市 / -- 等
}

// parseTHSRealhead 解析 realhead JSONP。code 用于判定类别与成交量单位。
// 同花顺返回的成交量对个股/指数/板块都是「股」（三份 fixture 均已核对），故统一 /100 出手。
func parseTHSRealhead(body []byte, code Code) (thsRealhead, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return thsRealhead{}, err
	}
	var envelope struct {
		Items map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return thsRealhead{}, fmt.Errorf("datasource: 同花顺快照格式异常: %w", err)
	}
	if len(envelope.Items) == 0 {
		return thsRealhead{}, ErrEmptyResponse
	}
	items := make(map[string]string, len(envelope.Items))
	for k, raw := range envelope.Items {
		items[k] = jsonScalar(raw)
	}

	out := thsRealhead{
		Code:        strings.TrimSpace(items["5"]),
		Market:      code.Market,
		Kind:        code.Kind,
		PrevClose:   numOrZero(items[thsItemPrevClose]),
		Open:        numOrZero(items[thsItemOpen]),
		High:        numOrZero(items[thsItemHigh]),
		Low:         numOrZero(items[thsItemLow]),
		Price:       numOrZero(items[thsItemPrice]),
		Amount:      numOrZero(items[thsItemAmount]),
		Change:      numOrZero(items[thsItemChange]),
		ChangePct:   numOrZero(items[thsItemChangePct]),
		Turnover:    numOrZero(items[thsItemTurnover]),
		Amplitude:   numOrZero(items[thsItemAmplitude]),
		LimitUp:     numOrZero(items[thsItemLimitUp]),
		LimitDown:   numOrZero(items[thsItemLimitDown]),
		VolumeRatio: numOrZero(items[thsItemVolumeRatio]),
		Up:          int(numOrZero(items[thsItemUpCount])),
		Flat:        int(numOrZero(items[thsItemFlatCount])),
		Down:        int(numOrZero(items[thsItemDownCount])),
		Status:      strings.TrimSpace(items["stockStatus"]),
	}
	if name, ok := items["name"]; ok {
		out.Name = strings.TrimSpace(name)
	}
	if market, ok := thsMarketIDs[items[thsItemMarketID]]; ok {
		out.Market = market
	}
	// 成交量：同花顺给的是「股」
	if shares, ok := parseNum(items[thsItemVolume]); ok {
		out.VolumeHand = shares / 100
	}
	if stamp := strings.TrimSpace(items["updateTime"]); stamp != "" {
		out.Time = cstStamp(stamp)
	}
	if out.Time == "" {
		if clock := strings.TrimSpace(items["time"]); clock != "" {
			// 形如 "2026-11-10 21:42:31 北京时间"
			if fields := strings.Fields(clock); len(fields) >= 2 {
				out.Time = cstStamp(fields[0] + " " + fields[1])
			}
		}
	}
	if out.Price <= 0 && out.PrevClose > 0 {
		// 停牌/未开盘时同花顺可能不给现价，用昨收兜底，由调用方标记 stale。
		out.Price = out.PrevClose
	}
	if out.Name == "" && out.Code == "" {
		return thsRealhead{}, ErrEmptyResponse
	}
	return out, nil
}

// jsonScalar 把任意 JSON 标量转成字符串（items 里既有字符串也有数字与布尔）。
func jsonScalar(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return ""
	}
	if unquoted, err := strconv.Unquote(text); err == nil {
		return unquoted
	}
	return text
}

// cstStamp 把 "2026-11-10 15:30" 或 "2026-11-10 15:30:00" 规整成 RFC3339（+08:00）。
func cstStamp(stamp string) string {
	layouts := []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, strings.TrimSpace(stamp), time.FixedZone("CST", 8*3600)); err == nil {
			return t.Format(time.RFC3339)
		}
	}
	return ""
}

// thsLineData 是同花顺 K 线文件的解析结果。
type thsLineData struct {
	Name   string
	Market string
	Bars   []contract.Bar
}

// parseTHSLine 解析 /v6/line/<symbol>/<period>/<year|last>.js。
//
// 每行字段：日期,开,高,低,收,成交量(股),成交额(元),换手率,...
// 注意顺序是「开高低收」，与腾讯/新浪 K 线的「开收高低」不同，写错就会把高低点算反。
func parseTHSLine(body []byte, code Code) (thsLineData, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return thsLineData{}, err
	}
	var envelope struct {
		Name       string `json:"name"`
		MarketType string `json:"marketType"`
		Data       string `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return thsLineData{}, fmt.Errorf("datasource: 同花顺 K 线格式异常: %w", err)
	}
	bars := make([]contract.Bar, 0, 256)
	for _, row := range strings.Split(envelope.Data, ";") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		fields := strings.Split(row, ",")
		if len(fields) < 7 {
			continue
		}
		open, ok1 := parseNum(fields[1])
		high, ok2 := parseNum(fields[2])
		low, ok3 := parseNum(fields[3])
		closePrice, ok4 := parseNum(fields[4])
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		volumeShares, _ := parseNum(fields[5])
		amount, _ := parseNum(fields[6])
		bars = append(bars, contract.Bar{
			Date:   formatTHSDate(fields[0]),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closePrice,
			Volume: volumeShares / 100, // 股 → 手
			Amount: amount,
		})
	}
	if len(bars) == 0 {
		return thsLineData{}, ErrEmptyResponse
	}
	return thsLineData{Name: envelope.Name, Market: envelope.MarketType, Bars: bars}, nil
}

// formatTHSDate 把 20261110 / 202611101030 转成 YYYY-MM-DD 或 YYYY-MM-DD HH:MM。
func formatTHSDate(raw string) string {
	text := strings.TrimSpace(raw)
	switch len(text) {
	case 8:
		return text[0:4] + "-" + text[4:6] + "-" + text[6:8]
	case 12:
		return text[0:4] + "-" + text[4:6] + "-" + text[6:8] + " " + text[8:10] + ":" + text[10:12]
	}
	return text
}

// thsURLs 是同花顺接口地址构造。
func thsRealheadURL(symbol string) string {
	return "https://d.10jqka.com.cn/v6/realhead/" + symbol + "/defer/last.js"
}

func thsLineURL(symbol, periodCode, suffix string) string {
	return "https://d.10jqka.com.cn/v6/line/" + symbol + "/" + periodCode + "/" + suffix + ".js"
}
