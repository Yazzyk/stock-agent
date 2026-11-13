package datasource

// 官方 API 通用查询层：端点目录驱动 + 参数校验 + 信封解包 + 输出裁剪。
//
// 这一层不针对具体业务域，负责把「目录里的端点 + 调用方参数」变成一次
// 合规的 GET：
//   - 未知参数直接报错（避免把 period 写成 periods 后误判为「无数据」）；
//   - 必填参数缺失在本地就返回，不浪费一次配额；
//   - 单次代码数超过目录上限时按上限切分（同一查询条件，多批合并）；
//   - 输出按 limit 裁剪，长文本截断，避免把几十万字符塞进 LLM 上下文。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	// fuyaoQueryDefaultLimit 是通用查询默认返回条数（不传 limit 时）。
	fuyaoQueryDefaultLimit = 30
	// fuyaoQueryMaxLimit 是通用查询单次最多返回条数（硬上限，防止上下文爆炸）。
	fuyaoQueryMaxLimit = 200
	// fuyaoFieldTextCap 是长文本字段（content/summary/analysis 等）的截断长度。
	fuyaoFieldTextCap = 600
)

// FuyaoCatalog 返回官方端点目录（离线常量）。
func (s *Service) FuyaoCatalog(context.Context) []contract.FuyaoEndpoint {
	return fuyaoCatalog()
}

