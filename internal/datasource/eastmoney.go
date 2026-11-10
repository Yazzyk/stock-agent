package datasource

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 东财 kline 的 klines 行字段（fields2=f51..f61）：
//
//	0 日期  1 开  2 收  3 高  4 低  5 成交量(手)  6 成交额(元)
//	7 振幅%  8 涨跌幅%  9 涨跌额  10 换手率%
//
// 注意顺序是「开收高低」，与同花顺的「开高低收」不同。
const (
	emKlineIdxDate     = 0
	emKlineIdxOpen     = 1
	emKlineIdxClose    = 2
	emKlineIdxHigh     = 3
	emKlineIdxLow      = 4
	emKlineIdxVolume   = 5
	emKlineIdxAmount   = 6
	emKlineMinFields   = 7
	emFFlowIdxDate     = 0
	emFFlowIdxMain     = 1
	emFFlowIdxSmall    = 2
	emFFlowIdxMedium   = 3
	emFFlowIdxLarge    = 4
	emFFlowIdxSuper    = 5
	emFFlowMinFields   = 6
	emVolumeRatioToken = ""
)

// emKlineData 是东财 K 线的解析结果。
type emKlineData struct {
	Code string
	Name string
	Bars []contract.Bar
	Raw  []string
}

// parseEMKline 解析 push2 kline 响应（可能带 cb= 包裹）。
func parseEMKline(body []byte) (emKlineData, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return emKlineData{}, err
	}
	var envelope struct {
		RC   int `json:"rc"`
		Data *struct {
			Code   string   `json:"code"`
			Name   string   `json:"name"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return emKlineData{}, fmt.Errorf("datasource: 东财 K 线格式异常: %w", err)
	}
	if envelope.Data == nil || len(envelope.Data.Klines) == 0 {
		return emKlineData{}, ErrEmptyResponse
	}
	bars := make([]contract.Bar, 0, len(envelope.Data.Klines))
	for _, row := range envelope.Data.Klines {
		fields := strings.Split(row, ",")
		if len(fields) < emKlineMinFields {
			continue
		}
		open, ok1 := parseNum(fields[emKlineIdxOpen])
		closePrice, ok2 := parseNum(fields[emKlineIdxClose])
		high, ok3 := parseNum(fields[emKlineIdxHigh])
		low, ok4 := parseNum(fields[emKlineIdxLow])
		if !ok1 || !ok2 || !ok3 || !ok4 {
			continue
		}
		volume, _ := parseNum(fields[emKlineIdxVolume])
		amount, _ := parseNum(fields[emKlineIdxAmount])
		bars = append(bars, contract.Bar{
			Date:   strings.TrimSpace(fields[emKlineIdxDate]),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closePrice,
			Volume: volume, // 东财 K 线成交量单位是手，无需换算
			Amount: amount,
		})
	}
	if len(bars) == 0 {
		return emKlineData{}, ErrEmptyResponse
	}
	return emKlineData{Code: envelope.Data.Code, Name: envelope.Data.Name, Bars: bars, Raw: envelope.Data.Klines}, nil
}

// emFFlow 是个股资金流的解析结果。
type emFFlow struct {
	Code      string
	Name      string
	Date      string
	MainNet   float64
	SmallNet  float64
	MediumNet float64
	LargeNet  float64
	SuperNet  float64
}

// parseEMFFlow 解析 push2 fflow/kline 响应，取最后一根（最新交易日）。
// 校验口径：主力净额 == 大单 + 超大单（fixture 里 26,922,753 == 17,282,621 + 9,640,132）。
func parseEMFFlow(body []byte) (emFFlow, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return emFFlow{}, err
	}
	var envelope struct {
		RC   int `json:"rc"`
		Data *struct {
			Code   string   `json:"code"`
			Name   string   `json:"name"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return emFFlow{}, fmt.Errorf("datasource: 东财资金流格式异常: %w", err)
	}
	if envelope.Data == nil || len(envelope.Data.Klines) == 0 {
		return emFFlow{}, ErrEmptyResponse
	}
	last := envelope.Data.Klines[len(envelope.Data.Klines)-1]
	fields := strings.Split(last, ",")
	if len(fields) < emFFlowMinFields {
		return emFFlow{}, fmt.Errorf("datasource: 东财资金流行字段不足: %q", last)
	}
	return emFFlow{
		Code:      envelope.Data.Code,
		Name:      envelope.Data.Name,
		Date:      strings.TrimSpace(fields[emFFlowIdxDate]),
		MainNet:   numOrZero(fields[emFFlowIdxMain]),
		SmallNet:  numOrZero(fields[emFFlowIdxSmall]),
		MediumNet: numOrZero(fields[emFFlowIdxMedium]),
		LargeNet:  numOrZero(fields[emFFlowIdxLarge]),
		SuperNet:  numOrZero(fields[emFFlowIdxSuper]),
	}, nil
}

