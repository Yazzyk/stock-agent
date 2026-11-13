package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	// settings/plugins.disabled 存 JSON 数组（插件 ID），是插件启停开关的唯一真源。
	pluginsDisabledBucket = "settings"
	pluginsDisabledKey    = "plugins.disabled"
	// inventoryServiceName 是设置中心插件取用的服务名（契约 §3.9）。
	inventoryServiceName = "kernel.inventory"
)

// protectedPlugins 是不允许停用的插件：停用设置中心会导致"再也打不开设置中心"的自锁。
var protectedPlugins = map[string]bool{"console": true}

// kernelInventory 实现 kernel.Inventory：运行时视图委托 kernel，
// 启停开关持久化到 settings/plugins.disabled，重启后由 loadDisabledPlugins 再灌回 kernel。
//
// 为什么不做运行时热停用：插件的工具/方法/面板已经并入全局注册表，
// 中途摘掉会留下悬空贡献（工具还在索引里但 Run 失效），因此一律写盘 + 提示重启。
type kernelInventory struct {
	k     *kernel.Kernel
	store contract.Store
	log   *slog.Logger

	mu              sync.Mutex
	disabled        []string
	restartRequired bool
}

func newKernelInventory(k *kernel.Kernel, store contract.Store, log *slog.Logger, disabled []string) *kernelInventory {
	cp := append([]string(nil), disabled...)
	sort.Strings(cp)
	return &kernelInventory{k: k, store: store, log: log, disabled: cp}
}

func (i *kernelInventory) Plugins() []kernel.PluginView { return i.k.Plugins() }
func (i *kernelInventory) Status() kernel.StatusView    { return i.k.Status() }
func (i *kernelInventory) Tools() []kernel.ToolView     { return i.k.ToolViews() }

func (i *kernelInventory) Disabled() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.disabled...)
}

func (i *kernelInventory) RestartRequired() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.restartRequired
}

// SetEnabled 持久化启停开关。id 必须是已注册插件；重复设置同一状态为幂等（不算待重启变更）。
func (i *kernelInventory) SetEnabled(id string, enabled bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("缺少插件 ID")
	}
	known := false
	for _, v := range i.k.Plugins() {
		if v.ID == id {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("插件 %q 不存在", id)
	}
	if !enabled && protectedPlugins[id] {
		return errors.New("设置中心不能停用自己，否则将无法再启用其它插件")
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	exists := false
	for _, v := range i.disabled {
		if v == id {
			exists = true
			break
		}
	}
	switch {
	case enabled && !exists, !enabled && exists:
		return nil // 已是目标状态，不写盘也不要求重启
	}
	if enabled {
		next := make([]string, 0, len(i.disabled))
		for _, v := range i.disabled {
			if v != id {
				next = append(next, v)
			}
		}
		i.disabled = next
	} else {
		i.disabled = append(i.disabled, id)
		sort.Strings(i.disabled)
	}
	if err := i.persistLocked(); err != nil {
		return err
	}
	i.restartRequired = true
	i.log.Info("插件启停开关已保存（重启生效）", "plugin", id, "enabled", enabled)
	return nil
}

// persistLocked 要求调用方已持锁。
func (i *kernelInventory) persistLocked() error {
	raw, err := json.Marshal(i.disabled)
	if err != nil {
		return fmt.Errorf("保存插件开关失败: %w", err)
	}
	if err := i.store.Put(pluginsDisabledBucket, pluginsDisabledKey, raw); err != nil {
		return fmt.Errorf("保存插件开关失败: %w", err)
	}
	return nil
}

// loadDisabledPlugins 读取持久化的停用列表；配置损坏时降级为"全部启用"并告警，
// 避免一个坏 JSON 把用户所有插件都锁死。
func loadDisabledPlugins(store contract.Store, log *slog.Logger) []string {
	raw, err := store.Get(pluginsDisabledBucket, pluginsDisabledKey)
	if err != nil {
		if !errors.Is(err, contract.ErrNotFound) {
			log.Warn("读取插件停用配置失败，按全部启用处理", "error", err)
		}
		return nil
	}
	if len(raw) == 0 {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		log.Warn("插件停用配置损坏，按全部启用处理", "error", err)
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if v := strings.TrimSpace(id); v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
