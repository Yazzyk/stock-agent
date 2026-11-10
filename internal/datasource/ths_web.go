package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 同花顺网页端自选（默认「自选股」分组只存在服务端，本地 cloud_store 里没有），
// 需要登录态 cookie：
//
//	优先环境变量 GUANLAN_THS_COOKIE
//	其次 <DataDir>/ths_cookie.txt（0600，用户自己粘贴，插件只读）
//
// 两者都没有时返回 ErrNeedWebLogin，由前端引导用户去浏览器登录后粘贴。
const (
	thsWebAPIURL  = "https://t.10jqka.com.cn/newcircle/group/getSelfStockWithMarket/"
	thsWebReferer = "https://t.10jqka.com.cn/newcircle/user/userPersonal/"
	thsCookieFile = "ths_cookie.txt"
	thsCookieEnv  = "GUANLAN_THS_COOKIE"
)

// ErrNeedWebLogin 表示没有同花顺网页端登录态。
var ErrNeedWebLogin = errors.New("未找到同花顺网页端登录态：请在浏览器登录 t.10jqka.com.cn 后，把 cookie 写入数据目录下的 ths_cookie.txt（或设置环境变量 GUANLAN_THS_COOKIE）")

// thsWebItem 是网页端自选列表的一项。
type thsWebItem struct {
	Code     string `json:"code"`
	MarketID string `json:"marketid"`
}

type thsWebResponse struct {
	ErrorCode json.Number  `json:"errorCode"`
	ErrorMsg  string       `json:"errorMsg"`
	Result    []thsWebItem `json:"result"`
}

// thsCookie 读取用户提供的登录态 cookie；空串表示没有。
func (s *Service) thsCookie() string {
	if v := strings.TrimSpace(os.Getenv(thsCookieEnv)); v != "" {
		return v
	}
	if s.dir == "" {
		return ""
	}
	path := filepath.Join(s.dir, thsCookieFile)
	if s.cfg.Guard != nil {
		if err := s.cfg.Guard.CheckPath(path); err != nil {
			s.log.Debug("cookie 文件路径不在允许目录内", "path", path, "error", err)
			return ""
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// thsWebGroups 用网页端登录态拉默认自选分组。
func (s *Service) thsWebGroups(ctx context.Context) (thsGroup, error) {
	cookie := s.thsCookie()
	if cookie == "" {
		return thsGroup{}, ErrNeedWebLogin
	}
	body, err := s.client.get(ctx, request{
		source:  sourceTHS,
		url:     thsWebAPIURL,
		referer: thsWebReferer,
		headers: map[string]string{
			"Cookie":           cookie,
			"Accept":           "application/json, text/javascript, */*; q=0.01",
			"X-Requested-With": "XMLHttpRequest",
		},
	})
	if err != nil {
		return thsGroup{}, err
	}
	return parseTHSWebSelfStock(body)
}

// parseTHSWebSelfStock 解析网页端自选响应（JSON 或 JSONP）。
// errorCode != 0 一律按登录态失效处理，因为该接口的非零错误几乎都是 cookie 过期。
func parseTHSWebSelfStock(body []byte) (thsGroup, error) {
	payload, err := unwrapJSONP(body)
	if err != nil {
		return thsGroup{}, err
	}
	var parsed thsWebResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return thsGroup{}, fmt.Errorf("datasource: 同花顺网页端自选格式异常: %w", err)
	}
	if code := strings.TrimSpace(parsed.ErrorCode.String()); code != "" && code != "0" {
		msg := strings.TrimSpace(parsed.ErrorMsg)
		if msg == "" {
			msg = "errorCode=" + code
		}
		return thsGroup{}, fmt.Errorf("%w（%s）", ErrNeedWebLogin, msg)
	}
	if len(parsed.Result) == 0 {
		return thsGroup{}, errors.New("datasource: 同花顺网页端自选列表为空，请确认账号里已添加自选股")
	}
	codes := make([]string, 0, len(parsed.Result))
	seen := make(map[string]bool, len(parsed.Result))
	for _, item := range parsed.Result {
		code, ok := thsWebItemCode(item)
		if !ok || seen[code.MarketSymbol()] {
			continue
		}
		seen[code.MarketSymbol()] = true
		codes = append(codes, code.MarketSymbol())
	}
	if len(codes) == 0 {
		return thsGroup{}, errors.New("datasource: 同花顺网页端自选列表里没有可同步的标的")
	}
	return thsGroup{ExternalID: "-1", Name: "自选股", Codes: codes}, nil
}

// thsWebItemCode 用 marketid（17 沪 / 33 深 / 151、152 北）优先判定市场，
// 缺失时按代码前缀兜底，避免 5 开头沪市基金、9 开头 B 股被误判。
func thsWebItemCode(item thsWebItem) (Code, bool) {
	text := strings.TrimSpace(item.Code)
	switch strings.TrimSpace(item.MarketID) {
	case "17":
		return codeOrSkip(contract.MarketSH, text)
	case "33":
		return codeOrSkip(contract.MarketSZ, text)
	case "151", "152":
		return codeOrSkip(contract.MarketBJ, text)
	}
	code, err := ParseCode(text)
	if err != nil {
		return Code{}, false
	}
	return code, true
}

func codeOrSkip(market contract.Market, symbol string) (Code, bool) {
	code, err := ParseCode(string(market) + symbol)
	if err != nil {
		return Code{}, false
	}
	// 板块/概念指数没有对应行情，跳过
	if code.Kind != KindStock && code.Kind != KindIndex {
		return Code{}, false
	}
	if code.Kind == KindIndex {
		return Code{}, false
	}
	return code, true
}