// emRankItem 是板块/概念资金流排名的一行。
type emRankItem struct {
	Code    string
	Name    string
	MainNet float64
}

// parseEMBkzj 解析 data.eastmoney.com 的板块资金流排名（diff[].f12/f14/f62）。
func parseEMBkzj(body []byte) ([]emRankItem, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		RC   int `json:"rc"`
		Data *struct {
			Total int `json:"total"`
			Diff  []struct {
				F12 string  `json:"f12"`
				F14 string  `json:"f14"`
				F62 float64 `json:"f62"`
			} `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("datasource: 东财板块资金流格式异常: %w", err)
	}
	if envelope.Data == nil || len(envelope.Data.Diff) == 0 {
		return nil, ErrEmptyResponse
	}
	out := make([]emRankItem, 0, len(envelope.Data.Diff))
	for _, item := range envelope.Data.Diff {
		out = append(out, emRankItem{Code: strings.TrimSpace(item.F12), Name: strings.TrimSpace(item.F14), MainNet: item.F62})
	}
	return out, nil
}

// emClistItem 是板块/概念列表的一行（含涨跌幅与主力净额）。
type emClistItem struct {
	Code      string
	Name      string
	ChangePct float64
	MainNet   float64
}

// parseEMClist 解析 push2 clist 响应（fs=m:90+t:2 行业 / t:3 概念）。
func parseEMClist(body []byte) ([]emClistItem, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		RC   int `json:"rc"`
		Data *struct {
			Total int `json:"total"`
			Diff  []struct {
				F3  float64 `json:"f3"`
				F12 string  `json:"f12"`
				F14 string  `json:"f14"`
				F62 float64 `json:"f62"`
			} `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("datasource: 东财板块列表格式异常: %w", err)
	}
	if envelope.Data == nil || len(envelope.Data.Diff) == 0 {
		return nil, ErrEmptyResponse
	}
	out := make([]emClistItem, 0, len(envelope.Data.Diff))
	for _, item := range envelope.Data.Diff {
		out = append(out, emClistItem{
			Code:      strings.TrimSpace(item.F12),
			Name:      strings.TrimSpace(item.F14),
			ChangePct: item.F3,
			MainNet:   item.F62,
		})
	}
	return out, nil
}

// StockSuggestion 是搜索建议的统一中间结构（东财/腾讯两种响应都归一到它）。
type StockSuggestion struct {
	Code   string
	Name   string
	Market contract.Market
}

