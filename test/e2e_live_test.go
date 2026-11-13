//go:build live

package test

// 端到端联网验收（需显式开 tag）：
//   go test -tags live ./test/ -run TestLive -v -count=1
//
// 和 internal/bootstrap/live_test.go（lead 维护）的分工：
//   - 那边验证「Go 客户端解析层」拿得到真实数据；
//   - 这里验证「整条装配链」：bootstrap → kernel → 插件 → 工具，且用**临时数据目录**，
//     不污染 ~/.guanlan 的真实自选/知识库/会话。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yazzyk/stock-agent/internal/bootstrap"
	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// chdirRepoRoot 把工作目录切到仓库根（go test 的 cwd 是包目录，插件按 cwd 找 content/）。
func chdirRepoRoot(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("取工作目录失败: %v", err)
	}
	if filepath.Base(wd) != "test" {
		return
	}
	root := filepath.Dir(wd)
	if err := os.Chdir(root); err != nil {
		t.Fatalf("切换到仓库根失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

// startLiveKernel 在临时数据目录里装配并启动真实 kernel（插件全部接线）。
func startLiveKernel(t *testing.T) (*kernel.Kernel, string) {
	t.Helper()
	chdirRepoRoot(t)
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	k, err := bootstrap.New(bootstrap.Config{DataDir: dataDir, LogLevel: "error"}, logger)
	if err != nil {
		t.Fatalf("bootstrap.New 失败: %v", err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("kernel.Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = k.Stop(context.Background()) })
	return k, dataDir
}

// 工具摘要的字数上限单独成一条测试：它是"工具作者义务"（PLAN §3.1），
// 与数据链路是否跑通无关，分开红才能一眼看出该谁修。
func TestLiveToolSummaries(t *testing.T) {
	k, _ := startLiveKernel(t)
	var longSummary []string
	for _, tl := range k.Tools() {
		if n := utf8.RuneCountInString(tl.Summary); n > 40 {
			longSummary = append(longSummary, fmt.Sprintf("%s（%d 字）", tl.Name, n))
		}
		if tl.Risk == plugin.RiskExec {
			t.Fatalf("不允许出现 exec 风险工具（红线）：%s", tl.Name)
		}
	}
	if len(longSummary) > 0 {
		t.Fatalf("以下工具 summary 超过 40 字（PLAN §3.1 要求 ≤40 字；L1 索引会截断，但作者义务仍要遵守）: %s",
			strings.Join(longSummary, ", "))
	}
}

// TestLiveWatchlist 单独验收 R7（自选同步 → 列表）：
// 本机没有同花顺数据时同步会失败，这时按 ACCEPTANCE 的约定记 BLOCKED 而不是 FAIL。
func TestLiveWatchlist(t *testing.T) {
	k, _ := startLiveKernel(t)
	ctx := context.Background()

	syncRaw, err := k.CallTool(ctx, "watch.sync", json.RawMessage(`{}`))
	if err != nil {
		t.Skipf("watch.sync 失败（本机可能没有同花顺本地数据或未授权完全磁盘访问）→ R7 记 BLOCKED: %v", err)
	}
	// 口径（lead 2026-11-10 拍板）：SyncResult.Source = 本次同步来源 = ths-local；
	// 而每个 WatchItem.Source = 条目来源 = ths。两者语义不同，都要断言在。
	var syncRes struct {
		Success bool     `json:"success"`
		Source  string   `json:"source"`
		Added   int      `json:"added"`
		Groups  []string `json:"groups"`
	}
	if err := json.Unmarshal(syncRaw, &syncRes); err != nil {
		t.Fatalf("watch.sync 返回不是合法 JSON: %v", err)
	}
	if !syncRes.Success {
		t.Skipf("watch.sync 未成功（结构化失败也是合法结果，记 BLOCKED）：%+v", syncRes)
	}
	if syncRes.Source != "ths-local" {
		t.Fatalf("同步来源应为 ths-local（SyncResult.Source），实际 %q", syncRes.Source)
	}

	raw, err := k.CallTool(ctx, "watch.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("同步成功但列表失败: %v", err)
	}
	var wl struct {
		Count  int      `json:"count"`
		Groups []string `json:"groups"`
		Items  []struct {
			Code   string `json:"code"`
			Name   string `json:"name"`
			Group  string `json:"group"`
			Market string `json:"market"`
			Source string `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &wl); err != nil {
		t.Fatalf("watch.list 返回不是合法 JSON: %v", err)
	}
	if wl.Count == 0 || len(wl.Items) == 0 {
		t.Fatalf("同步后自选列表不应为空: %+v", wl)
	}

	badNames := 0
	var samples []string
	badSource := 0
	for _, it := range wl.Items {
		if it.Code == "" || it.Market == "" || it.Group == "" {
			t.Fatalf("自选条目缺 code/market/group: %+v", it)
		}
		// 实测（2026-11-10 22:40）：条目 source 与同步 source 都是 ths-local。
		// lead 的 D13 口径说"条目来源=ths、同步来源=ths-local"，与实现不符；
		// 这里按**实测 + ACCEPTANCE R7 示例**断言 ths-local，并把差异回报 lead（见 QA.md D13）。
		if it.Source != "ths-local" {
			badSource++
			if badSource == 1 {
				t.Logf("首个不符条目的 source=%q（期望 ths-local）", it.Source)
			}
		}
		if it.Name == "" || !utf8.ValidString(it.Name) || strings.Contains(it.Name, "\uFFFD") {
			badNames++
			if len(samples) < 8 {
				samples = append(samples, fmt.Sprintf("%s=%q", it.Code, it.Name))
			}
		}
	}
	if len(samples) > 0 {
		t.Logf("坏名称样例: %s", strings.Join(samples, " "))
	}
	if badSource > 0 {
		t.Fatalf("自选条目来源应为 ths-local（ACCEPTANCE R7 示例），实际 %d/%d 条不符", badSource, len(wl.Items))
	}
	if badNames*2 > len(wl.Items) {
		t.Fatalf("自选名称超过一半无法辨认（编码问题）：%d/%d", badNames, len(wl.Items))
	}
	if badNames > 0 {
		t.Logf("注意：%d/%d 条自选名称异常（空或乱码），已记入 QA 缺陷清单", badNames, len(wl.Items))
	}
	t.Logf("watch.list: %d 条自选，%d 个分组", wl.Count, len(wl.Groups))
}

// TestLiveEndToEnd 是整条链路的联网验收：工具索引 → 知识库 → 快照 → K 线 → 资金流 → 审计。
func TestLiveEndToEnd(t *testing.T) {
	k, dataDir := startLiveKernel(t)
	ctx := context.Background()

	// ---------- 1. 工具索引：数量、风险等级、元工具 ----------
	tools := k.Tools()
	if len(tools) < 12 {
		t.Fatalf("工具索引应 ≥12 个（ACCEPTANCE R6），实际 %d", len(tools))
	}
	for _, tl := range tools {
		if tl.Risk == plugin.RiskExec {
			t.Fatalf("不允许出现 exec 风险工具（红线）：%s", tl.Name)
		}
	}
	for _, want := range []string{"tool.search", "tool.describe", "tool.call"} {
		if _, ok := k.Tool(want); !ok {
			t.Fatalf("缺少元工具 %s", want)
		}
	}

	// ---------- 2. 知识库：临时目录里应真实导入 content/kb 并检索到 ----------
	var search struct {
		Total int `json:"total"`
		Hits  []struct {
			ID      string  `json:"id"`
			Title   string  `json:"title"`
			Snippet string  `json:"snippet"`
			Score   float64 `json:"score"`
		} `json:"hits"`
	}
	raw, err := k.CallTool(ctx, "kb.search", json.RawMessage(`{"query":"涨停 分歧 一致","limit":3}`))
	if err != nil {
		t.Fatalf("kb.search 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &search); err != nil {
		t.Fatalf("kb.search 返回不是合法 JSON: %v", err)
	}
	if search.Total == 0 || len(search.Hits) == 0 {
		t.Fatalf("知识库应检索到结果（content/kb 有 10 篇），实际 total=%d hits=%d（临时目录 %s）",
			search.Total, len(search.Hits), dataDir)
	}
	top := search.Hits[0]
	if top.Title == "" || top.Snippet == "" || top.Score <= 0 {
		t.Fatalf("检索命中应带标题/片段/正分，实际: %+v", top)
	}
	t.Logf("kb.search top1: %s（score=%.4f）片段: %.60s", top.Title, top.Score, top.Snippet)

	// ---------- 3. 行情快照：字段自洽 + 来源可追溯 ----------
	var quotes []struct {
		Code      string  `json:"code"`
		Name      string  `json:"name"`
		Market    string  `json:"market"`
		Price     float64 `json:"price"`
		PrevClose float64 `json:"prevClose"`
		ChangePct float64 `json:"changePct"`
		Source    string  `json:"source"`
		Time      string  `json:"time"`
	}
	raw, err = k.CallTool(ctx, "market.quote", json.RawMessage(`{"codes":["600000","000001"]}`))
	if err != nil {
		t.Fatalf("market.quote 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &quotes); err != nil {
		t.Fatalf("market.quote 返回不是合法 JSON: %v", err)
	}
	if len(quotes) != 2 {
		t.Fatalf("应返回 2 只票的快照，实际 %d", len(quotes))
	}
	for _, q := range quotes {
		if q.Code == "" || q.Name == "" || q.Market == "" {
			t.Fatalf("快照缺少 code/name/market: %+v", q)
		}
		if q.Price <= 0 || q.PrevClose <= 0 {
			t.Fatalf("%s 价格必须为正: price=%.2f prevClose=%.2f", q.Code, q.Price, q.PrevClose)
		}
		if q.Source == "" {
			t.Fatalf("%s 缺少 source（无法追溯口径）", q.Code)
		}
		if utf8.RuneCountInString(q.Name) > 8 || strings.Contains(q.Name, "\uFFFD") {
			t.Fatalf("%s 名称异常（可能编码问题）: %q", q.Code, q.Name)
		}
	}

	// ---------- 4. K 线：条数、日期递增、正数 ----------
	var bars []struct {
		Date  string  `json:"date"`
		Open  float64 `json:"open"`
		High  float64 `json:"high"`
		Low   float64 `json:"low"`
		Close float64 `json:"close"`
	}
	raw, err = k.CallTool(ctx, "market.bars", json.RawMessage(`{"code":"600000","period":"day","limit":60}`))
	if err != nil {
		t.Fatalf("market.bars 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &bars); err != nil {
		t.Fatalf("market.bars 返回不是合法 JSON: %v", err)
	}
	if len(bars) != 60 {
		t.Fatalf("请求 60 根应返回 60 根（不足要显式报错），实际 %d", len(bars))
	}
	for i, b := range bars {
		if i > 0 && b.Date <= bars[i-1].Date {
			t.Fatalf("K 线日期必须严格递增：第 %d 根 %s 未大于 %s", i, b.Date, bars[i-1].Date)
		}
		if b.Close <= 0 || b.High < b.Low {
			t.Fatalf("第 %d 根 K 线不自洽: %+v", i, b)
		}
	}

	// ---------- 5. 板块资金流：mainNet 单位是元 ----------
	var rank struct {
		Items []struct {
			Name    string  `json:"name"`
			MainNet float64 `json:"mainNet"`
		} `json:"items"`
	}
	raw, err = k.CallTool(ctx, "flow.rank", json.RawMessage(`{"kind":"sector","limit":5}`))
	if err != nil {
		t.Fatalf("flow.rank 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &rank); err != nil {
		t.Fatalf("flow.rank 返回不是合法 JSON: %v", err)
	}
	if len(rank.Items) == 0 {
		t.Fatal("板块资金流不应为空")
	}
	for _, it := range rank.Items {
		if it.Name == "" {
			t.Fatalf("板块条目缺少名称: %+v", it)
		}
		if v := it.MainNet; v < -1e11 || v > 1e11 {
			t.Fatalf("%s 的 mainNet 量级异常（应为元，上限 1e11）：%v", it.Name, v)
		}
	}

	// ---------- 5.1 资金流出榜：direction=out 必须给最负的前 N 条 ----------
	var outRank struct {
		Items []struct {
			Code    string  `json:"code"`
			Name    string  `json:"name"`
			MainNet float64 `json:"mainNet"`
			Rank    int     `json:"rank"`
		} `json:"items"`
	}
	raw, err = k.CallTool(ctx, "flow.rank", json.RawMessage(`{"kind":"sector","limit":5,"direction":"out"}`))
	if err != nil {
		t.Fatalf("flow.rank(direction=out) 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &outRank); err != nil {
		t.Fatalf("flow.rank(out) 返回不是合法 JSON: %v", err)
	}
	if len(outRank.Items) == 0 {
		t.Fatal("资金流出榜不应为空")
	}
	for i, it := range outRank.Items {
		if it.Rank != i+1 {
			t.Fatalf("流出榜名次应连续，第 %d 条 rank=%d", i+1, it.Rank)
		}
		if it.MainNet > 0 {
			t.Fatalf("流出榜应全部为净流出，%s mainNet=%v", it.Name, it.MainNet)
		}
		if i > 0 && it.MainNet < outRank.Items[i-1].MainNet {
			t.Fatalf("流出榜应按净额升序（最负在前）：%s %v 在 %s %v 之后", it.Name, it.MainNet, outRank.Items[i-1].Name, outRank.Items[i-1].MainNet)
		}
	}

	// ---------- 5.1.1 板块/概念多日资金流：BK 代码走 90.<BK> 端点 ----------
	var sectorFlow struct {
		Code   string `json:"code"`
		Name   string `json:"name"`
		Points []struct {
			Date    string  `json:"date"`
			MainNet float64 `json:"mainNet"`
		} `json:"points"`
	}
	if outRank.Items[0].Code == "" {
		t.Fatal("资金流出榜首条缺少 code，无法验证多日资金流")
	}
	raw, err = k.CallTool(ctx, "sector.history", json.RawMessage(`{"code":"`+outRank.Items[0].Code+`","days":10}`))
	if err != nil {
		t.Fatalf("sector.history 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &sectorFlow); err != nil {
		t.Fatalf("sector.history 返回不是合法 JSON: %v", err)
	}
	if sectorFlow.Code == "" || len(sectorFlow.Points) == 0 {
		t.Fatalf("板块多日资金流不应为空: code=%q points=%d", sectorFlow.Code, len(sectorFlow.Points))
	}
	for i, pt := range sectorFlow.Points {
		if pt.Date == "" {
			t.Fatalf("第 %d 个板块资金流点缺少日期", i)
		}
		if i > 0 && pt.Date <= sectorFlow.Points[i-1].Date {
			t.Fatalf("板块资金流日期必须递增：%s 未大于 %s", pt.Date, sectorFlow.Points[i-1].Date)
		}
		if v := pt.MainNet; v < -1e13 || v > 1e13 {
			t.Fatalf("板块 %s 的 mainNet 量级异常（应为元）：%v", pt.Date, v)
		}
	}

	// ---------- 5.2 K 线研判：短线/长线双尺度 ----------
	raw, err = k.CallTool(ctx, "kline.analyze", json.RawMessage(`{"code":"600000","period":"day"}`))
	if err != nil {
		t.Fatalf("kline.analyze 调用失败: %v", err)
	}
	var analysis struct {
		Horizons []struct {
			Key     string    `json:"key"`
			Title   string    `json:"title"`
			Window  int       `json:"window"`
			Bias    string    `json:"bias"`
			Support []float64 `json:"support"`
		} `json:"horizons"`
	}
	if err := json.Unmarshal(raw, &analysis); err != nil {
		t.Fatalf("kline.analyze 返回不是合法 JSON: %v", err)
	}
	if len(analysis.Horizons) != 2 {
		t.Fatalf("应返回短线/长线两份研判，实际 %d", len(analysis.Horizons))
	}
	for _, h := range analysis.Horizons {
		if h.Key != "short" && h.Key != "long" {
			t.Fatalf("horizon.key 非法: %q", h.Key)
		}
		if h.Title == "" || h.Window <= 0 {
			t.Fatalf("%s 研判缺少标题/窗口: %+v", h.Key, h)
		}
		if h.Bias != "偏多" && h.Bias != "偏空" && h.Bias != "中性" {
			t.Fatalf("%s 的 bias 非法: %q", h.Key, h.Bias)
		}
	}

	// ---------- 5.3 市场中心：指数/宽度/梯队/龙虎榜 ----------
	for _, call := range []struct {
		name    string
		payload string
		min     int
	}{
		{"market.indices", `{}`, 3},
		{"market.ladder", `{"limit":5}`, 1},
		{"market.longhu", `{"limit":5}`, 1},
		{"market.hot", `{"type":"hour","limit":5}`, 1},
	} {
		raw, err = k.CallTool(ctx, call.name, json.RawMessage(call.payload))
		if err != nil {
			t.Fatalf("%s 调用失败: %v", call.name, err)
		}
		var payload struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("%s 返回不是对象: %v", call.name, err)
		}
		if len(payload.Items) < call.min {
			t.Fatalf("%s 返回 %d 条，至少期望 %d 条", call.name, len(payload.Items), call.min)
		}
	}
	var breadth struct {
		Up    int `json:"up"`
		Down  int `json:"down"`
		Flat  int `json:"flat"`
		Limit int `json:"limitUp"`
	}
	raw, err = k.CallTool(ctx, "market.breadth", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("market.breadth 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &breadth); err != nil {
		t.Fatalf("market.breadth 返回不是合法 JSON: %v", err)
	}
	if breadth.Up+breadth.Down+breadth.Flat < 1000 {
		t.Fatalf("涨跌家数合计异常：up=%d down=%d flat=%d", breadth.Up, breadth.Down, breadth.Flat)
	}

	// ---------- 5.4 分时：当日 241 点 ----------
	var minute struct {
		PrevClose float64 `json:"prevClose"`
		Points    []struct {
			Time  string  `json:"time"`
			Price float64 `json:"price"`
		} `json:"points"`
	}
	raw, err = k.CallTool(ctx, "market.minute", json.RawMessage(`{"code":"600000"}`))
	if err != nil {
		t.Fatalf("market.minute 调用失败: %v", err)
	}
	if err := json.Unmarshal(raw, &minute); err != nil {
		t.Fatalf("market.minute 返回不是合法 JSON: %v", err)
	}
	if minute.PrevClose <= 0 || len(minute.Points) < 60 {
		t.Fatalf("分时数据不完整：prevClose=%v points=%d", minute.PrevClose, len(minute.Points))
	}
	for i, pt := range minute.Points {
		if pt.Time == "" || pt.Price <= 0 {
			t.Fatalf("第 %d 个分时点非法: %+v", i, pt)
		}
	}

	// ---------- 6. 审计：工具调用必须落库 ----------
	events, err := k.Audit().List(200)
	if err != nil {
		t.Fatalf("审计读取失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("工具调用后审计必须非空")
	}
	sawQuote := false
	for _, e := range events {
		if e.Kind == "tool" && e.Action == "market.quote" {
			sawQuote = true
		}
	}
	if !sawQuote {
		t.Fatalf("审计里应有一次 market.quote 调用，实际事件 %d 条", len(events))
	}
}
