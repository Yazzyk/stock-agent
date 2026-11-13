// Package console 提供「设置中心」面板：插件 / 工具 / 审计 / 安全策略 / 数据源总览，
// 以及插件的启用与停用（写盘 + 重启生效，不做运行时热停用）。
//
// 数据来源：bootstrap 注册的服务 "kernel.inventory"（kernel.Inventory 接口）。
// 这里只用类型断言取接口，不 import bootstrap（否则会形成 builtin → console → bootstrap → builtin 的环）。
package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

// init 把插件工厂登记进全局登记表：builtin 只需 blank import 本包即可生效。
func init() { all.Register(New) }

const (
	// serviceName 是 bootstrap 注册的设置中心服务名。
	serviceName = "kernel.inventory"
	// auditLimit 是审计页展示的最近事件条数（契约 §3.9）。
	auditLimit = 100
	// savedMessage 是启停保存后给用户的中文提示。
	savedMessage = "已保存，重启应用后生效"
)

type Plugin struct {
	plugin.Base
	inv   kernel.Inventory
	audit contract.Audit
	guard contract.Guard
	// fy 是官方金融数据 API 的管理能力（数据层可选实现；nil 表示当前实现不支持配置）。
	fy contract.FuyaoAdmin
}

func New() plugin.Plugin { return &Plugin{} }

func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "console",
		Name:        "设置中心",
		Version:     "0.1.0",
		Description: "插件、工具、审计、安全策略与数据源总览；插件启用/停用（重启生效）。",
		Author:      "guanlan",
		Tags:        []string{"设置", "系统"},
		Provides:    []string{"console.inventory"},
	}
}

func (p *Plugin) Activate(c *plugin.Context) error {
	v, ok := c.Services.Get(serviceName)
	if !ok {
		return errors.New("设置中心服务未注册：bootstrap 需要把 kernel.Inventory 实现注册为 service \"kernel.inventory\"")
	}
	inv, ok := v.(kernel.Inventory)
	if !ok {
		return fmt.Errorf("设置中心服务类型不符：%T 未实现 kernel.Inventory", v)
	}
	p.inv = inv
	p.audit = c.Audit
	p.guard = c.Guard
	// 官方数据源配置：数据服务以 "datasource" 注册，实现 contract.FuyaoAdmin 时才可用。
	if v, ok := c.Services.Get("datasource"); ok {
		if admin, ok := v.(contract.FuyaoAdmin); ok {
			p.fy = admin
		}
	}

	if err := c.Methods.Add(plugin.Method{Name: "inventory", Description: "运行时总览（插件/工具/审计/状态）", Fn: p.inventory}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "setEnabled", Description: "启用/停用插件（重启生效）", Fn: p.setEnabled}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "policy", Description: "只读安全策略（路径已脱敏）", Fn: p.policy}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "fuyao", Description: "官方金融数据 API 配置状态（不含明文 Key）", Fn: p.fuyaoStatus}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "fuyaoSave", Description: "保存官方金融数据 API 配置（Key 只写不读）", Fn: p.fuyaoSave}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "fuyaoTest", Description: "用真实快照请求验证官方 API Key", Fn: p.fuyaoTest}); err != nil {
		return err
	}
	return c.UI.Add(plugin.UISlot{
		ID: "console", Title: "设置中心", Icon: "settings", Group: "系统",
		Component: "console", Order: 95,
	})
}

// inventoryResp 是设置中心首屏的完整数据；audit 为附加字段（前端审计页直接用）。
type inventoryResp struct {
	Plugins         []kernel.PluginView   `json:"plugins"`
	Status          kernel.StatusView     `json:"status"`
	Tools           []kernel.ToolView     `json:"tools"`
	Disabled        []string              `json:"disabled"`
	RestartRequired bool                  `json:"restartRequired"`
	Audit           []contract.AuditEvent `json:"audit"`
	// Protected 是不能停用的插件（设置中心自己）：前端应把开关置灰并给提示。
	Protected []string `json:"protected"`
}

