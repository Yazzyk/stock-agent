package datasource

// 新浪行业/概念资金流：数据层唯一的板块资金流来源（MoneyFlow.ssl_bkzj_*）：
//   - 排名：ssl_bkzj_bk，一次返回行业(0)/概念(1)的净额、流入/流出与涨跌幅；
//   - 多日：ssl_bkzj_zjlrqs?bankuai=<category>，返回每日 netamount/r0_net、均价与涨跌幅。
// 口径：新浪排名给的是「全部资金净额(netamount)」（inamount - outamount），
// 不是「主力净额（大单 + 超大单）」——本层不再有任何主力净额的板块口径，
// 前端与 Agent 需要把榜单读成「资金净额榜」。Source 统一标 sina。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	sinaFlowReferer = "https://finance.sina.com.cn"
	// sinaFlowMax 是排名接口一次请求的上限（实测 100 足够覆盖行业/概念前列）。
	sinaFlowMax = 100
	ttlSinaFlow = 60 * time.Second
)

// sinaFlowRankRaw 是新浪板块资金榜的一行（数值均为字符串）。
type sinaFlowRankRaw struct {
	Category   string `json:"category"`
	Name       string `json:"name"`
	NetAmount  string `json:"netamount"`
	Ratio      string `json:"ratioamount"`
	ChangeRate string `json:"avg_changeratio"`
}

// sinaFlowHistoryRaw 是新浪板块多日资金流的一行。
type sinaFlowHistoryRaw struct {
	OpenDate   string `json:"opendate"`
	AvgPrice   string `json:"avg_price"`
	ChangeRate string `json:"avg_changeratio"`
	NetAmount  string `json:"netamount"`
	Ratio      string `json:"ratioamount"`
	R0Net      string `json:"r0_net"`
}

// sinaKindCode 把我们的 sector/concept 映射到新浪 fenlei。
func sinaKindCode(kind Kind) string {
	if kind == KindConcept {
		return "1"
	}
	return "0"
}

// sinaSectorRankURL 组装行业/概念资金榜地址；asc=0 固定降序，方向由本地重排保证。
func sinaSectorRankURL(kind Kind, num int) string {
	if num <= 0 || num > sinaFlowMax {
		num = sinaFlowMax
	}
	query := url.Values{}
	query.Set("page", "1")
	query.Set("num", strconv.Itoa(num))
	query.Set("sort", "netamount")
	query.Set("asc", "0")
	query.Set("fenlei", sinaKindCode(kind))
	return "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_bkzj_bk?" + query.Encode()
}

// sinaSectorHistoryURL 组装某个新浪板块（category）的多日资金流地址。
func sinaSectorHistoryURL(category string, days int) string {
	query := url.Values{}
	query.Set("page", "1")
	query.Set("num", strconv.Itoa(days))
	query.Set("sort", "opendate")
	query.Set("asc", "0")
	query.Set("bankuai", category)
	return "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssl_bkzj_zjlrqs?" + query.Encode()
}

// normalizeSinaCategory 校验新浪板块代码：小写字母/数字/下划线，1~40 位。
func normalizeSinaCategory(code string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(code))
	if v == "" {
		return "", fmt.Errorf("datasource: 板块代码不能为空")
	}
	if len(v) > 40 {
		return "", fmt.Errorf("datasource: 板块代码过长: %q", code)
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return "", fmt.Errorf("datasource: 板块代码只允许小写字母/数字/下划线，实际 %q", code)
	}
	return v, nil
}

// parseSinaFlowRank 解析新浪板块资金榜；无法解析的行跳过，全空时报错。
//
// 口径：MainNet 是「全部资金净额 netamount」，MainPct 是 ratioamount*100，
// ChangePct 是 avg_changeratio*100。Source 标 sina，前端据此知道这是新浪口径而不是旧东财主力净额。
func parseSinaFlowRank(body []byte) ([]contract.FundFlow, error) {
	var rows []sinaFlowRankRaw
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("datasource: 新浪板块资金榜格式异常: %w", err)
	}
	out := make([]contract.FundFlow, 0, len(rows))
	for _, row := range rows {
		code := strings.TrimSpace(row.Category)
		name := strings.TrimSpace(row.Name)
		if code == "" || name == "" {
			continue
		}
		out = append(out, contract.FundFlow{
			Code:      code,
			Name:      name,
			MainNet:   parseFloatString(row.NetAmount),
			MainPct:   parseFloatString(row.Ratio) * 100,
			ChangePct: parseFloatString(row.ChangeRate) * 100,
			Source:    sourceSina,
		})
	}
	if len(out) == 0 {
		return nil, ErrEmptyResponse
	}
	return out, nil
}

