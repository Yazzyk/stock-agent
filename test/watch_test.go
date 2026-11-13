package test

// Watch 哨兵（第 8 个插件）的外部验收：
//   a) 条件 DSL 的安全边界：不能执行代码、不能访问白名单之外的指标；
//   b) 装配契约：缺 agent.runtime 服务时 watch 必须激活失败并说明原因（依赖 agentlab）；
//   c) 方法链路：save → list → toggle → test → logs，规则文件落到 <DataDir>/watches/<id>.json；
//   d) 热加载：改规则文件后引擎在一个 tick 内生效（<5s）。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/agent"
	"github.com/yazzyk/stock-agent/internal/watch"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	watchplugin "github.com/yazzyk/stock-agent/plugins/watch"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------- fake DataService：固定可控的行情/资金流/Bars ----------

type fakeData struct{}

func (fakeData) Quote(_ context.Context, codes []string) ([]contract.Quote, error) {
	out := make([]contract.Quote, 0, len(codes))
	for _, c := range codes {
		out = append(out, contract.Quote{
			Code: c, Name: "浦发银行", Market: contract.MarketSH,
			Price: 10.2, PrevClose: 10.1, Open: 10.06, High: 10.24, Low: 10.0,
			Change: 0.1, ChangePct: 0.99, Volume: 477826, Amount: 485074220,
			Turnover: 0.144, Time: "2026-11-10T15:00:00+08:00", Source: "fake",
		})
	}
	return out, nil
}

func (fakeData) Bars(_ context.Context, code string, _ contract.Period, limit int, _ contract.Adjust) ([]contract.Bar, error) {
	if limit <= 0 {
		limit = 120
	}
	out := make([]contract.Bar, limit)
	price := 9.0
	for i := 0; i < limit; i++ {
		price += 0.02 * math.Sin(float64(i)*0.3)
		out[i] = contract.Bar{
			Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02"),
			Open: price, High: price * 1.01, Low: price * 0.99, Close: price,
			Volume: 500000 + float64(i%7)*1000, Amount: price * (500000 + float64(i%7)*1000),
		}
	}
	_ = code
	return out, nil
}

func (fakeData) StockFundFlow(_ context.Context, code string) (contract.FundFlow, error) {
	return contract.FundFlow{Code: code, Name: "浦发银行", MainNet: 26922753, SuperNet: 9640132, LargeNet: 17282621, MainPct: 5.5}, nil
}

func (fakeData) FundFlowRank(_ context.Context, kind string, limit int) ([]contract.FundFlow, error) {
	return []contract.FundFlow{{Code: "BK1201", Name: "电子", Kind: kind, MainNet: 9201537792, ChangePct: 3.29, Rank: 1}}, nil
}

func (fakeData) Sectors(_ context.Context, kind string) ([]contract.Sector, error) {
	return []contract.Sector{{Code: "BK1201", Name: "电子", Kind: kind, ChangePct: 3.29, MainNet: 9201537792}}, nil
}

func (fakeData) Watchlist(context.Context) ([]contract.WatchItem, error) { return nil, nil }

func (fakeData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	return contract.SyncResult{Success: false, Message: "测试环境不同步"}, nil
}

func (fakeData) Search(context.Context, string) ([]contract.StockRef, error) { return nil, nil }

func (fakeData) Health(context.Context) map[string]string { return map[string]string{"fake": "ok"} }

// ---------- a) 条件 DSL 安全边界 ----------

