package datasource

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// Kind 是标的类别。个股/指数/板块的代码前缀、成交量单位、可用接口都不同，
// 必须在入口处分清，不能靠调用方猜测。
type Kind string

const (
	KindStock   Kind = "stock"
	KindIndex   Kind = "index"
	KindSector  Kind = "sector"
	KindConcept Kind = "concept"
)

// Code 是归一化后的标的标识。
type Code struct {
	Raw    string          `json:"raw"`
	Symbol string          `json:"symbol"`
	Market contract.Market `json:"market"`
	Kind   Kind            `json:"kind"`
}

var (
	errEmptyCode  = errors.New("datasource: 代码不能为空")
	errBadCode    = errors.New("datasource: 无法识别的代码")
	digitSymbolRe = regexp.MustCompile(`^\d{6}$`)
	bkSymbolRe    = regexp.MustCompile(`(?i)^bk\d{4}$`)
	thsAlphaRe    = regexp.MustCompile(`(?i)^[0-9][a-z][0-9]{4}$`)
	// 同花顺板块/概念指数：881xxx（行业）、883/884/885xxx（概念系列）
	thsSectorRe = regexp.MustCompile(`^88[1-5]\d{3}$`)
)

// thsIndexSymbols 是同花顺指数代码，键为「市场+6 位代码」。
// 只收录实测可用的映射（testdata 里有 1A0001 / 399001 / 1B0300 三份样本），
// 未收录的指数不会走同花顺，自动降级到腾讯/新浪。
var thsIndexSymbols = map[string]string{
	"sh000001": "1A0001", // 上证指数
	"sh000300": "1B0300", // 沪深300
	"sz399001": "399001", // 深证成指
	"sz399006": "399006", // 创业板指
}

// ParseCode 把各种写法归一成 Code：
//
//	600000 / sh600000 / SH600000 / 1.600000 / s_sh600000 → 沪市个股
//	000001 → 深市个股（平安银行；指数必须写 sh000001，避免歧义）
//	sh000001 / 1A0001 → 上证指数
//	BK1036 / 90.BK1036 → 东财板块
//	bk_881101 / 881101 → 同花顺板块
func ParseCode(raw string) (Code, error) {
	text := strings.ToLower(strings.TrimSpace(raw))
	text = strings.ReplaceAll(text, " ", "")
	if text == "" {
		return Code{}, errEmptyCode
	}
	if strings.HasPrefix(text, "s_") {
		text = strings.TrimPrefix(text, "s_")
	}

	// 同花顺前缀：hs_600000 / hs_1a0001 / bk_881101
	if strings.HasPrefix(text, "hs_") {
		return parseTHSSymbol(raw, strings.TrimPrefix(text, "hs_"))
	}
	if strings.HasPrefix(text, "bk_") {
		return parseTHSSector(raw, strings.TrimPrefix(text, "bk_"))
	}

	// 东财 secid：1.600000 / 0.000001 / 90.BK1036
	if idx := strings.Index(text, "."); idx > 0 {
		head, tail := text[:idx], text[idx+1:]
		switch head {
		case "1":
			return finalize(raw, tail, contract.MarketSH)
		case "0":
			return finalize(raw, tail, contract.MarketSZ)
		case "90":
			return parseEMSector(raw, tail)
		}
	}

	// 市场前缀：sh/sz/bj
	for _, m := range []struct {
		prefix string
		market contract.Market
	}{{"sh", contract.MarketSH}, {"sz", contract.MarketSZ}, {"bj", contract.MarketBJ}} {
		if strings.HasPrefix(text, m.prefix) && len(text) > len(m.prefix) {
			return finalize(raw, strings.TrimPrefix(text, m.prefix), m.market)
		}
	}

	// 无前缀的板块写法
	if bkSymbolRe.MatchString(text) {
		return parseEMSector(raw, text)
	}
	if thsSectorRe.MatchString(text) {
		return parseTHSSector(raw, text)
	}
	return finalize(raw, text, "")
}

// parseTHSSymbol 处理 hs_ 前缀：hs_1a0001 是上证指数，hs_399001 是深证成指。
func parseTHSSymbol(raw, body string) (Code, error) {
	body = strings.TrimSpace(body)
	switch {
	case thsAlphaRe.MatchString(body):
		// 1A0001 → sh000001；1B0300 → sh000300
		market := contract.MarketSH
		upper := strings.ToUpper(body)
		digits := "0" + upper[2:]
		for key, ths := range thsIndexSymbols {
			if strings.EqualFold(ths, upper) {
				return Code{Raw: raw, Symbol: key[2:], Market: contract.Market(key[:2]), Kind: KindIndex}, nil
			}
		}
		return Code{Raw: raw, Symbol: digits, Market: market, Kind: KindIndex}, nil
	case strings.HasPrefix(body, "399"):
		return Code{Raw: raw, Symbol: body, Market: contract.MarketSZ, Kind: KindIndex}, nil
	default:
		return finalize(raw, body, "")
	}
}

// parseTHSSector 处理 bk_ 前缀或 881xxx/883-885xxx 写法。
func parseTHSSector(raw, body string) (Code, error) {
	body = strings.TrimSpace(body)
	if len(body) != 6 {
		return Code{}, fmt.Errorf("%w: %s", errBadCode, raw)
	}
	for _, r := range body {
		if r < '0' || r > '9' {
			return Code{}, fmt.Errorf("%w: %s", errBadCode, raw)
		}
	}
	return Code{Raw: raw, Symbol: body, Kind: KindSector}, nil
}

