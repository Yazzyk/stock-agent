// Package headless 提供无界面运行模式：同一套 kernel 与插件，在终端跑一次任务。
// 它既是 QA 的验收入口，也是出问题时的最小复现路径（不依赖 GUI）。
package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/yazzyk/stock-agent/internal/kernel"
)

// Run 执行一个 headless 任务并返回进程退出码。
func Run(ctx context.Context, k *kernel.Kernel, task string, args []string, w io.Writer) int {
	task = strings.TrimSpace(task)
	switch task {
	case "", "selftest":
		return selftest(ctx, k, w)
	case "plugins":
		return listPlugins(k, w)
	case "tools":
		return listTools(k, w)
	case "quote":
		return callTool(ctx, k, "market.quote", map[string]any{"codes": args}, w)
	case "bars":
		if len(args) == 0 {
			fmt.Fprintln(w, "用法: --task=bars -- 600000 [day|week] [limit]")
			return 2
		}
		payload := map[string]any{"code": args[0], "period": "day", "limit": 60}
		if len(args) > 1 {
			payload["period"] = args[1]
		}
		return callTool(ctx, k, "market.bars", payload, w)
	case "watchlist":
		return callTool(ctx, k, "watch.list", map[string]any{}, w)
	case "flow":
		kind := "sector"
		limit := 10
		if len(args) > 0 {
			kind = args[0]
		}
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &limit)
		}
		return callTool(ctx, k, "flow.rank", map[string]any{"kind": kind, "limit": limit}, w)
	case "analyze":
		if len(args) == 0 {
			fmt.Fprintln(w, "用法: --task=analyze -- 600000 [day|week]")
			return 2
		}
		period := "day"
		if len(args) > 1 {
			period = args[1]
		}
		return callTool(ctx, k, "kline.analyze", map[string]any{"code": args[0], "period": period}, w)
	case "minute":
		if len(args) == 0 {
			fmt.Fprintln(w, "用法: --task=minute -- 600000")
			return 2
		}
		return minuteSummary(ctx, k, args[0], w)
	case "strategies":
		return invoke(ctx, k, "quant", "strategies", map[string]any{}, w)
	case "backtest":
		if len(args) == 0 {
			fmt.Fprintln(w, "用法: --task=backtest -- 600000 [ma|rsi|momentum]")
			return 2
		}
		strategy := "ma"
		if len(args) > 1 {
			strategy = args[1]
		}
		return callTool(ctx, k, "quant.backtest", map[string]any{"code": args[0], "strategy": strategy}, w)
	case "walkforward":
		if len(args) == 0 {
			fmt.Fprintln(w, "用法: --task=walkforward -- 600000 [ma|rsi|momentum]")
			return 2
		}
		strategy := "ma"
		if len(args) > 1 {
			strategy = args[1]
		}
		return invoke(ctx, k, "quant", "walkforward", map[string]any{"code": args[0], "strategy": strategy, "folds": 3}, w)
	case "kbs":
		query := strings.Join(args, " ")
		return callTool(ctx, k, "kb.search", map[string]any{"query": query, "limit": 5}, w)
	case "skills":
		return callTool(ctx, k, "skill.list", map[string]any{}, w)
	case "watches", "watchdog":
		return invoke(ctx, k, "watch", "list", map[string]any{}, w)
	case "watch-test":
		if len(args) == 0 {
			fmt.Fprintln(w, "用法: --task=watch-test -- <规则ID>")
			return 2
		}
		return invoke(ctx, k, "watch", "test", map[string]any{"id": args[0]}, w)
	case "premarket", "postmarket", "monitor", "quant", "review", "plan":
		worker := task
		if worker == "review" {
			worker = "postmarket"
		}
		if worker == "plan" {
			worker = "premarket"
		}
		return invoke(ctx, k, "agentlab", "run", map[string]any{"worker": worker, "input": strings.Join(args, " ")}, w)
	case "chat":
		return invoke(ctx, k, "agentlab", "run", map[string]any{"worker": "chat", "input": strings.Join(args, " ")}, w)
	default:
		// 兜底：把 task 当成工具名直接调用，便于 QA 与排障。
		if _, ok := k.Tool(task); ok {
			return callTool(ctx, k, task, map[string]any{}, w)
		}
		fmt.Fprintf(w, "未知任务 %q。可用: selftest|plugins|tools|quote|bars|watchlist|flow|analyze|strategies|backtest|walkforward|kbs|skills|watches|watch-test|premarket|postmarket|monitor|quant|chat|<工具名>\n", task)
		return 2
	}
}