func (p *Plugin) inventory(context.Context, json.RawMessage) (any, error) {
	resp := inventoryResp{
		Plugins:         p.inv.Plugins(),
		Status:          p.inv.Status(),
		Tools:           p.inv.Tools(),
		Disabled:        p.inv.Disabled(),
		RestartRequired: p.inv.RestartRequired(),
		Audit:           []contract.AuditEvent{},
		Protected:       []string{"console"},
	}
	if p.audit != nil {
		// 审计读取失败不影响其它标签，只降级为空列表。
		if events, err := p.audit.List(auditLimit); err == nil {
			for _, e := range events {
				e.Detail = p.redact(e.Detail)
				resp.Audit = append(resp.Audit, e)
			}
		}
	}
	return resp, nil
}

// setEnabledResp 按契约 §3.9 返回 ok/restartRequired/message；disabled 是附加字段，
// 让前端省一次 inventory 往返即可刷新开关。
type setEnabledResp struct {
	OK              bool     `json:"ok"`
	RestartRequired bool     `json:"restartRequired"`
	Message         string   `json:"message"`
	Disabled        []string `json:"disabled"`
}

func (p *Plugin) setEnabled(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	if strings.TrimSpace(in.ID) == "" {
		return nil, errors.New("缺少插件 id")
	}
	if err := p.inv.SetEnabled(in.ID, in.Enabled); err != nil {
		return nil, err
	}
	return setEnabledResp{
		OK:              true,
		RestartRequired: p.inv.RestartRequired(),
		Message:         savedMessage,
		Disabled:        p.inv.Disabled(),
	}, nil
}

// fuyaoStatus 返回官方数据源配置状态；数据层未实现管理能力时返回可操作提示。
func (p *Plugin) fuyaoStatus(ctx context.Context, _ json.RawMessage) (any, error) {
	if p.fy == nil {
		return nil, errors.New("当前数据层不支持配置官方 API（datasource 未实现 FuyaoAdmin）")
	}
	return p.fy.FuyaoStatus(ctx), nil
}

// fuyaoSaveBody 是保存官方 API 配置的入参。
type fuyaoSaveBody struct {
	APIKey   string `json:"apiKey"`
	BaseURL  string `json:"baseUrl"`
	Enabled  bool   `json:"enabled"`
	ClearKey bool   `json:"clearKey"`
}

// fuyaoSave 保存官方 API 配置并回读最新状态（Key 只写不读）。
func (p *Plugin) fuyaoSave(ctx context.Context, payload json.RawMessage) (any, error) {
	if p.fy == nil {
		return nil, errors.New("当前数据层不支持配置官方 API（datasource 未实现 FuyaoAdmin）")
	}
	var in fuyaoSaveBody
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("参数格式错误：%w", err)
		}
	}
	return p.fy.SaveFuyao(ctx, contract.FuyaoConfig{
		APIKey:   in.APIKey,
		BaseURL:  in.BaseURL,
		Enabled:  in.Enabled,
		ClearKey: in.ClearKey,
	})
}

// fuyaoTest 调一次真实快照接口验证 Key；返回 ok/message，不把上游原文抛给前端。
func (p *Plugin) fuyaoTest(ctx context.Context, _ json.RawMessage) (any, error) {
	if p.fy == nil {
		return nil, errors.New("当前数据层不支持配置官方 API（datasource 未实现 FuyaoAdmin）")
	}
	if err := p.fy.TestFuyao(ctx); err != nil {
		return map[string]any{"ok": false, "message": err.Error()}, nil
	}
	return map[string]any{"ok": true, "message": "官方 API 连通正常"}, nil
}

// policy 返回脱敏后的只读策略：把家目录/数据目录前缀换成 ~，避免把本机绝对路径摊到前端。
func (p *Plugin) policy(context.Context, json.RawMessage) (any, error) {
	return maskPolicy(p.policyValue()), nil
}

func (p *Plugin) policyValue() contract.Policy {
	if p.guard == nil {
		return contract.Policy{}
	}
	return p.guard.Policy()
}

func (p *Plugin) redact(s string) string {
	if p.guard == nil || s == "" {
		return s
	}
	return p.guard.Redact(s)
}

// maskPolicy 把策略里的绝对路径脱敏为 "~" 前缀形式，其余字段原样保留。
func maskPolicy(p contract.Policy) contract.Policy {
	out := p
	out.NetworkAllowlist = append([]string(nil), p.NetworkAllowlist...)
	out.PathRoots = make([]string, 0, len(p.PathRoots))
	for _, root := range p.PathRoots {
		out.PathRoots = append(out.PathRoots, maskPath(root))
	}
	return out
}

func maskPath(path string) string {
	if path == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(path, home)
		}
	}
	return path
}