// parseEMSector 处理 BK1036 与 90.BK1036。
func parseEMSector(raw, body string) (Code, error) {
	upper := strings.ToUpper(strings.TrimSpace(body))
	if !bkSymbolRe.MatchString(upper) {
		return Code{}, fmt.Errorf("%w: %s", errBadCode, raw)
	}
	return Code{Raw: raw, Symbol: upper, Kind: KindSector}, nil
}

// finalize 在已知/可推断市场的前提下判定个股或指数。
func finalize(raw, symbol string, market contract.Market) (Code, error) {
	if symbol == "" {
		return Code{}, fmt.Errorf("%w: %s", errBadCode, raw)
	}
	// 同花顺 alpha 指数代码在无前缀时也接受
	if thsAlphaRe.MatchString(symbol) {
		return parseTHSSymbol(raw, symbol)
	}
	if !digitSymbolRe.MatchString(symbol) {
		return Code{}, fmt.Errorf("%w: %s", errBadCode, raw)
	}
	if market == "" {
		market = inferMarket(symbol)
	}
	if market == "" {
		return Code{}, fmt.Errorf("%w: %s", errBadCode, raw)
	}
	kind := KindStock
	if isIndexSymbol(market, symbol) {
		kind = KindIndex
	}
	return Code{Raw: raw, Symbol: symbol, Market: market, Kind: kind}, nil
}

// inferMarket 按代码前缀推断交易所：北交所优先，避免 9 开头沪市 B 股被误判。
func inferMarket(symbol string) contract.Market {
	switch {
	case strings.HasPrefix(symbol, "43"), strings.HasPrefix(symbol, "83"),
		strings.HasPrefix(symbol, "87"), strings.HasPrefix(symbol, "92"):
		return contract.MarketBJ
	case strings.HasPrefix(symbol, "6"), strings.HasPrefix(symbol, "9"), strings.HasPrefix(symbol, "5"):
		return contract.MarketSH
	case strings.HasPrefix(symbol, "0"), strings.HasPrefix(symbol, "3"),
		strings.HasPrefix(symbol, "2"), strings.HasPrefix(symbol, "1"):
		return contract.MarketSZ
	}
	return ""
}

// isIndexSymbol 判定指数：
// 沪市 000xxx（上证系列）与 950xxx，深市 399xxx，北交所 899xxx。
func isIndexSymbol(market contract.Market, symbol string) bool {
	switch market {
	case contract.MarketSH:
		return strings.HasPrefix(symbol, "000") || strings.HasPrefix(symbol, "950") ||
			strings.HasPrefix(symbol, "1B") || strings.HasPrefix(symbol, "1A")
	case contract.MarketSZ:
		return strings.HasPrefix(symbol, "399")
	case contract.MarketBJ:
		return strings.HasPrefix(symbol, "899")
	}
	return false
}

// ID 是归一化后的唯一标识，用于缓存键与批量请求去重。
func (c Code) ID() string {
	if c.Kind == KindSector || c.Kind == KindConcept {
		return string(c.Kind) + ":" + c.Symbol
	}
	return string(c.Market) + c.Symbol
}

// THSSymbol 返回同花顺代码（hs_600000 / hs_1A0001 / bk_881101）；不支持时返回空串。
func (c Code) THSSymbol() string {
	switch c.Kind {
	case KindSector, KindConcept:
		if len(c.Symbol) == 6 && !bkSymbolRe.MatchString(c.Symbol) {
			return "bk_" + c.Symbol
		}
		return ""
	case KindIndex:
		if mapped, ok := thsIndexSymbols[string(c.Market)+c.Symbol]; ok {
			return "hs_" + mapped
		}
		// 未收录映射的指数：深证 399xxx 与同花顺同号，可直接用
		if c.Market == contract.MarketSZ && strings.HasPrefix(c.Symbol, "399") {
			return "hs_" + c.Symbol
		}
		return ""
	default:
		if c.Market == "" {
			return ""
		}
		return "hs_" + c.Symbol
	}
}

// SecID 返回东财 secid（1.600000 / 0.000001 / 90.BK1036）；不支持时返回空串。
func (c Code) SecID() string {
	switch c.Kind {
	case KindSector, KindConcept:
		if bkSymbolRe.MatchString(c.Symbol) {
			return "90." + c.Symbol
		}
		return ""
	}
	switch c.Market {
	case contract.MarketSH:
		return "1." + c.Symbol
	case contract.MarketSZ, contract.MarketBJ:
		return "0." + c.Symbol
	}
	return ""
}

// MarketSymbol 返回腾讯/新浪使用的 symbol（sh600000 / sz000001 / bj430047）。
func (c Code) MarketSymbol() string {
	if c.Kind == KindSector || c.Kind == KindConcept || c.Market == "" {
		return ""
	}
	return string(c.Market) + c.Symbol
}

// QuoteCode 是写进 sync_result / 前端展示的代码形态。
func (c Code) QuoteCode() string {
	if c.Kind == KindSector || c.Kind == KindConcept {
		return c.Symbol
	}
	return c.Symbol
}