// 条件表达式只能读白名单指标 + 四则/比较/逻辑运算，任何"执行代码"的写法都必须解析失败。
func TestWatchConditionDSLRejectsCode(t *testing.T) {
	rejected := []struct {
		name string
		src  string
	}{
		{"调用外部命令", `exec("rm -rf /")`},
		{"调用系统函数", `os.system("rm")`},
		{"import 语句", "import os"},
		{"字符串字面量", `quote.price == "10"`},
		{"下标访问", `quote["price"] > 1`},
		{"函数调用形式", "eval(1) > 0"},
		{"注入分号", "quote.price > 1; rm -rf /"},
		{"反引号命令", "quote.price > `ls`"},
		{"Python dunder", "__import__('os')"},
		{"取反后跟函数", "not exec(1)"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := watch.Parse(tc.src); err == nil {
				t.Fatalf("条件 %q 必须解析失败（DSL 不允许执行代码）", tc.src)
			}
		})
	}

	accepted := []string{
		"quote.price > 10",
		"quote.price > 10 and flow.mainNetYi > 1",
		"not kline.rsi < 30 or time.trading == 1",
		"(sector.changePct > 2 or sector.mainNetYi > 5) and time.hour >= 9",
	}
	for _, src := range accepted {
		if _, err := watch.Parse(src); err != nil {
			t.Fatalf("合法条件 %q 不应解析失败: %v", src, err)
		}
	}
}

// 语法能过不代表能引用：不在白名单里的指标必须被 Rule.Validate 拒绝。
func TestWatchRuleValidateRejectsUnknownMetric(t *testing.T) {
	base := func(cond string) watch.Rule {
		return watch.Rule{
			Name:      "测试规则",
			Sources:   []watch.Source{{Type: watch.SourceQuote, Codes: []string{"600000"}}},
			Condition: cond,
			Action:    watch.Action{Worker: "chat", Prompt: "看一下 {summary}"},
		}
	}
	bad := []string{
		"quote.foo > 1",
		"quote.constructor > 1",
		"window.location == 1",
		"process.env > 0",
		"flow.mainNet > 1 and quote.noSuchField > 2",
	}
	for _, cond := range bad {
		r := base(cond)
		err := r.Validate()
		if err == nil {
			t.Fatalf("条件 %q 用了未定义指标，必须被拒绝", cond)
		}
		if !strings.Contains(err.Error(), "未定义") && !strings.Contains(err.Error(), "非法") {
			t.Fatalf("拒绝理由应说明指标未定义/表达式非法，实际: %v", err)
		}
	}

	// 合法条件 + 合法来源应通过，且默认值被补齐
	ok := base("quote.price > 10 and flow.mainNetYi > 1")
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法规则不应被拒绝: %v", err)
	}
	if ok.Action.Worker != "chat" {
		t.Fatalf("worker 应保持显式值 chat，实际 %q", ok.Action.Worker)
	}

	// 非法来源类型同样要拒绝（规则会被自动执行，宁严勿松）
	badSource := base("true")
	badSource.Sources = []watch.Source{{Type: watch.SourceType("shell"), Codes: []string{"600000"}}}
	if err := badSource.Validate(); err == nil {
		t.Fatal("未知触发源类型必须被拒绝")
	}
}

// 指标白名单本身要是稳定有序的，且模板规则只能用白名单里的指标。
func TestWatchMetricWhitelist(t *testing.T) {
	list := watch.MetricList()
	if len(list) == 0 {
		t.Fatal("指标白名单不应为空")
	}
	if !sort.SliceIsSorted(list, func(i, j int) bool { return list[i].Name < list[j].Name }) {
		t.Fatal("指标清单应按名称排序（面板要稳定展示）")
	}
	for _, spec := range list {
		if spec.Label == "" {
			t.Fatalf("指标 %s 缺少中文标签", spec.Name)
		}
	}
	// 模板规则是用户最可能直接套用的样例，它们引用的指标必须在白名单里
	for _, r := range watch.ExampleRules() {
		if err := r.Validate(); err != nil {
			t.Fatalf("模板规则 %q 自身校验失败: %v", r.Name, err)
		}
	}
}

// ---------- b) 装配契约：缺 agent.runtime 必须激活失败 ----------

// fakeAgentLab 模拟 agentlab：需要显式决定是否注册 agent.runtime（用来测依赖语义）。
type fakeAgentLab struct {
	plugin.Base
	withRuntime bool
}

func (fakeAgentLab) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "agentlab", Name: "Agent 工作台", Version: "0.0.1"}
}

