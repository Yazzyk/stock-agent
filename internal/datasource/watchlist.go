package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 自选股持久化：bucket=watchlist（本包独占），键布局 item/<归一化代码> + meta。
// 另外总有一个「手工导入」兜底文件 <DataDir>/watchlist.json —— 它不依赖任何授权与登录态，
// 是用户在权限/登录都拿不到时的最后一条路。
const (
	bucketWatchlist  = "watchlist"
	prefixWatchItem  = "item/"
	keyWatchMeta     = "meta"
	watchFileName    = "watchlist.json"
	defaultGroupName = "自选股"
)

type watchMeta struct {
	UpdatedAt string   `json:"updatedAt"`
	Source    string   `json:"source"`
	Groups    []string `json:"groups"`
}

type watchFilePayload struct {
	UpdatedAt string               `json:"updatedAt"`
	Items     []contract.WatchItem `json:"items"`
}

// Watchlist 返回本地自选列表（store 优先，空则读手工导入文件）。
func (s *Service) Watchlist(_ context.Context) ([]contract.WatchItem, error) {
	items, err := s.loadWatchItems()
	if err != nil && !errors.Is(err, contract.ErrNotFound) {
		return nil, err
	}
	if len(items) > 0 {
		return items, nil
	}
	fileItems, _, fileErr := s.loadManualWatchlist()
	if fileErr != nil {
		if errors.Is(fileErr, os.ErrNotExist) {
			return []contract.WatchItem{}, nil
		}
		return nil, fileErr
	}
	return fileItems, nil
}

// SyncTHSWatchlist 按三级降级同步同花顺自选：
//
//	① macOS 同花顺客户端容器（需完全磁盘访问权限，未授权返回 NeedFullDiskAccess）
//	② 网页端 t.10jqka.com.cn（需登录态 cookie，缺失返回 NeedWebLogin）
//	③ 手工导入 <DataDir>/watchlist.json（始终可用）
//
// 三路都拿不到时返回 Success=false 的中文结果，而不是 error —— 前端要能展示「缺什么、去哪儿补」。
func (s *Service) SyncTHSWatchlist(ctx context.Context) (contract.SyncResult, error) {
	result := contract.SyncResult{Warnings: []string{}}

	var groups []thsGroup
	var source string

	home := s.home
	if home == "" {
		result.Warnings = append(result.Warnings, "无法定位用户主目录，跳过本机同花顺客户端")
	} else {
		local, _, err := scanTHSLocal(home)
		switch {
		case err == nil && len(local) > 0:
			groups, source = local, "ths-local"
		case errors.Is(err, ErrNeedFullDiskAccess):
			result.NeedFullDiskAccess = true
			result.Warnings = append(result.Warnings, err.Error())
		case err != nil:
			result.Warnings = append(result.Warnings, err.Error())
		}
	}

	if len(groups) == 0 {
		web, webErr := s.thsWebGroups(ctx)
		switch {
		case webErr == nil:
			groups, source = []thsGroup{web}, "ths-web"
		case errors.Is(webErr, ErrNeedWebLogin):
			result.NeedWebLogin = true
			result.Warnings = append(result.Warnings, webErr.Error())
		case webErr != nil:
			result.Warnings = append(result.Warnings, "同花顺网页端自选读取失败: "+webErr.Error())
		}
	}

	if len(groups) == 0 {
		fileItems, _, fileErr := s.loadManualWatchlist()
		if fileErr == nil && len(fileItems) > 0 {
			groups, source = groupsFromItems(fileItems), "manual"
		} else {
			result.Success = false
			result.Message = "没有可同步的自选来源：本机同花顺需要完全磁盘访问权限或登录态，数据目录下也没有 watchlist.json"
			return result, nil
		}
	}

	existing := s.loadWatchItemsQuiet()
	merged, added, removed, groupNames, warnings := s.mergeWatchGroups(existing, groups, source)
	// 同花顺归档里只有代码没有名称，名字必须在这里用批量快照回填并落盘，
	// 否则面板与盘前计划拿到的全是裸代码。
	// 旧名字只作为「快照失败时的兜底」，不再直接继承 —— 否则一次乱码会永久留在库里。
	merged = s.fillWatchNames(ctx, merged, watchNamesOf(existing))
	result.Warnings = append(result.Warnings, warnings...)
	if err := s.saveWatchItems(merged, source, groupNames); err != nil {
		result.Success = false
		result.Message = "自选已解析但保存失败: " + err.Error()
		result.Groups = groupNames
		result.Source = source
		return result, nil
	}
	result.Success = true
	result.Source = source
	result.Groups = groupNames
	result.Added = added
	result.Removed = removed
	result.Message = fmt.Sprintf("已从%s同步 %d 个分组、%d 只标的（新增 %d，移除 %d）", sourceLabel(source), len(groupNames), len(merged), added, removed)
	return result, nil
}