// parseEMSuggest 解析东财 suggest 响应。
func parseEMSuggest(body []byte) ([]StockSuggestion, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Table *struct {
			Data []struct {
				Code             string `json:"Code"`
				Name             string `json:"Name"`
				Classify         string `json:"Classify"`
				SecurityTypeName string `json:"SecurityTypeName"`
				QuoteID          string `json:"QuoteID"`
			} `json:"Data"`
		} `json:"QuotationCodeTable"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("datasource: 东财搜索格式异常: %w", err)
	}
	if envelope.Table == nil || len(envelope.Table.Data) == 0 {
		return nil, ErrEmptyResponse
	}
	out := make([]StockSuggestion, 0, len(envelope.Table.Data))
	for _, item := range envelope.Table.Data {
		code := strings.TrimSpace(item.Code)
		name := strings.TrimSpace(item.Name)
		if code == "" || name == "" {
			continue
		}
		classify := strings.ToLower(item.Classify)
		if !strings.Contains(classify, "astock") && !strings.Contains(classify, "index") {
			continue
		}
		market := contract.Market("")
		switch {
		case strings.Contains(item.SecurityTypeName, "沪"):
			market = contract.MarketSH
		case strings.Contains(item.SecurityTypeName, "深"):
			market = contract.MarketSZ
		case strings.Contains(item.SecurityTypeName, "北"):
			market = contract.MarketBJ
		}
		if market == "" {
			if quoteID := strings.ToLower(strings.TrimSpace(item.QuoteID)); strings.Contains(quoteID, ".") {
				prefix := strings.SplitN(quoteID, ".", 2)[0]
				if prefix == "1" {
					market = contract.MarketSH
				} else {
					market = inferMarket(code)
				}
			}
		}
		if market == "" {
			market = inferMarket(code)
		}
		if market == "" {
			continue
		}
		out = append(out, StockSuggestion{Code: code, Name: name, Market: market})
	}
	return out, nil
}

func emKlineURL(secid string, periodCode int, adjust int, limit int) string {
	query := url.Values{}
	query.Set("secid", secid)
	query.Set("klt", fmt.Sprintf("%d", periodCode))
	query.Set("fqt", fmt.Sprintf("%d", adjust))
	query.Set("lmt", fmt.Sprintf("%d", limit))
	query.Set("end", "20500101")
	query.Set("fields1", "f1,f2,f3,f4,f5,f6")
	query.Set("fields2", "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61")
	return "https://push2.eastmoney.com/api/qt/stock/kline/get?" + query.Encode()
}

func emFFlowURL(secid string) string {
	query := url.Values{}
	query.Set("secid", secid)
	query.Set("lmt", "0")
	query.Set("klt", "101")
	query.Set("fields1", "f1,f2,f3,f7")
	query.Set("fields2", "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63,f64,f65")
	return "https://push2.eastmoney.com/api/qt/stock/fflow/kline/get?" + query.Encode()
}

func emSectorFlowURL(kind Kind, limit int) string {
	// t:2 行业板块 / t:3 概念板块
	code := "m:90+t:2"
	if kind == KindConcept {
		code = "m:90+t:3"
	}
	query := url.Values{}
	query.Set("key", "f62")
	query.Set("code", code)
	return "https://data.eastmoney.com/dataapi/bkzj/getbkzj?" + query.Encode()
}

func emSectorListURL(kind Kind, limit int) string {
	code := "m:90+t:2"
	if kind == KindConcept {
		code = "m:90+t:3"
	}
	query := url.Values{}
	query.Set("pn", "1")
	query.Set("pz", fmt.Sprintf("%d", limit))
	query.Set("po", "1")
	query.Set("np", "1")
	query.Set("fltt", "2")
	query.Set("invt", "2")
	query.Set("fid", "f62")
	query.Set("fs", code)
	query.Set("fields", "f3,f12,f14,f62")
	return "https://push2.eastmoney.com/api/qt/clist/get?" + query.Encode()
}

func emSuggestURL(keyword string, limit int) string {
	query := url.Values{}
	query.Set("input", keyword)
	query.Set("type", "14")
	query.Set("count", fmt.Sprintf("%d", limit))
	query.Set("token", "D43BF722C8E33BDC906FB84D85E326E8")
	return "https://searchapi.eastmoney.com/api/suggest/get?" + query.Encode()
}

func txSuggestURL(keyword string) string {
	return "https://smartbox.gtimg.cn/s3/?q=" + url.QueryEscape(keyword) + "&t=all"
}
