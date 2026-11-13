package datasource

// 龙虎榜：解析同花顺 ifmarket/lhbtable/report/<date>/1 的 HTML 表格。
//
// 上游返回的是 **GBK 编码**的整页 HTML（实测 2026-11-11 单日 65KB），表格长这样：
//
//	<table class="m-table">  <- 表头（代码/名称/现价/涨跌幅/成交金额/净买入额）
//	<table class="m-table">  <- 数据体，每行一个 <tr>，7 个 <td>
//
//	<td>3日标记</td><td>000026</td><td>飞亚达</td><td>23.16</td>
//	<td>10.02%</td><td>6.17亿</td><td>1.17亿</td>
//
// 三个必须处理的坑：
//  1. 编码：先按 UTF-8 解，失败再按 GBK 解（simplifiedchinese.GBK）。不能只用一种：
//     同一天里「纯 ASCII 的表头页」和「带中文的数据页」都可能出现。
//  2. 金额量级：「1.17亿 / 5830.19万」要换算成元，且带负号（-3749.33万）。
//  3. 首列（3日标记）可能是空的，也可能是 <label>3日</label>，不影响后面的列位置。
//
// 这张表**没有**上榜原因，也没有买入/卖出拆分，所以 LongHuRow 只填
// Code/Name/Price/ChangePct/NetBuy/Date；Reason 留空，Buy/Sell 留 0。
// 这是上游能力的边界，不是解析漏项 —— 上层展示时不要在缺字段上编原因。

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// thsLongHuReportURL 拼某交易日的龙虎榜地址（page 从 1 开始）。
func thsLongHuReportURL(date string, page int) string {
	return fmt.Sprintf("https://data.10jqka.com.cn/ifmarket/lhbtable/report/%s/%d", date, page)
}

// fetchMarketLongHu 抓当日龙虎榜；当日无数据时回退最近交易日（最多 5 个交易日）。
//
// 日期回退语义与旧实现一致：先试今天（自然日），没有数据就往前找交易日，
// 最多回看 thsLongHuMaxTradingDays 个交易日（跳过周末）。非交易日上游返回空表格。
func (s *Service) fetchMarketLongHu(ctx context.Context) ([]LongHuRow, error) {
	var lastErr error
	tried := 0
	for offset := 0; offset < 32 && tried < thsLongHuMaxTradingDays; offset++ {
		day := s.now().AddDate(0, 0, -offset)
		if isWeekend(day.Weekday()) {
			continue
		}
		tried++
		date := day.Format("2006-01-02")
		rows, err := s.fetchMarketLongHuDate(ctx, date)
		if err != nil {
			lastErr = err
			continue
		}
		if len(rows) > 0 {
			return rows, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: 龙虎榜最近 %d 个交易日都没有数据", ErrNoData, thsLongHuMaxTradingDays)
}

// isWeekend 判断是否周末（周末上游一定没有龙虎榜，直接跳过省一次请求）。
func isWeekend(day time.Weekday) bool {
	return day == time.Saturday || day == time.Sunday
}

// fetchMarketLongHuDate 抓指定交易日并聚合。
func (s *Service) fetchMarketLongHuDate(ctx context.Context, date string) ([]LongHuRow, error) {
	body, err := s.client.get(ctx, request{
		source:  sourceTHS,
		url:     thsLongHuReportURL(date, 1),
		referer: "https://data.10jqka.com.cn/market/longhu/",
	})
	if err != nil {
		return nil, err
	}
	return parseTHSLongHu(body, date)
}

// parseTHSLongHu 解析同花顺龙虎榜表格并按代码聚合。
//
// 同一代码可能因多个上榜类型（普通 / 3日）重复出现：这里按代码合并，
// 净买入额累加（同花顺每天每只票的多个上榜原因对应不同的金额区间，加总才等于当日该票总净买入）。
// 价格取首个非零值，涨跌幅也同样处理。
func parseTHSLongHu(body []byte, date string) ([]LongHuRow, error) {
	text, err := decodeLongHuHTML(body)
	if err != nil {
		return nil, err
	}
	records := parseLongHuRows(text, date)
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: 同花顺龙虎榜 %s 没有数据", ErrEmptyResponse, date)
	}
	return aggregateLongHu(records), nil
}

// decodeLongHuHTML 先按 UTF-8 试，非法再按 GBK 解。
// 上游的 Content-Type 不可靠（实测返回 ISO-8859 声明但内容是 GBK），只能按字节判定。
func decodeLongHuHTML(body []byte) (string, error) {
	if len(body) == 0 {
		return "", ErrEmptyResponse
	}
	if utf8Valid(body) {
		return string(body), nil
	}
	decoded, err := decodeGBK(body)
	if err != nil {
		return "", fmt.Errorf("datasource: 同花顺龙虎榜 GBK 解码失败: %w", err)
	}
	return decoded, nil
}

// parseLongHuRows 逐 <tr> 提取数据行。
//
// 只认「7 个 <td> 且第 2 列是 6 位数字代码」的行：表头行用的是 <th>，
// 而页面里还有别的表格片段（分页、说明），靠这个形状过滤掉最稳。
func parseLongHuRows(text, date string) []LongHuRow {
	rows := make([]LongHuRow, 0, 64)
	for _, tr := range splitByTag(text, "tr") {
		cells := splitCells(tr)
		if len(cells) < 7 {
			continue
		}
		code := stripTags(cells[1])
		if !isAShareCode(code) {
			continue
		}
		name := stripTags(cells[2])
		if name == "" {
			continue
		}
		rows = append(rows, LongHuRow{
			Code:      code,
			Name:      name,
			Price:     parseNumOrZero(stripTags(cells[3])),
			ChangePct: parsePctOrZero(stripTags(cells[4])),
			// cells[5] 是成交金额：LongHuRow 没有对应字段，丢弃。
			NetBuy: parseCnAmount(stripTags(cells[6])),
			Date:   date,
		})
	}
	return rows
}

// splitByTag 取出 <tag ...>...</tag> 之间的内容（大小写不敏感，不处理嵌套同名标签 ——
// 同花顺这张表里 tr/td 不会嵌套同名标签，够用）。
func splitByTag(text, tag string) []string {
	lower := strings.ToLower(text)
	open := "<" + tag
	closeTag := "</" + tag + ">"
	out := make([]string, 0, 64)
	position := 0
	for {
		start := strings.Index(lower[position:], open)
		if start < 0 {
			break
		}
		start += position
		// 跳过 <tag ... > 到 '>'，避免把 <tr> 与 <track> 混淆。
		head := strings.IndexByte(lower[start:], '>')
		if head < 0 {
			break
		}
		contentStart := start + head + 1
		end := strings.Index(lower[contentStart:], closeTag)
		if end < 0 {
			break
		}
		end += contentStart
		out = append(out, text[contentStart:end])
		position = end + len(closeTag)
	}
	return out
}

// splitCells 取一行里的所有 <td> 内容。
func splitCells(row string) []string {
	return splitByTag(row, "td")
}

// stripTags 去掉所有 HTML 标签并压缩空白（单元格里常有 <a>/<label> 包裹名称）。
func stripTags(cell string) string {
	var builder strings.Builder
	depth := 0
	for _, r := range cell {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				builder.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// isAShareCode 判断是否是 6 位 A 股代码（排除 B 股 200/900 开头与可转债等）。
func isAShareCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	if strings.HasPrefix(code, "200") || strings.HasPrefix(code, "900") {
		return false
	}
	switch code[0] {
	case '0', '3', '4', '6', '8', '9':
		return true
	}
	return false
}

// parseNumOrZero 解析价格/普通数值；失败返回 0。
func parseNumOrZero(text string) float64 {
	if v, ok := parseNum(strings.TrimSpace(text)); ok {
		return v
	}
	return 0
}

// parsePctOrZero 解析 "10.02%" / "-5.07%" 这类带百分号的文本；失败返回 0。
func parsePctOrZero(text string) float64 {
	cleaned := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "%"))
	return parseNumOrZero(cleaned)
}

