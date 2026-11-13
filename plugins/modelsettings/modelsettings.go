// Package modelsettings 提供「模型设置」面板：多 provider 配置、密钥只写不读、连通性测试。
package modelsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yazzyk/stock-agent/internal/llm"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

// init 把插件工厂登记进全局登记表：builtin 只需 blank import 本包即可生效。
func init() { all.Register(New) }

const serviceName = "llm"

type Plugin struct {
	plugin.Base
	svc *llm.Service
}

func New() plugin.Plugin { return &Plugin{} }

func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          "modelsettings",
		Name:        "模型设置",
		Version:     "0.1.0",
		Description: "配置 DeepSeek / OpenAI 兼容 / OpenCode 网关 / WorkBuddy（本机登录）模型；API Key 本地加密、只写不读，支持连通性测试。",
		Author:      "guanlan",
		Tags:        []string{"设置", "模型"},
		Provides:    []string{"llm.settings"},
	}
}

func (p *Plugin) Activate(c *plugin.Context) error {
	v, ok := c.Services.Get(serviceName)
	if !ok {
		return errors.New("模型服务未注册：bootstrap 需要把 *llm.Service 注册为 service \"llm\"")
	}
	svc, ok := v.(*llm.Service)
	if !ok {
		return fmt.Errorf("模型服务类型不符：%T", v)
	}
	p.svc = svc

	if err := c.Methods.Add(plugin.Method{Name: "list", Description: "列出模型配置", Fn: p.list}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "save", Description: "新增/更新模型配置", Fn: p.save}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "remove", Description: "删除模型配置", Fn: p.remove}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "test", Description: "测试模型连通性", Fn: p.test}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "setDefault", Description: "把某条模型配置设为默认", Fn: p.setDefault}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "presets", Description: "内置 provider/模型预设", Fn: presets}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "workbuddyStatus", Description: "读取本机 WorkBuddy 登录状态（不含 token）", Fn: p.workbuddyStatus}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "workbuddyModels", Description: "拉取 WorkBuddy 账号的模型目录", Fn: p.workbuddyModels}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "workbuddyLogin", Description: "发起 WorkBuddy 浏览器登录，返回授权地址", Fn: p.workbuddyLogin}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "workbuddyLoginPoll", Description: "轮询 WorkBuddy 登录结果", Fn: p.workbuddyLoginPoll}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "workbuddyImport", Description: "从 dsh / go-stock 导入 WorkBuddy 登录态", Fn: p.workbuddyImport}); err != nil {
		return err
	}
	if err := c.Methods.Add(plugin.Method{Name: "workbuddyLogout", Description: "退出 WorkBuddy 登录（清空本应用凭证）", Fn: p.workbuddyLogout}); err != nil {
		return err
	}
	return c.UI.Add(plugin.UISlot{
		ID: "modelsettings", Title: "模型设置", Icon: "cpu", Group: "设置",
		Component: "modelsettings", Order: 90,
	})
}

type listResp struct {
	Providers []llm.ProviderView `json:"providers"`
	DefaultID string             `json:"defaultId"`
}

func (p *Plugin) list(context.Context, json.RawMessage) (any, error) {
	views, defaultID, err := p.svc.List()
	if err != nil {
		return nil, err
	}
	return listResp{Providers: views, DefaultID: defaultID}, nil
}

func (p *Plugin) save(_ context.Context, payload json.RawMessage) (any, error) {
	var in llm.SaveInput
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	views, defaultID, err := p.svc.Save(in)
	if err != nil {
		return nil, err
	}
	return listResp{Providers: views, DefaultID: defaultID}, nil
}

// setDefault 只切换默认模型指向，不修改该配置的其他字段。
func (p *Plugin) setDefault(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	views, defaultID, err := p.svc.SetDefault(in.ID)
	if err != nil {
		return nil, err
	}
	return listResp{Providers: views, DefaultID: defaultID}, nil
}

func (p *Plugin) remove(_ context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	if in.ID == "" {
		return nil, errors.New("缺少 id")
	}
	views, defaultID, err := p.svc.Remove(in.ID)
	if err != nil {
		return nil, err
	}
	return listResp{Providers: views, DefaultID: defaultID}, nil
}

// presetsResp 是「新增配置」下拉的填充数据：provider 接入信息 + 官方模型参数。
type presetsResp struct {
	Providers []llm.ProviderPreset `json:"providers"`
}

// presets 只读内置预设表，不读网络、不读写本地配置。
func presets(context.Context, json.RawMessage) (any, error) {
	return presetsResp{Providers: llm.Presets()}, nil
}

func (p *Plugin) test(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(payload, &in)
	return p.svc.Test(ctx, in.ID)
}

// workbuddyStatus 返回本机 WorkBuddy 登录状态；只读本地凭证库，不触发网络刷新。
func (p *Plugin) workbuddyStatus(context.Context, json.RawMessage) (any, error) {
	return p.svc.WorkBuddyStatus(), nil
}

// workbuddyModelsResp 是模型目录响应，形状与前端 data.ts 的解析对齐。
type workbuddyModelsResp struct {
	Models []llm.WorkBuddyModel `json:"models"`
}

// workbuddyModels 返回当前登录账号的模型目录（已过滤图像模型）。
func (p *Plugin) workbuddyModels(ctx context.Context, _ json.RawMessage) (any, error) {
	models, err := p.svc.WorkBuddyModels(ctx)
	if err != nil {
		return nil, err
	}
	return workbuddyModelsResp{Models: models}, nil
}

// workbuddyLogin 发起一次浏览器登录，返回登录会话 id 与授权地址。
func (p *Plugin) workbuddyLogin(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Region string `json:"region"`
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, fmt.Errorf("参数格式错误：%w", err)
		}
	}
	return p.svc.WorkBuddyLoginStart(ctx, in.Region)
}

// workbuddyLoginPoll 轮询一次登录结果；ok=true 表示凭证已落盘。
func (p *Plugin) workbuddyLoginPoll(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		LoginID string `json:"loginId"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, fmt.Errorf("参数格式错误：%w", err)
	}
	if in.LoginID == "" {
		return nil, errors.New("缺少 loginId")
	}
	return p.svc.WorkBuddyLoginPoll(ctx, in.LoginID), nil
}

// workbuddyImport 把 dsh / go-stock 已有的 WorkBuddy 登录态导入本地库。
func (p *Plugin) workbuddyImport(ctx context.Context, _ json.RawMessage) (any, error) {
	imported, names, err := p.svc.WorkBuddyImport(ctx)
	if err != nil {
		return nil, err
	}
	message := fmt.Sprintf("已导入 %d 个 WorkBuddy 账号", imported)
	if len(names) > 0 {
		message += "：" + strings.Join(names, "、")
	}
	return map[string]string{"message": message}, nil
}

// workbuddyLogout 清空本应用自己的 WorkBuddy 凭证库。
func (p *Plugin) workbuddyLogout(context.Context, json.RawMessage) (any, error) {
	if err := p.svc.WorkBuddyLogout(); err != nil {
		return nil, err
	}
	return map[string]string{"message": "已退出 WorkBuddy 登录，本应用保存的凭证已删除"}, nil
}