func sourceLabel(source string) string {
	switch source {
	case "ths-local":
		return "同花顺客户端"
	case "ths-web":
		return "同花顺网页端"
	case "manual":
		return "手工导入 watchlist.json"
	}
	return source
}

// groupsFromItems 把手工导入的条目按分组聚合成同步用的中间结构。
func groupsFromItems(items []contract.WatchItem) []thsGroup {
	order := make([]string, 0, 4)
	byName := map[string]*thsGroup{}
	for _, item := range items {
		name := strings.TrimSpace(item.Group)
		if name == "" {
			name = defaultGroupName
		}
		group, exists := byName[name]
		if !exists {
			group = &thsGroup{ExternalID: fmt.Sprintf("manual-%d", len(order)), Name: name}
			byName[name] = group
			order = append(order, name)
		}
		code, err := ParseCode(item.Code)
		if err != nil {
			continue
		}
		group.Codes = append(group.Codes, code.MarketSymbol())
	}
	out := make([]thsGroup, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}

// mergeWatchGroups 把同步结果与现有自选合并，返回新列表、新增数、移除数与分组顺序。
func (s *Service) mergeWatchGroups(existing []contract.WatchItem, groups []thsGroup, source string) ([]contract.WatchItem, int, int, []string, []string) {
	warnings := make([]string, 0, 2)
	existingByCode := make(map[string]contract.WatchItem, len(existing))
	for _, item := range existing {
		code, err := ParseCode(item.Code)
		if err != nil {
			continue
		}
		existingByCode[code.ID()] = item
	}

	merged := make([]contract.WatchItem, 0, len(existing))
	seen := make(map[string]bool, len(existing))
	groupNames := make([]string, 0, len(groups))
	order := 0
	added := 0
	for _, group := range groups {
		name := strings.TrimSpace(group.Name)
		if name == "" {
			name = defaultGroupName
		}
		groupNames = append(groupNames, name)
		for _, raw := range group.Codes {
			code, err := ParseCode(raw)
			if err != nil {
				warnings = append(warnings, "跳过无法识别的代码: "+raw)
				continue
			}
			if seen[code.ID()] {
				continue
			}
			seen[code.ID()] = true
			item := contract.WatchItem{
				Code:   code.Symbol,
				Name:   "",
				Market: code.Market,
				Group:  name,
				Order:  order,
				Source: source,
			}
			order++
			if _, ok := existingByCode[code.ID()]; !ok {
				added++
			}
			merged = append(merged, item)
		}
	}
	removed := 0
	for id := range existingByCode {
		if !seen[id] {
			removed++
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Group != merged[j].Group {
			return groupIndex(groupNames, merged[i].Group) < groupIndex(groupNames, merged[j].Group)
		}
		return merged[i].Order < merged[j].Order
	})
	return merged, added, removed, groupNames, warnings
}

func groupIndex(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return len(names)
}

// ---------- store 读写 ----------

func (s *Service) loadWatchItems() ([]contract.WatchItem, error) {
	if s.cfg.Store == nil {
		return nil, contract.ErrNotFound
	}
	pairs, err := s.cfg.Store.List(bucketWatchlist, prefixWatchItem)
	if err != nil {
		return nil, err
	}
	items := make([]contract.WatchItem, 0, len(pairs))
	for _, pair := range pairs {
		var item contract.WatchItem
		if json.Unmarshal(pair.Value, &item) != nil {
			continue
		}
		items = append(items, item)
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

func (s *Service) loadWatchItemsQuiet() []contract.WatchItem {
	items, err := s.loadWatchItems()
	if err != nil {
		return nil
	}
	return items
}

// watchNamesOf 收集现有条目里已可用的名称，键为归一化代码 ID。
func watchNamesOf(items []contract.WatchItem) map[string]string {
	out := make(map[string]string, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		raw := item.Code
		if item.Market != "" {
			raw = string(item.Market) + item.Code
		}
		code, err := ParseCode(raw)
		if err != nil {
			continue
		}
		out[code.ID()] = name
	}
	return out
}

// fillWatchNames 给缺名称的条目批量补名字，并把名称写进 market_cache。
// 行情源全部失败时依次回退「旧名字」与「本地名称缓存」，同步本身不因取名失败而失败。
func (s *Service) fillWatchNames(ctx context.Context, items []contract.WatchItem, previous map[string]string) []contract.WatchItem {
	missing := make([]Code, 0, len(items))
	indexes := make([]int, 0, len(items))
	for i, item := range items {
		if strings.TrimSpace(item.Name) != "" {
			continue
		}
		raw := item.Code
		if item.Market != "" {
			raw = string(item.Market) + item.Code
		}
		code, err := ParseCode(raw)
		if err != nil {
			continue
		}
		missing = append(missing, code)
		indexes = append(indexes, i)
	}
	if len(missing) == 0 {
		return items
	}
	names := s.fetchStockNames(ctx, missing)
	for _, index := range indexes {
		item := &items[index]
		raw := item.Code
		if item.Market != "" {
			raw = string(item.Market) + item.Code
		}
		code, err := ParseCode(raw)
		if err != nil {
			continue
		}
		if name, ok := names[code.ID()]; ok && name != "" {
			item.Name = name
			s.saveCachedName(code, name)
			continue
		}
		if name := previous[code.ID()]; name != "" {
			item.Name = name
			continue
		}
		item.Name = s.CachedName(code)
	}
	return items
}

// saveWatchItems 全量覆盖自选（写新增、删旧键、更新 meta）。没有 Store 时明确报错，不假装成功。
func (s *Service) saveWatchItems(items []contract.WatchItem, source string, groups []string) error {
	if s.cfg.Store == nil {
		return errors.New("datasource: 没有接入 Store，无法保存自选")
	}
	keys := make(map[string]bool, len(items))
	for _, item := range items {
		code, err := ParseCode(item.Code)
		if err != nil {
			continue
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		key := prefixWatchItem + code.ID()
		keys[key] = true
		if err := s.cfg.Store.Put(bucketWatchlist, key, raw); err != nil {
			return err
		}
	}
	existing, err := s.cfg.Store.List(bucketWatchlist, prefixWatchItem)
	if err == nil {
		for _, pair := range existing {
			if !keys[pair.Key] {
				if err := s.cfg.Store.Delete(bucketWatchlist, pair.Key); err != nil {
					return err
				}
			}
		}
	}
	meta := watchMeta{
		UpdatedAt: s.now().Format("2006-01-02T15:04:05-07:00"),
		Source:    source,
		Groups:    groups,
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return s.cfg.Store.Put(bucketWatchlist, keyWatchMeta, raw)
}

// WatchMeta 返回最近一次同步的来源与时间（面板展示用）。
func (s *Service) WatchMeta() watchMeta {
	if s.cfg.Store == nil {
		return watchMeta{}
	}
	raw, err := s.cfg.Store.Get(bucketWatchlist, keyWatchMeta)
	if err != nil {
		return watchMeta{}
	}
	var meta watchMeta
	if json.Unmarshal(raw, &meta) != nil {
		return watchMeta{}
	}
	return meta
}

// ---------- 手工导入文件 ----------

func (s *Service) manualWatchlistPath() string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, watchFileName)
}

// loadManualWatchlist 读 <DataDir>/watchlist.json；文件不存在返回 os.ErrNotExist。
func (s *Service) loadManualWatchlist() ([]contract.WatchItem, watchFilePayload, error) {
	path := s.manualWatchlistPath()
	if path == "" {
		return nil, watchFilePayload{}, os.ErrNotExist
	}
	if s.cfg.Guard != nil {
		if err := s.cfg.Guard.CheckPath(path); err != nil {
			return nil, watchFilePayload{}, fmt.Errorf("datasource: 自选文件路径不在允许目录内: %w", err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, watchFilePayload{}, err
	}
	var payload watchFilePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, watchFilePayload{}, fmt.Errorf("datasource: watchlist.json 格式异常（需要 items 数组，元素含 code/name/group/order）: %w", err)
	}
	items := make([]contract.WatchItem, 0, len(payload.Items))
	for i, item := range payload.Items {
		code, err := ParseCode(item.Code)
		if err != nil {
			continue
		}
		group := strings.TrimSpace(item.Group)
		if group == "" {
			group = defaultGroupName
		}
		order := item.Order
		if order == 0 {
			order = i
		}
		items = append(items, contract.WatchItem{
			Code:   code.Symbol,
			Name:   strings.TrimSpace(item.Name),
			Market: code.Market,
			Group:  group,
			Order:  order,
			Source: "manual",
		})
	}
	if len(items) == 0 {
		return nil, payload, nil
	}
	return items, payload, nil
}