// rankFromSina 是资金榜的唯一链路：取回全量榜单后按方向本地重排并编号。
func (s *Service) rankFromSina(ctx context.Context, kind Kind, dir string, limit int) ([]contract.FundFlow, error) {
	cacheKey := "flow:rank:sina:" + string(kind)
	var rows []contract.FundFlow
	if cached, ok := s.cache.get(cacheKey); ok {
		if items, isItems := cached.([]contract.FundFlow); isItems {
			rows = items
		}
	}
	if rows == nil {
		body, err := s.client.get(ctx, request{
			source:  sourceSina,
			url:     sinaSectorRankURL(kind, sinaFlowMax),
			referer: sinaFlowReferer,
		})
		if err != nil {
			return nil, err
		}
		rows, err = parseSinaFlowRank(body)
		if err != nil {
			return nil, err
		}
		// 记住 category → 中文名：多日历史接口只回 category，没有名称，
		// 这里补一张 5 分钟缓存，保证图例/末端标签不显示 new_dzxx。
		for _, row := range rows {
			s.cache.put(sinaNameKey(row.Code), row.Name, ttlSinaFlow*5)
		}
		s.cache.put(cacheKey, rows, ttlSinaFlow)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if dir == "out" {
			return rows[i].MainNet < rows[j].MainNet
		}
		return rows[i].MainNet > rows[j].MainNet
	})
	out := make([]contract.FundFlow, 0, limit)
	for i, row := range rows {
		if len(out) >= limit {
			break
		}
		row.Kind = string(kind)
		row.Rank = i + 1
		out = append(out, row)
	}
	return out, nil
}

// parseSinaSectorHistory 解析新浪板块多日资金流（接口按日期倒序，这里反转成升序）。
func parseSinaSectorHistory(body []byte, code string, days int) (contract.FundFlowHistory, error) {
	var rows []sinaFlowHistoryRaw
	if err := json.Unmarshal(body, &rows); err != nil {
		return contract.FundFlowHistory{}, fmt.Errorf("datasource: 新浪板块资金流格式异常: %w", err)
	}
	points := make([]contract.FundFlowPoint, 0, len(rows))
	for _, row := range rows {
		date := strings.TrimSpace(row.OpenDate)
		if date == "" {
			continue
		}
		mainNet := parseFloatString(row.R0Net)
		if mainNet == 0 {
			// 新浪某些板块没有 r0_net，用全部净额兜底（前端会看到同一口径）。
			mainNet = parseFloatString(row.NetAmount)
		}
		points = append(points, contract.FundFlowPoint{
			Date:      date,
			MainNet:   mainNet,
			MainPct:   parseFloatString(row.Ratio) * 100,
			Close:     parseFloatString(row.AvgPrice),
			ChangePct: parseFloatString(row.ChangeRate) * 100,
		})
	}
	if len(points) == 0 {
		return contract.FundFlowHistory{}, ErrEmptyResponse
	}
	// 上游倒序 → 升序，保证与个股多日链路的时间轴一致。
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

// sectorHistorySina 取新浪板块多日资金流；名称由调用方从排名结果补。
func (s *Service) sectorHistorySina(ctx context.Context, category string, days int, name string) (contract.FundFlowHistory, error) {
	cacheKey := fmt.Sprintf("flow:sector:sina:%s:%d", category, days)
	if cached, ok := s.cache.get(cacheKey); ok {
		if history, isHistory := cached.(contract.FundFlowHistory); isHistory {
			return history, nil
		}
	}
	body, err := s.client.get(ctx, request{
		source:  sourceSina,
		url:     sinaSectorHistoryURL(category, days),
		referer: sinaFlowReferer,
		gbk:     true,
	})
	if err != nil {
		return contract.FundFlowHistory{}, err
	}
	history, err := parseSinaSectorHistory(body, category, days)
	if err != nil {
		return contract.FundFlowHistory{}, err
	}
	if strings.TrimSpace(name) == "" {
		if cached, ok := s.cache.get(sinaNameKey(category)); ok {
			if cachedName, isName := cached.(string); isName {
				name = cachedName
			}
		}
	}
	if strings.TrimSpace(name) != "" {
		history.Name = strings.TrimSpace(name)
	}
	s.cache.put(cacheKey, history, ttlSinaFlow)
	return history, nil
}

// sinaNameKey 是 category → 中文名的缓存键。
func sinaNameKey(category string) string {
	return "flow:sina:name:" + strings.ToLower(strings.TrimSpace(category))
}

// parseFloatString 解析新浪返回的数字字符串；空/非法返回 0。
func parseFloatString(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return f
}