func selftest(ctx context.Context, k *kernel.Kernel, w io.Writer) int {
	st := k.Status()
	fmt.Fprintf(w, "插件: %d 个，已激活 %d 个，已停用 %d 个；工具 %d 个；面板 %d 个\n", st.Plugins, st.Active, len(k.Disabled()), st.Tools, st.UI)
	for _, p := range k.Plugins() {
		state := "ok"
		switch {
		case p.Disabled:
			// 用户主动停用不算故障：启动时 markSkipped，这里如实展示。
			state = "已停用"
		case !p.Active:
			state = "未激活"
			if p.Error != "" {
				state += "(" + p.Error + ")"
			}
		}
		fmt.Fprintf(w, "  - %-16s v%-8s %s\n", p.ID, p.Version, state)
	}
	if st.Services != nil {
		fmt.Fprintf(w, "服务: %s\n", strings.Join(st.Services, ", "))
	}
	if len(st.DataHealth) > 0 {
		fmt.Fprintf(w, "数据源: %v\n", st.DataHealth)
	}
	if models, err := k.LLM().Models(ctx); err == nil {
		for _, m := range models {
			fmt.Fprintf(w, "模型: %s/%s key=%v default=%v\n", m.Provider, m.Model, m.HasKey, m.IsDefault)
		}
	}
	// 停用是用户的显式选择：只有「既没激活也没停用」才算故障。
	if fail := st.Plugins == 0 || st.Active+len(k.Disabled()) != st.Plugins; fail {
		fmt.Fprintln(w, "结果: FAIL（插件未全部激活，且不全是主动停用）")
		return 1
	}
	fmt.Fprintln(w, "结果: OK")
	return 0
}

func listPlugins(k *kernel.Kernel, w io.Writer) int {
	for _, p := range k.Plugins() {
		fmt.Fprintf(w, "%-16s %-24s v%-8s active=%-5v tools=%d ui=%d methods=%d\n",
			p.ID, p.Name, p.Version, p.Active, len(p.Tools), len(p.UI), len(p.Methods))
	}
	return 0
}

func listTools(k *kernel.Kernel, w io.Writer) int {
	for _, t := range k.Tools() {
		fmt.Fprintf(w, "%-28s [%-7s] %s\n", t.Name, t.Risk, t.Summary)
	}
	return 0
}

func callTool(ctx context.Context, k *kernel.Kernel, name string, payload map[string]any, w io.Writer) int {
	raw, _ := json.Marshal(payload)
	out, err := k.CallTool(ctx, name, raw)
	if err != nil {
		fmt.Fprintf(w, "调用 %s 失败: %v\n", name, err)
		return 1
	}
	fmt.Fprintln(w, pretty(out))
	return 0
}

// minuteSummary 输出分时摘要（来源/名称/日期/点数与前 3 个点），
// 避免 241 个点把终端输出撑爆，同时能直接看出降级到了哪个数据源。
func minuteSummary(ctx context.Context, k *kernel.Kernel, code string, w io.Writer) int {
	raw, _ := json.Marshal(map[string]any{"code": code})
	out, err := k.Invoke(ctx, "kline", "minute", raw)
	if err != nil {
		fmt.Fprintf(w, "调用 kline.minute 失败: %v\n", err)
		return 1
	}
	var series struct {
		Code      string  `json:"code"`
		Name      string  `json:"name"`
		Date      string  `json:"date"`
		Source    string  `json:"source"`
		PrevClose float64 `json:"prevClose"`
		Points    []struct {
			Time   string  `json:"time"`
			Price  float64 `json:"price"`
			Avg    float64 `json:"avg"`
			Volume float64 `json:"volume"`
		} `json:"points"`
	}
	if err := json.Unmarshal(out, &series); err != nil {
		fmt.Fprintf(w, "解析分时结果失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "%s %s 日期=%s 来源=%s 点数=%d 昨收=%.2f\n", series.Code, series.Name, series.Date, series.Source, len(series.Points), series.PrevClose)
	for i, point := range series.Points {
		if i >= 3 {
			fmt.Fprintln(w, "…")
			break
		}
		fmt.Fprintf(w, "  %s 价=%.2f 均价=%.2f 量=%.0f\n", point.Time, point.Price, point.Avg, point.Volume)
	}
	return 0
}

func invoke(ctx context.Context, k *kernel.Kernel, pluginID, method string, payload map[string]any, w io.Writer) int {
	raw, _ := json.Marshal(payload)
	out, err := k.Invoke(ctx, pluginID, method, raw)
	if err != nil {
		fmt.Fprintf(w, "调用 %s.%s 失败: %v\n", pluginID, method, err)
		return 1
	}
	fmt.Fprintln(w, pretty(out))
	return 0
}

func pretty(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	if len(b) > 4000 {
		// 按 rune 截断：按字节切会把中文 UTF-8 序列切断，产出非法 JSON。
		r := []rune(string(b))
		if len(r) > 4000 {
			return string(r[:4000]) + "\n…（已截断）"
		}
		return string(b)
	}
	return string(b)
}