// FuyaoQuery 执行一次通用查询。
//
// 行为：
//  1. 从目录取端点；未知端点返回 contract.ErrFuyaoUnsupported。
//  2. 校验参数（未知键、必填缺失、纯空白值），必要时按 Limit 切分 thscodes。
//  3. 逐个调用官方 API，合并 item；任一批权限不足（2003/2004）时返回带 Permission 的空结果。
//  4. 按 query.Limit 裁剪记录条数，并对长文本字段截断。
func (s *Service) FuyaoQuery(ctx context.Context, query contract.FuyaoQuery) (contract.FuyaoData, error) {
	ep, ok := fuyaoEndpointByID(strings.TrimSpace(query.Endpoint))
	if !ok {
		return contract.FuyaoData{}, fmt.Errorf("%w: %s（可用端点见 docs/FUYAO-API.md 或 fuyao.catalog）", contract.ErrFuyaoUnsupported, query.Endpoint)
	}
	if !s.fuyaoAvailable() {
		return contract.FuyaoData{}, fmt.Errorf("datasource: 官方 API 未启用（未配置 API Key）")
	}
	params, err := fuyaoValidateParams(ep, query.Params)
	if err != nil {
		return contract.FuyaoData{}, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = fuyaoQueryDefaultLimit
	}
	if limit > fuyaoQueryMaxLimit {
		limit = fuyaoQueryMaxLimit
	}

	// 原生批量端点：按目录上限把 thscodes 切分，其余参数整批共用。
	batches, err := fuyaoBatchParams(ep, params)
	if err != nil {
		return contract.FuyaoData{}, err
	}
	out := contract.FuyaoData{
		Endpoint: ep.ID, Path: ep.Path, Title: ep.Title, Group: ep.Group,
		Params: map[string]string{},
	}
	for k, v := range params {
		out.Params[k] = strings.Join(v, ",")
	}
	var (
		raws     []json.RawMessage
		firstErr error
	)
	for _, batch := range batches {
		var data json.RawMessage
		if err := s.fuyaoGet(ctx, ep.Path, batch, &data); err != nil {
			if errors.Is(err, contract.ErrFuyaoPermission) {
				out.Permission = "client"
				out.Note = "该能力为同花顺 AI 客户端专用（官方 code=2004）；外部 API Key 无权访问，已降级为空结果。"
				out.Count = 0
				return out, nil
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		raws = append(raws, data)
	}
	if len(raws) == 0 && firstErr != nil {
		return contract.FuyaoData{}, firstErr
	}

	items, raw, timestamp, total := fuyaoExtract(raws)
	out.Items = items
	out.Raw = raw
	out.Timestamp = timestamp
	out.Count = len(items)
	if total > out.Count {
		out.Count = total
	}
	if out.Count == 0 && out.Raw == nil {
		out.Note = "官方返回成功但没有记录（合法空结果）；请核对日期/代码是否在可用范围内。"
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Truncated = true
	}
	for i := range out.Items {
		out.Items[i] = fuyaoTrimItem(out.Items[i])
	}
	if out.Raw != nil {
		out.Raw = fuyaoTrimItem(out.Raw)
	}
	if out.Truncated {
		out.Note = strings.TrimSpace(out.Note + " 已按 limit 截断，需要更多请分页或缩小范围。")
	}
	return out, nil
}

// fuyaoQueryChecked 是强类型域方法的通用入口：在 FuyaoQuery 之上把
// permission=client 转成 contract.ErrFuyaoPermission，避免调用方拿到空数组
// 却误以为「今天没有数据」。通用视图方法（返回 FuyaoData）不走这里。
func (s *Service) fuyaoQueryChecked(ctx context.Context, query contract.FuyaoQuery) (contract.FuyaoData, error) {
	data, err := s.FuyaoQuery(ctx, query)
	if err != nil {
		return data, err
	}
	if data.Permission != "" {
		return data, contract.ErrFuyaoPermission
	}
	return data, nil
}

// fuyaoValidateParams 校验入参：未知键、必填缺失、去空白；返回可直接编码的 url.Values。
func fuyaoValidateParams(ep contract.FuyaoEndpoint, in map[string]string) (url.Values, error) {
	known := make(map[string]contract.FuyaoParam, len(ep.Params))
	for _, p := range ep.Params {
		known[p.Name] = p
	}
	values := url.Values{}
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := known[k]; !ok {
			return nil, fmt.Errorf("datasource: 端点 %s 不支持参数 %q（可用参数：%s）", ep.ID, k, fuyaoParamNames(ep))
		}
		v = strings.TrimSpace(v)
		if v != "" {
			values.Set(k, v)
		}
	}
	for _, p := range ep.Params {
		if !p.Required {
			continue
		}
		if strings.TrimSpace(values.Get(p.Name)) == "" {
			return nil, fmt.Errorf("datasource: 端点 %s 缺少必填参数 %s（%s）", ep.ID, p.Name, p.Desc)
		}
	}
	// 没传的默认值让官方自己兜底；只有调用方显式传空串时才视为未传。
	return values, nil
}

// fuyaoParamNames 返回端点参数名的可读列表（报错与工具提示共用）。
func fuyaoParamNames(ep contract.FuyaoEndpoint) string {
	if len(ep.Params) == 0 {
		return "无"
	}
	names := make([]string, 0, len(ep.Params))
	for _, p := range ep.Params {
		name := p.Name
		if p.Required {
			name += "*"
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// fuyaoBatchParams 按端点 Limit 切分 thscodes；不涉及批量代码时原样返回一批。
func fuyaoBatchParams(ep contract.FuyaoEndpoint, params url.Values) ([]url.Values, error) {
	key := "thscodes"
	if params.Get(key) == "" {
		// 少量端点用 thscode 但支持逗号（指数反查/集合竞价），同样按上限切分。
		if params.Get("thscode") == "" || ep.Limit <= 0 {
			return []url.Values{params}, nil
		}
		key = "thscode"
	}
	if ep.Limit <= 0 {
		return []url.Values{params}, nil
	}
	tokens := splitTrim(params.Get(key))
	if len(tokens) <= ep.Limit {
		return []url.Values{params}, nil
	}
	out := make([]url.Values, 0, (len(tokens)+ep.Limit-1)/ep.Limit)
	for start := 0; start < len(tokens); start += ep.Limit {
		end := start + ep.Limit
		if end > len(tokens) {
			end = len(tokens)
		}
		chunk := url.Values{}
		for k, v := range params {
			chunk[k] = append([]string(nil), v...)
		}
		chunk.Set(key, strings.Join(tokens[start:end], ","))
		out = append(out, chunk)
	}
	return out, nil
}

// splitTrim 逗号切分并去掉空白/空段（官方对空段返回 1002，本地先清干净）。
func splitTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// fuyaoExtract 从一批 data JSON 里抽取记录、对象、时间戳与总数。
//
// 官方容器形态（实测）：
//   - {timestamp,total,item:[...]}          行情/特色数据
//   - {timestamp,item:[...]}                多数列表
//   - {items:[...]}                          资讯域
//   - {item:[...], pagination:{total}}       分页池
//   - [... ]                                 QDII 分类等少数端点 data 直接是数组
//   - {auction_phase, data_status, item:[]}  竞价
func fuyaoExtract(raws []json.RawMessage) ([]map[string]any, map[string]any, int64, int) {
	var (
		items     []map[string]any
		firstObj  map[string]any
		timestamp int64
		total     int
	)
	for _, raw := range raws {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		if raw[0] == '[' {
			var arr []map[string]any
			if json.Unmarshal(raw, &arr) == nil {
				items = append(items, arr...)
			}
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			continue
		}
		if firstObj == nil {
			firstObj = obj
		}
		if ts := jsonInt64(obj["timestamp"]); ts > 0 && timestamp == 0 {
			timestamp = ts
		}
		if ts := jsonInt64(obj["date_ms"]); ts > 0 && timestamp == 0 {
			timestamp = ts
		}
		if pagination, ok := obj["pagination"].(map[string]any); ok {
			if n := int(jsonInt64(pagination["total"])); n > total {
				total = n
			}
		}
		if n := int(jsonInt64(obj["total"])); n > total {
			total = n
		}
		if n := int(jsonInt64(obj["stock_count"])); n > total {
			total = n
		}
		// 优先 item，其次 items/stock_items；dates 是字符串数组，转成 {date:...}。
		for _, key := range []string{"item", "items", "stock_items"} {
			if arr, ok := obj[key].([]any); ok {
				items = append(items, mapsOf(arr)...)
				break
			}
		}
		if dates, ok := obj["dates"].([]any); ok && len(items) == 0 {
			for _, d := range dates {
				if s, ok := d.(string); ok {
					items = append(items, map[string]any{"date": s})
				}
			}
		}
	}
	// 对象型 data：没有列表字段时整体作为 raw 返回（如资金流快照、基金持仓汇总）。
	if len(items) == 0 && len(raws) == 1 && firstObj != nil {
		hasList := false
		for _, key := range []string{"item", "items", "stock_items", "dates"} {
			if _, ok := firstObj[key]; ok {
				hasList = true
				break
			}
		}
		if !hasList {
			return nil, firstObj, timestamp, total
		}
	}
	return items, nil, timestamp, total
}

// mapsOf 把 []any 转成 []map[string]any（非对象项丢弃，避免污染表格）。
func mapsOf(arr []any) []map[string]any {
	out := make([]map[string]any, 0, len(arr))
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// decodeFuyaoItems 把通用 map 记录转成强类型 DTO（字段名与官方 JSON 一致）。
// 单个字段类型不符时整条跳过并返回错误，调用方据此决定是否降级为原始 Items。
func decodeFuyaoItems[T any](items []map[string]any) ([]T, error) {
	out := make([]T, 0, len(items))
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		var row T
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// mapFloat 从 map 中安全取 float64（兼容 number/string/null）。
func mapFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f
	}
	return 0
}

// mapString 从 map 中安全取字符串。
func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// mapObj 从 map 中安全取子对象。
func mapObj(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

// jsonInt64 宽容地把 JSON number/string 转成 int64。
func jsonInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

// fuyaoTrimItem 裁剪单条记录：长文本截断、超深嵌套只保留一层标量摘要。
func fuyaoTrimItem(item map[string]any) map[string]any {
	out := make(map[string]any, len(item))
	keys := make([]string, 0, len(item))
	for k := range item {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := item[k]
		switch val := v.(type) {
		case string:
			if len([]rune(val)) > fuyaoFieldTextCap {
				val = string([]rune(val)[:fuyaoFieldTextCap]) + "…（已截断）"
			}
			out[k] = val
		case []any:
			// 数组字段（tags、index_list 等）保留前 20 项，避免一条记录几千字符。
			if len(val) > 20 {
				out[k] = append(append([]any{}, val[:20]...), "…（已截断）")
			} else {
				out[k] = val
			}
		default:
			out[k] = v
		}
	}
	return out
}
