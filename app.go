package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// App 是 Wails 绑定层：只做转发与事件桥接，业务逻辑一律在插件里。
type App struct {
	ctx   context.Context
	k     *kernel.Kernel
	unsub func()
}

func NewApp(k *kernel.Kernel) *App {
	return &App{k: k}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	a.unsub = a.k.Events().Subscribe("*", func(e plugin.Event) {
		if a.ctx == nil {
			return
		}
		runtime.EventsEmit(a.ctx, "kernel:event", map[string]string{
			"topic":   e.Topic,
			"time":    e.Time.Format(time.RFC3339Nano),
			"payload": string(e.Payload),
		})
	})
	a.k.Events().Publish(plugin.Event{Topic: "app.ready"})
}

func (a *App) Shutdown(ctx context.Context) {
	if a.unsub != nil {
		a.unsub()
	}
	_ = a.k.Stop(ctx)
}

// Plugins 返回全部插件的清单（含 UI slot / tools / methods），前端据此动态渲染。
func (a *App) Plugins() []kernel.PluginView { return a.k.Plugins() }

// Status 返回 kernel 健康状态与安全策略，供设置页展示。
func (a *App) Status() kernel.StatusView { return a.k.Status() }

// Invoke 是所有前端到插件的调用入口：Invoke(pluginID, method, payloadJSON) -> resultJSON
func (a *App) Invoke(pluginID, method, payload string) (string, error) {
	raw := json.RawMessage(payload)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	out, err := a.k.Invoke(a.ctx, pluginID, method, raw)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Audit 返回最近的审计事件（只读）。
func (a *App) Audit(limit int) ([]AuditRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	events, err := a.k.Audit().List(limit)
	if err != nil {
		return nil, err
	}
	rows := make([]AuditRow, 0, len(events))
	for _, e := range events {
		rows = append(rows, AuditRow{
			Time:    e.Time.Format("2006-01-02 15:04:05"),
			Plugin:  e.Plugin,
			Kind:    e.Kind,
			Action:  e.Action,
			OK:      e.OK,
			Risk:    e.Risk,
			Elapsed: e.Elapsed,
			Detail:  a.k.Guard().Redact(e.Detail),
		})
	}
	return rows, nil
}

type AuditRow struct {
	Time    string `json:"time"`
	Plugin  string `json:"plugin"`
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	OK      bool   `json:"ok"`
	Risk    string `json:"risk,omitempty"`
	Elapsed int64  `json:"elapsedMs,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Tools 返回工具索引（L1），设置页用于排查"模型看到什么"。
func (a *App) Tools() []kernel.ToolView {
	views := make([]kernel.ToolView, 0, 32)
	for _, t := range a.k.Tools() {
		views = append(views, kernel.ToolView{Name: t.Name, Title: t.Title, Summary: t.Summary, Tags: t.Tags, Risk: string(t.Risk)})
	}
	return views
}

// CallTool 供调试台直接执行工具（不经模型）。
func (a *App) CallTool(name, args string) (string, error) {
	if len(args) == 0 {
		args = "{}"
	}
	out, err := a.k.CallTool(a.ctx, name, json.RawMessage(args))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ResetLayout 预留给前端"恢复默认布局"；不涉及后端状态，故直接返回 true。
func (a *App) ResetLayout() (bool, error) { return true, nil }

