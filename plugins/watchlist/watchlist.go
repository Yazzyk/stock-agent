// Package watchlist 提供本地自选股：面板、工具与同花顺三级降级同步入口。
//
// 持久化约定：自选条目写在 Store 的 watchlist bucket，键布局与 internal/datasource
// 完全一致（item/<市场+代码> 存条目 JSON、meta 存同步元信息），这样 DataService.Watchlist()
// 读到的内容和本插件写入的内容永远是同一份数据，不存在「插件自己的自选」和
// 「数据层的自选」两套真相。
package watchlist

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

const (
	bucketWatchlist  = "watchlist"
	prefixWatchItem  = "item/"
	keyWatchMeta     = "meta"
	defaultGroupName = "自选股"
	pluginID         = "watchlist"
)

func init() { all.Register(New) }

// New 是插件工厂（签名由 docs/API.md §8 冻结）。
func New() plugin.Plugin { return &Plugin{} }

// Plugin 是自选股插件。
type Plugin struct {
	plugin.Base
	ctx *plugin.Context
}

var _ plugin.Plugin = (*Plugin)(nil)

// Manifest 描述插件身份与贡献点。
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "自选股",
		Version:     "1.0.0",
		Description: "本地自选股管理：列表、分组、增删，以及同花顺客户端/网页端/手工导入三级同步",
		Tags:        []string{"watch", "market"},
		Provides: []string{
			"method:list", "method:sync", "method:add", "method:remove", "method:groups",
			"method:check", "method:saveCookie", "method:captureCookie", "method:privacy",
			"tool:watch.list", "tool:watch.sync", "tool:market.quote", "tool:market.search",
			"ui:watchlist",
		},
	}
}

// Activate 注册方法、工具与面板。
func (p *Plugin) Activate(ctx *plugin.Context) error {
	p.ctx = ctx
	methods := []plugin.Method{
		{Name: "list", Description: "返回自选条目与分组", Fn: p.methodList},
		{Name: "sync", Description: "从同花顺同步自选（三级降级）", Fn: p.methodSync},
		{Name: "add", Description: "添加自选", Fn: p.methodAdd},
		{Name: "remove", Description: "移除自选", Fn: p.methodRemove},
		{Name: "groups", Description: "返回分组列表", Fn: p.methodGroups},
		{Name: "check", Description: "同步前置诊断（只读，不触发同步）", Fn: p.methodCheck},
		{Name: "saveCookie", Description: "校验并保存同花顺网页登录态", Fn: p.methodSaveCookie},
		{Name: "captureCookie", Description: "尝试从本机浏览器读取登录态", Fn: p.methodCaptureCookie},
		{Name: "privacy", Description: "完全磁盘访问设置入口", Fn: p.methodPrivacy},
	}
	for _, m := range methods {
		if err := ctx.Methods.Add(m); err != nil {
			return err
		}
	}

	tools := []plugin.Tool{
		{
			Name:    "watch.list",
			Title:   "读取自选股",
			Summary: "查看当前自选股清单与分组，用户问“我的自选”时用",
			Tags:    []string{"watch", "market"},
			Risk:    plugin.RiskRead,
			Schema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Run:     p.toolList,
		},
		{
			Name:    "watch.sync",
			Title:   "同步同花顺自选",
			Summary: "从本机同花顺或网页端同步自选股，需要最新自选时用",
			Tags:    []string{"watch"},
			Risk:    plugin.RiskNetwork,
			Schema:  json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Run:     p.toolSync,
		},
		{
			Name:    "market.quote",
			Title:   "实时行情快照",
			Summary: "按代码批量取现价与涨跌幅，判断当日强弱时用",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"codes":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":20,"description":"股票代码，如 600000、sh000001"}},
				"required":["codes"],
				"additionalProperties":false
			}`),
			Run: p.toolQuote,
		},
		{
			Name:    "market.search",
			Title:   "搜索股票",
			Summary: "按名称或代码模糊搜标的，用户只说名字时用",
			Tags:    []string{"market"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"keyword":{"type":"string","minLength":1,"description":"名称或代码片段"},
					"limit":{"type":"integer","minimum":1,"maximum":20,"description":"返回条数，默认 10"}
				},
				"required":["keyword"],
				"additionalProperties":false
			}`),
			Run: p.toolSearch,
		},
	}
	for _, t := range tools {
		if err := ctx.Tools.Add(t); err != nil {
			return err
		}
	}

	return ctx.UI.Add(plugin.UISlot{
		ID:        "watchlist",
		Title:     "自选股",
		Icon:      "star",
		Group:     "行情",
		Component: "watchlist",
		Order:     10,
	})
}