func (a *fakeAgentLab) Activate(c *plugin.Context) error {
	if !a.withRuntime {
		return nil // 激活成功但不提供服务：模拟"agentlab 坏了/被裁剪"
	}
	rt := agent.NewRuntime(agent.RuntimeOptions{
		LLM:    nil,
		Guard:  c.Guard,
		Audit:  c.Audit,
		Events: c.Events,
		Policy: c.Policy,
		Store:  c.Store,
		Clock:  c.Clock,
	})
	return c.Services.Register("agent.runtime", rt)
}

func TestWatchPluginRequiresAgentRuntime(t *testing.T) {
	// 反例：agentlab 激活了但没注册 agent.runtime → watch 必须失败并说明原因
	f := newKernelWithData(t, fakeData{})
	f.register(t, &fakeAgentLab{withRuntime: false}, watchplugin.New())
	if err := f.k.Start(context.Background()); err != nil {
		t.Fatalf("单插件失败不应让 Start 返回错误: %v", err)
	}
	v := f.pluginView(t, "watch")
	if v.Active {
		t.Fatal("缺少 agent.runtime 时 watch 不应激活")
	}
	if !strings.Contains(v.Error, "agent.runtime") {
		t.Fatalf("错误应指向缺失的服务 agent.runtime，实际: %q", v.Error)
	}
	if !strings.Contains(v.Error, "agentlab") {
		t.Fatalf("错误应提示依赖 agentlab，实际: %q", v.Error)
	}

	// 正例：注册了 agent.runtime → watch 激活，并贡献 5 个工具 + 1 个面板 + 1 个服务
	f2 := newKernelWithData(t, fakeData{})
	f2.register(t, &fakeAgentLab{withRuntime: true}, watchplugin.New())
	f2.start(t)
	v2 := f2.pluginView(t, "watch")
	if !v2.Active {
		t.Fatalf("有 agent.runtime 时 watch 应激活，实际错误: %q", v2.Error)
	}
	if len(v2.Tools) != 5 {
		t.Fatalf("watch 应注册 5 个工具，实际 %d", len(v2.Tools))
	}
	for _, tl := range v2.Tools {
		if !strings.HasPrefix(tl.Name, "watchdog.") {
			t.Fatalf("工具前缀应为 watchdog.（watch.list 是自选股的），实际 %s", tl.Name)
		}
	}
	if len(v2.UI) != 1 || v2.UI[0].Component != "watch" || v2.UI[0].Order != 35 {
		t.Fatalf("面板槽位应为 watch/order=35，实际 %+v", v2.UI)
	}
	if _, ok := f2.k.Services().Get("watch.engine"); !ok {
		t.Fatal("应注册 watch.engine 服务")
	}
	if st := f2.k.Status(); !containsString(st.Services, "watch.engine") {
		t.Fatalf("Status.Services 应包含 watch.engine，实际 %v", st.Services)
	}
}

// ---------- c) 方法链路：save → list → toggle → test → logs ----------

