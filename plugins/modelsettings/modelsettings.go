// Package modelsettings 提供「模型设置」面板：多 provider 配置、密钥只写不读、连通性测试。
package modelsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
		Description: "配置 DeepSeek / OpenAI 兼容模型，API Key 本地加密、只写不读，支持连通性测试。",
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

func (p *Plugin) test(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(payload, &in)
	return p.svc.Test(ctx, in.ID)
}