// ---------- 方法 ----------

type listReply struct {
	Items     []contract.WatchItem `json:"items"`
	Groups    []string             `json:"groups"`
	UpdatedAt string               `json:"updatedAt"`
	Source    string               `json:"source"`
}

func (p *Plugin) methodList(ctx context.Context, _ json.RawMessage) (any, error) {
	items, err := p.ctx.Data.Watchlist(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取自选失败: %w", err)
	}
	items = p.enrichNames(ctx, items)
	meta := p.loadMeta()
	return listReply{
		Items:     items,
		Groups:    groupsOf(items, meta.Groups),
		UpdatedAt: meta.UpdatedAt,
		Source:    meta.Source,
	}, nil
}

func (p *Plugin) methodSync(ctx context.Context, _ json.RawMessage) (any, error) {
	result, err := p.ctx.Data.SyncTHSWatchlist(ctx)
	if err != nil {
		return nil, fmt.Errorf("同步自选失败: %w", err)
	}
	p.ctx.Events.Publish(plugin.Event{Topic: "watch.updated", Payload: mustJSON(map[string]any{
		"success": result.Success, "source": result.Source, "added": result.Added, "removed": result.Removed,
	})})
	return result, nil
}

type addArgs struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

func (p *Plugin) methodAdd(ctx context.Context, payload json.RawMessage) (any, error) {
	var args addArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code, err := normalizeCode(args.Code)
	if err != nil {
		return nil, err
	}
	group := strings.TrimSpace(args.Group)
	if group == "" {
		group = defaultGroupName
	}

	items, err := p.loadItems(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	order := 0
	found := false
	for i := range items {
		existing, normErr := normalizeCode(items[i].Code)
		if normErr != nil {
			continue
		}
		if existing.ID() == code.ID() {
			items[i].Group = group
			items[i].Order = 0
			if name != "" {
				items[i].Name = name
			}
			found = true
			continue
		}
		if items[i].Group == group && items[i].Order >= order {
			order = items[i].Order + 1
		}
	}
	if !found {
		items = append(items, contract.WatchItem{
			Code:   code.Symbol,
			Name:   name,
			Market: code.Market,
			Group:  group,
			Order:  order,
			Source: "manual",
		})
	}
	if name == "" {
		items = p.enrichNames(ctx, items)
	}
	if err := p.saveItems(items); err != nil {
		return nil, err
	}
	return addReply{Items: items}, nil
}

type addReply struct {
	Items []contract.WatchItem `json:"items"`
}

type removeArgs struct {
	Code string `json:"code"`
}

func (p *Plugin) methodRemove(ctx context.Context, payload json.RawMessage) (any, error) {
	var args removeArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	code, err := normalizeCode(args.Code)
	if err != nil {
		return nil, err
	}
	items, err := p.loadItems(ctx)
	if err != nil {
		return nil, err
	}
	kept := make([]contract.WatchItem, 0, len(items))
	for _, item := range items {
		existing, normErr := normalizeCode(item.Code)
		if normErr == nil && existing.ID() == code.ID() {
			continue
		}
		kept = append(kept, item)
	}
	if err := p.saveItems(kept); err != nil {
		return nil, err
	}
	return addReply{Items: kept}, nil
}

type groupsReply struct {
	Groups []string `json:"groups"`
}

func (p *Plugin) methodGroups(ctx context.Context, _ json.RawMessage) (any, error) {
	items, err := p.loadItems(ctx)
	if err != nil {
		return nil, err
	}
	meta := p.loadMeta()
	return groupsReply{Groups: groupsOf(items, meta.Groups)}, nil
}

// ---------- 工具 ----------

func (p *Plugin) toolList(ctx context.Context, _ json.RawMessage) (any, error) {
	reply, err := p.methodList(ctx, nil)
	if err != nil {
		return nil, err
	}
	view := reply.(listReply)
	return map[string]any{"items": view.Items, "groups": view.Groups, "count": len(view.Items)}, nil
}

func (p *Plugin) toolSync(ctx context.Context, _ json.RawMessage) (any, error) {
	return p.methodSync(ctx, nil)
}

type quoteArgs struct {
	Codes []string `json:"codes"`
}

func (p *Plugin) toolQuote(ctx context.Context, payload json.RawMessage) (any, error) {
	var args quoteArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	if len(args.Codes) == 0 {
		return nil, fmt.Errorf("codes 不能为空")
	}
	quotes, err := p.ctx.Data.Quote(ctx, args.Codes)
	if err != nil && len(quotes) == 0 {
		return nil, fmt.Errorf("取行情失败: %w", err)
	}
	return quotes, nil
}

type searchArgs struct {
	Keyword string `json:"keyword"`
	Limit   int    `json:"limit"`
}

func (p *Plugin) toolSearch(ctx context.Context, payload json.RawMessage) (any, error) {
	var args searchArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	keyword := strings.TrimSpace(args.Keyword)
	if keyword == "" {
		return nil, fmt.Errorf("keyword 不能为空")
	}
	refs, err := p.ctx.Data.Search(ctx, keyword)
	if err != nil {
		return nil, fmt.Errorf("搜索失败: %w", err)
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	if len(refs) > limit {
		refs = refs[:limit]
	}
	return refs, nil
}

// ---------- 持久化 ----------

func (p *Plugin) loadItems(ctx context.Context) ([]contract.WatchItem, error) {
	items, err := p.ctx.Data.Watchlist(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取自选失败: %w", err)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Group != items[j].Group {
			return items[i].Group < items[j].Group
		}
		if items[i].Order != items[j].Order {
			return items[i].Order < items[j].Order
		}
		return items[i].Code < items[j].Code
	})
	return items, nil
}