// parseCnAmount 把「1.17亿 / 5830.19万 / -3749.33万 / 1234」统一换算成元。
//
// 单位：亿 = 1e8、万 = 1e4；没有单位时按「元」原样返回（同花顺对小于万元的金额不给单位）。
// 负号保留：净买入为负表示净卖出。
func parseCnAmount(text string) float64 {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" || cleaned == "-" || cleaned == "--" {
		return 0
	}
	multiplier := 1.0
	switch {
	case strings.HasSuffix(cleaned, "亿"):
		multiplier = 1e8
		cleaned = strings.TrimSuffix(cleaned, "亿")
	case strings.HasSuffix(cleaned, "万"):
		multiplier = 1e4
		cleaned = strings.TrimSuffix(cleaned, "万")
	case strings.HasSuffix(cleaned, "元"):
		cleaned = strings.TrimSuffix(cleaned, "元")
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(cleaned), 64)
	if err != nil {
		return 0
	}
	return value * multiplier
}

// aggregateLongHu 按代码合并重复上榜，并按净买入绝对值降序。
func aggregateLongHu(records []LongHuRow) []LongHuRow {
	order := make([]string, 0, len(records))
	byCode := make(map[string]*LongHuRow, len(records))
	for _, row := range records {
		code := strings.TrimSpace(row.Code)
		if code == "" {
			continue
		}
		if existing, ok := byCode[code]; ok {
			existing.NetBuy += row.NetBuy
			if existing.Price == 0 {
				existing.Price = row.Price
			}
			if existing.ChangePct == 0 {
				existing.ChangePct = row.ChangePct
			}
			continue
		}
		copy := row
		byCode[code] = &copy
		order = append(order, code)
	}
	out := make([]LongHuRow, 0, len(order))
	for _, code := range order {
		out = append(out, *byCode[code])
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := math.Abs(out[i].NetBuy), math.Abs(out[j].NetBuy)
		if left != right {
			return left > right
		}
		return out[i].Code < out[j].Code
	})
	return out
}