func TestWatchPluginMethodChain(t *testing.T) {
	f := newKernelWithData(t, fakeData{})
	f.register(t, &fakeAgentLab{withRuntime: true}, watchplugin.New())
	f.start(t)
	ctx := context.Background()

	// 1) 默认规则集（模板）应非空且带指标白名单
	raw, err := f.k.Invoke(ctx, "watch", "list", nil)
	if err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	var listResp struct {
		Rules   []watch.Rule       `json:"rules"`
		Running bool               `json:"running"`
		Metrics []watch.MetricSpec `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		t.Fatalf("list 返回不是合法 JSON: %v", err)
	}
	if !listResp.Running {
		t.Fatal("引擎应在运行（running=true）")
	}
	if len(listResp.Metrics) == 0 {
		t.Fatal("list 应返回指标白名单")
	}

	// 2) save：条件 quote.price > 10（fake 现价 10.2 → 命中）
	// 口径（lead 定稿，安全优先）：**新建规则默认关闭**——规则会真的触发模型调用（花 token），
	// 所以 Save 不替调用方启用；面板"保存并启用"与 Agent 生成规则都要显式传 enabled:true。
	savePayload := `{
		"name": "现价突破 10 元",
		"description": "QA 链路测试",
		"sources": [{"type": "quote", "codes": ["600000"]}],
		"condition": "quote.price > 10",
		"action": {"worker": "chat", "prompt": "现价 {summary}"},
		"cooldownSeconds": 60
	}`
	raw, err = f.k.Invoke(ctx, "watch", "save", json.RawMessage(savePayload))
	if err != nil {
		t.Fatalf("save 失败: %v", err)
	}
	var saveResp struct {
		Rule  watch.Rule   `json:"rule"`
		Rules []watch.Rule `json:"rules"`
	}
	if err := json.Unmarshal(raw, &saveResp); err != nil {
		t.Fatalf("save 返回不是合法 JSON: %v", err)
	}
	rule := saveResp.Rule
	if rule.ID == "" {
		t.Fatal("save 应分配规则 ID")
	}
	if rule.Enabled {
		t.Fatal("新建规则默认应关闭（不传 enabled 时 Normalize 不替调用方启用）")
	}
	if rule.CreatedBy != "user" {
		t.Fatalf("面板/方法创建的规则 createdBy 应为 user，实际 %q", rule.CreatedBy)
	}

	// 3) 规则文件：<DataDir>/watches/<id>.json，内容可回读
	file := filepath.Join(f.dir, "watches", rule.ID+".json")
	buf, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("规则文件应落在 DataDir/watches 下（%s）: %v", file, err)
	}
	var fromFile watch.Rule
	if err := json.Unmarshal(buf, &fromFile); err != nil {
		t.Fatalf("规则文件不是合法 JSON: %v", err)
	}
	if fromFile.ID != rule.ID || fromFile.Condition != "quote.price > 10" {
		t.Fatalf("规则文件内容与保存结果不一致: %+v", fromFile)
	}

	// 4) toggle：显式开启后 enabled=true（这是"保存并启用"的等价路径）
	raw, err = f.k.Invoke(ctx, "watch", "toggle", json.RawMessage(`{"id":"`+rule.ID+`","enabled":true}`))
	if err != nil {
		t.Fatalf("toggle 失败: %v", err)
	}
	var toggleResp struct {
		Rule watch.Rule `json:"rule"`
	}
	if err := json.Unmarshal(raw, &toggleResp); err != nil {
		t.Fatalf("toggle 返回不是合法 JSON: %v", err)
	}
	if !toggleResp.Rule.Enabled {
		t.Fatal("toggle(enabled=true) 后规则应为开启")
	}
	// 再关回去，确认开关可逆（面板两次点击都要生效）
	raw, err = f.k.Invoke(ctx, "watch", "toggle", json.RawMessage(`{"id":"`+rule.ID+`","enabled":false}`))
	if err != nil {
		t.Fatalf("toggle(false) 失败: %v", err)
	}
	if err := json.Unmarshal(raw, &toggleResp); err != nil {
		t.Fatalf("toggle 返回不是合法 JSON: %v", err)
	}
	if toggleResp.Rule.Enabled {
		t.Fatal("toggle(enabled=false) 后规则应为关闭")
	}

	// 5) test：返回 metrics，matched 必须与条件一致（10.2 > 10 → true）
	raw, err = f.k.Invoke(ctx, "watch", "test", json.RawMessage(`{"id":"`+rule.ID+`"}`))
	if err != nil {
		t.Fatalf("test 失败: %v", err)
	}
	var testResp struct {
		RuleID    string `json:"ruleId"`
		Matched   bool   `json:"matched"`
		Scenarios []struct {
			Code    string             `json:"code"`
			Metrics map[string]float64 `json:"metrics"`
			Matched bool               `json:"matched"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &testResp); err != nil {
		t.Fatalf("test 返回不是合法 JSON: %v", err)
	}
	if testResp.RuleID != rule.ID || len(testResp.Scenarios) != 1 {
		t.Fatalf("test 结果不符: %+v", testResp)
	}
	if price := testResp.Scenarios[0].Metrics["quote.price"]; math.Abs(price-10.2) > 1e-9 {
		t.Fatalf("metrics 应含 quote.price=10.2（来自 Data），实际 %v", price)
	}
	if !testResp.Matched {
		t.Fatal("quote.price=10.2 > 10，matched 应为 true")
	}

	// 反证：把条件改成永远不成立，matched 必须翻转（说明 matched 不是常量）
	raw, err = f.k.Invoke(ctx, "watch", "save", json.RawMessage(strings.Replace(savePayload,
		`"quote.price > 10"`, `"quote.price > 100"`, 1)))
	if err != nil {
		t.Fatalf("save(反证规则) 失败: %v", err)
	}
	var saveResp2 struct {
		Rule watch.Rule `json:"rule"`
	}
	if err := json.Unmarshal(raw, &saveResp2); err != nil {
		t.Fatalf("save 返回不是合法 JSON: %v", err)
	}
	raw, err = f.k.Invoke(ctx, "watch", "test", json.RawMessage(`{"id":"`+saveResp2.Rule.ID+`"}`))
	if err != nil {
		t.Fatalf("test(反证规则) 失败: %v", err)
	}
	var testResp2 struct {
		Matched bool `json:"matched"`
	}
	if err := json.Unmarshal(raw, &testResp2); err != nil {
		t.Fatalf("test 返回不是合法 JSON: %v", err)
	}
	if testResp2.Matched {
		t.Fatal("quote.price > 100 不应命中，matched 应为 false（否则 matched 与条件不一致）")
	}

	// 6) logs：返回结构合法（试运行不写触发日志，允许为空但字段必须齐）
	raw, err = f.k.Invoke(ctx, "watch", "logs", json.RawMessage(`{"limit":10}`))
	if err != nil {
		t.Fatalf("logs 失败: %v", err)
	}
	var logsResp struct {
		Logs []watch.RunLog `json:"logs"`
	}
	if err := json.Unmarshal(raw, &logsResp); err != nil {
		t.Fatalf("logs 返回不是合法 JSON: %v", err)
	}
	for _, lg := range logsResp.Logs {
		if lg.RuleID == "" || lg.Time == "" {
			t.Fatalf("日志条目缺 ruleId/time: %+v", lg)
		}
	}

	// 6.5) remove：删规则要同时清掉列表项与规则文件
	if _, err := f.k.Invoke(ctx, "watch", "remove", json.RawMessage(`{"id":"`+rule.ID+`"}`)); err != nil {
		t.Fatalf("remove 失败: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("remove 后规则文件应被删除（%s），实际 err=%v", file, err)
	}
	raw, err = f.k.Invoke(ctx, "watch", "list", nil)
	if err != nil {
		t.Fatalf("list(删除后) 失败: %v", err)
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		t.Fatalf("list 返回不是合法 JSON: %v", err)
	}
	for _, r := range listResp.Rules {
		if r.ID == rule.ID {
			t.Fatalf("remove 后列表里不应再有规则 %s", rule.ID)
		}
	}

	// 7) 未知规则：test / toggle 都要报错而不是静默成功
	if _, err := f.k.Invoke(ctx, "watch", "test", json.RawMessage(`{"id":"nope"}`)); err == nil {
		t.Fatal("未知规则的 test 应报错")
	}
	if _, err := f.k.Invoke(ctx, "watch", "toggle", json.RawMessage(`{"id":"nope","enabled":true}`)); err == nil {
		t.Fatal("未知规则的 toggle 应报错")
	}
}

// ---------- d) 热加载：改文件后 5s 内生效 ----------

func TestWatchHotReloadRuleFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	engine, err := watch.New(watch.Options{
		Store:       newFakeStore(),
		Data:        fakeData{},
		Guard:       newFakeGuard(),
		Audit:       newFakeAudit(),
		Events:      plugin.NewBus(),
		Clock:       time.Now,
		Logger:      discardLogger(),
		DataDir:     dir,
		Tick:        100 * time.Millisecond,
		TradingOnly: false,
		Trigger:     func(context.Context, string, string) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatalf("engine.New 失败: %v", err)
	}
	engine.Start(context.Background())
	t.Cleanup(engine.Stop)

	// 直接往 watches/ 放一条规则文件（热加载路径）
	watches := filepath.Join(dir, "watches")
	if err := os.MkdirAll(watches, 0o700); err != nil {
		t.Fatalf("创建 watches 目录失败: %v", err)
	}
	rule := watch.Rule{
		ID:        "hotreload1",
		Name:      "热加载测试",
		Enabled:   false, // 关掉，避免触发 Agent
		Sources:   []watch.Source{{Type: watch.SourceQuote, Codes: []string{"600000"}}},
		Condition: "quote.price > 1",
		Action:    watch.Action{Worker: "chat", Prompt: "x"},
		CreatedAt: now.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
	}
	buf, _ := json.MarshalIndent(rule, "", "  ")
	if err := os.WriteFile(filepath.Join(watches, "hotreload1.json"), buf, 0o600); err != nil {
		t.Fatalf("写规则文件失败: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := engine.Get("hotreload1"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("规则文件 5s 内未被热加载（引擎 Tick=100ms）")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 再改文件：condition 变化应在下一次 tick 生效（仍然 5s 内）
	rule.Condition = "quote.price > 99"
	buf, _ = json.MarshalIndent(rule, "", "  ")
	if err := os.WriteFile(filepath.Join(watches, "hotreload1.json"), buf, 0o600); err != nil {
		t.Fatalf("改写规则文件失败: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if got, ok := engine.Get("hotreload1"); ok && got.Condition == "quote.price > 99" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("文件内容改动 5s 内未生效（热加载没有按 mtime 重载）")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 默认关闭的规则在 tick 里必须被跳过；显式开启后才允许触发（会真的花模型 token）。
func TestWatchDisabledRuleDoesNotTrigger(t *testing.T) {
	var mu sync.Mutex
	triggers := 0
	engine, err := watch.New(watch.Options{
		Store:              newFakeStore(),
		Data:               fakeData{},
		Guard:              newFakeGuard(),
		Audit:              newFakeAudit(),
		Events:             plugin.NewBus(),
		Clock:              time.Now,
		Logger:             discardLogger(),
		DataDir:            t.TempDir(),
		Tick:               50 * time.Millisecond,
		TradingOnly:        false,
		MaxTriggersPerHour: 100,
		MaxConcurrent:      1,
		Trigger: func(context.Context, string, string) (string, error) {
			mu.Lock()
			triggers++
			mu.Unlock()
			return "已触发", nil
		},
	})
	if err != nil {
		t.Fatalf("engine.New 失败: %v", err)
	}
	engine.Start(context.Background())
	t.Cleanup(engine.Stop)

	saved, err := engine.Save(watch.Rule{
		Name:      "关闭状态不应触发",
		Enabled:   false,
		Sources:   []watch.Source{{Type: watch.SourceQuote, Codes: []string{"600000"}}},
		Condition: "quote.price > 1",
		Action:    watch.Action{Worker: "chat", Prompt: "x"},
	}, "user")
	if err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	if saved.Enabled {
		t.Fatal("Save 不应替调用方启用规则")
	}

	// 给至少 10 个 tick 的机会：条件恒真、但规则关闭，必须一次都不触发
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	got := triggers
	mu.Unlock()
	if got != 0 {
		t.Fatalf("关闭状态的规则不应触发，实际触发 %d 次", got)
	}

	// 显式开启后才触发
	if _, err := engine.Toggle(saved.ID, true); err != nil {
		t.Fatalf("Toggle(true) 失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got = triggers
		mu.Unlock()
		if got > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("开启后 3s 内未触发（条件 quote.price>1 恒真）")
		}
		time.Sleep(50 * time.Millisecond)
	}
	logs := engine.Logs(5)
	if len(logs) == 0 {
		t.Fatal("触发后应有 RunLog")
	}
	if !logs[0].Triggered || !logs[0].Matched {
		t.Fatalf("触发日志应 triggered=true matched=true，实际: %+v", logs[0])
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