// saveItems 全量覆盖 store（与 datasource 相同的键布局），并同步 groups 元信息。
func (p *Plugin) saveItems(items []contract.WatchItem) error {
	if p.ctx.Store == nil {
		return fmt.Errorf("本地存储不可用，无法保存自选")
	}
	keys := make(map[string]bool, len(items))
	order := 0
	for i := range items {
		code, err := normalizeCode(items[i].Code)
		if err != nil {
			continue
		}
		item := items[i]
		item.Code = code.Symbol
		item.Market = code.Market
		item.Order = order
		if item.Group == "" {
			item.Group = defaultGroupName
		}
		order++
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		key := prefixWatchItem + code.ID()
		keys[key] = true
		if err := p.ctx.Store.Put(bucketWatchlist, key, raw); err != nil {
			return fmt.Errorf("保存自选失败: %w", err)
		}
	}
	existing, err := p.ctx.Store.List(bucketWatchlist, prefixWatchItem)
	if err == nil {
		for _, pair := range existing {
			if keys[pair.Key] {
				continue
			}
			if err := p.ctx.Store.Delete(bucketWatchlist, pair.Key); err != nil {
				return fmt.Errorf("清理自选失败: %w", err)
			}
		}
	}
	meta := watchMeta{
		UpdatedAt: p.ctx.Clock().Format("2006-01-02T15:04:05-07:00"),
		Source:    "manual",
		Groups:    groupsOf(items, nil),
	}
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return p.ctx.Store.Put(bucketWatchlist, keyWatchMeta, rawMeta)
}

type watchMeta struct {
	UpdatedAt string   `json:"updatedAt"`
	Source    string   `json:"source"`
	Groups    []string `json:"groups"`
}

func (p *Plugin) loadMeta() watchMeta {
	if p.ctx.Store == nil {
		return watchMeta{}
	}
	raw, err := p.ctx.Store.Get(bucketWatchlist, keyWatchMeta)
	if err != nil {
		return watchMeta{}
	}
	var meta watchMeta
	if json.Unmarshal(raw, &meta) != nil {
		return watchMeta{}
	}
	return meta
}

// ---------- 辅助 ----------

// enrichNames 给缺名字的条目补名字：一次批量行情，失败就保持空名字（不阻断列表）。
func (p *Plugin) enrichNames(ctx context.Context, items []contract.WatchItem) []contract.WatchItem {
	codes := make([]string, 0, len(items))
	indexes := make([]int, 0, len(items))
	for i, item := range items {
		if item.Name != "" {
			continue
		}
		// DataService 对 >10 只代码走批量快照（一次请求），所以这里可以放宽到 50 只
		if len(codes) >= 50 {
			break
		}
		// 必须带上市场前缀：000001 在 sh 是上证指数、在 sz 是平安银行，裸代码会取错名称
		codes = append(codes, string(item.Market)+item.Code)
		indexes = append(indexes, i)
	}
	if len(codes) == 0 {
		return items
	}
	quotes, err := p.ctx.Data.Quote(ctx, codes)
	if err != nil && len(quotes) == 0 {
		p.ctx.Log.Debug("补名字失败，保留空名字", "error", err)
		return items
	}
	byCode := make(map[string]string, len(quotes))
	for _, quote := range quotes {
		byCode[string(quote.Market)+quote.Code] = quote.Name
	}
	for _, index := range indexes {
		if name, ok := byCode[string(items[index].Market)+items[index].Code]; ok && name != "" {
			items[index].Name = name
		}
	}
	return items
}

// groupsOf 合并「元信息里的分组」与「条目里出现的分组」，保持顺序并去重。
func groupsOf(items []contract.WatchItem, metaGroups []string) []string {
	seen := make(map[string]bool, len(metaGroups)+4)
	out := make([]string, 0, len(metaGroups)+4)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range metaGroups {
		add(name)
	}
	for _, item := range items {
		add(item.Group)
	}
	return out
}

type normalizedCode struct {
	Symbol string
	Market contract.Market
}

func (c normalizedCode) ID() string { return string(c.Market) + c.Symbol }

// normalizeCode 归一代码写法（600000 / sh600000 / 1.600000 / sz000001 …），只接受沪深北 A 股与指数。
func normalizeCode(raw string) (normalizedCode, error) {
	text := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), " ", ""))
	if text == "" {
		return normalizedCode{}, fmt.Errorf("代码不能为空")
	}
	if strings.HasPrefix(text, "s_") {
		text = strings.TrimPrefix(text, "s_")
	}
	market := contract.Market("")
	switch {
	case strings.HasPrefix(text, "sh"):
		market, text = contract.MarketSH, strings.TrimPrefix(text, "sh")
	case strings.HasPrefix(text, "sz"):
		market, text = contract.MarketSZ, strings.TrimPrefix(text, "sz")
	case strings.HasPrefix(text, "bj"):
		market, text = contract.MarketBJ, strings.TrimPrefix(text, "bj")
	}
	if idx := strings.Index(text, "."); idx > 0 && market == "" {
		head, tail := text[:idx], text[idx+1:]
		switch head {
		case "1":
			market, text = contract.MarketSH, tail
		case "0":
			market, text = contract.MarketSZ, tail
		default:
			return normalizedCode{}, fmt.Errorf("无法识别的代码: %s", raw)
		}
	}
	if len(text) != 6 {
		return normalizedCode{}, fmt.Errorf("代码需要 6 位数字: %s", raw)
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return normalizedCode{}, fmt.Errorf("代码需要 6 位数字: %s", raw)
		}
	}
	if market == "" {
		switch {
		case strings.HasPrefix(text, "43"), strings.HasPrefix(text, "83"),
			strings.HasPrefix(text, "87"), strings.HasPrefix(text, "92"):
			market = contract.MarketBJ
		case strings.HasPrefix(text, "6"), strings.HasPrefix(text, "9"), strings.HasPrefix(text, "5"):
			market = contract.MarketSH
		default:
			market = contract.MarketSZ
		}
	}
	return normalizedCode{Symbol: text, Market: market}, nil
}

func decodeArgs(raw json.RawMessage, dst any) error {
	payload := strings.TrimSpace(string(raw))
	if payload == "" {
		payload = "{}"
	}
	if err := json.Unmarshal([]byte(payload), dst); err != nil {
		return fmt.Errorf("参数格式错误: %w", err)
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
