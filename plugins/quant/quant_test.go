package quant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

type fakeData struct{}

func (fakeData) Quote(context.Context, []string) ([]contract.Quote, error) { return nil, nil }
func (fakeData) Bars(_ context.Context, code string, _ contract.Period, limit int, adj contract.Adjust) ([]contract.Bar, error) {
	if adj == contract.AdjustQfq {
		return nil, errors.New("前复权不可用")
	}
	if limit > 300 {
		limit = 300
	}
	bars := make([]contract.Bar, 0, limit)
	price := 10.0
	for i := 0; i < limit; i++ {
		price += 0.05
		if i%7 == 0 {
			price -= 0.2
		}
		bars = append(bars, contract.Bar{
			Date: "2026-01-" + pad2(i%28+1),
			Open: price * 0.999, High: price * 1.01, Low: price * 0.99, Close: price, Volume: 10000,
		})
	}
	return bars, nil
}
func (fakeData) StockFundFlow(context.Context, string) (contract.FundFlow, error) {
	return contract.FundFlow{}, nil
}
func (fakeData) FundFlowRank(context.Context, string, int) ([]contract.FundFlow, error) {
	return nil, nil
}
func (fakeData) Sectors(context.Context, string) ([]contract.Sector, error) { return nil, nil }
func (fakeData) Watchlist(context.Context) ([]contract.WatchItem, error)    { return nil, nil }
func (fakeData) SyncTHSWatchlist(context.Context) (contract.SyncResult, error) {
	return contract.SyncResult{}, nil
}
func (fakeData) Search(context.Context, string) ([]contract.StockRef, error) { return nil, nil }
func (fakeData) Health(context.Context) map[string]string                    { return map[string]string{"fake": "ok"} }

func pad2(v int) string {
	if v < 10 {
		return "0" + string(rune('0'+v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}

type memStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func (s *memStore) Get(bucket, key string) ([]byte, error)                { return nil, contract.ErrNotFound }
func (s *memStore) Put(bucket, key string, val []byte) error              { return nil }
func (s *memStore) Delete(bucket, key string) error                       { return nil }
func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) { return nil, nil }
func (s *memStore) Close() error                                          { return nil }

type okGuard struct{}

func (okGuard) AllowHost(string) bool   { return true }
func (okGuard) CheckPath(string) error  { return nil }
func (okGuard) Redact(s string) string  { return s }
func (okGuard) Policy() contract.Policy { return contract.Policy{AllowExec: false} }

type noAudit struct{}

func (noAudit) Record(contract.AuditEvent)              {}
func (noAudit) List(int) ([]contract.AuditEvent, error) { return nil, nil }

func newKernel(t *testing.T) *kernel.Kernel {
	t.Helper()
	k, err := kernel.New(kernel.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:  &memStore{},
		Data:   fakeData{},
		Guard:  okGuard{},
		Audit:  noAudit{},
	})
	if err != nil {
		t.Fatalf("kernel 创建失败: %v", err)
	}
	if err := k.Register(New()); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	return k
}

func TestPluginExposesUIToolAndService(t *testing.T) {
	k := newKernel(t)
	var pv *kernel.PluginView
	for _, p := range k.Plugins() {
		if p.ID == "quant" {
			pv = &p
		}
	}
	if pv == nil || !pv.Active {
		t.Fatalf("quant 插件未激活: %+v", pv)
	}
	if len(pv.UI) != 1 || pv.UI[0].Component != "quant" || pv.UI[0].Order != 45 {
		t.Fatalf("面板声明不符: %+v", pv.UI)
	}
	if len(pv.Tools) != 2 || pv.Tools[0].Name != "quant.backtest" || pv.Tools[1].Name != "quant.optimize" {
		t.Fatalf("工具声明不符: %+v", pv.Tools)
	}
	if _, ok := k.Services().Get("quant.engine"); !ok {
		t.Fatal("缺少 quant.engine 服务")
	}
}

func TestRunMethodReturnsReportWithCosts(t *testing.T) {
	k := newKernel(t)
	out, err := k.Invoke(context.Background(), "quant", "run", json.RawMessage(`{"code":"600000","strategy":"ma","params":{"fast":5,"slow":20}}`))
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("返回不是 JSON: %s", out)
	}
	report, _ := m["report"].(string)
	for _, key := range []string{"佣金", "印花税", "过户费", "滑点"} {
		if !strings.Contains(report, key) {
			t.Fatalf("报告缺少 %q", key)
		}
	}
	// 前复权不可用时必须降级并声明
	if m["adjust"] != "none" {
		t.Fatalf("应降级为不复权, got %v", m["adjust"])
	}
	if w, _ := m["warning"].(string); !strings.Contains(w, "不复权") {
		t.Fatalf("应带复权降级提示, got %q", w)
	}
}

func TestStrategiesAndWalkForward(t *testing.T) {
	k := newKernel(t)
	out, err := k.Invoke(context.Background(), "quant", "strategies", nil)
	if err != nil {
		t.Fatalf("strategies 失败: %v", err)
	}
	for _, key := range []string{"均线交叉", "formula", "rules", "T+1", "佣金", "复权", "costs"} {
		if !strings.Contains(string(out), key) {
			t.Fatalf("策略清单缺少 %q: %s", key, out)
		}
	}
	out, err = k.Invoke(context.Background(), "quant", "walkforward", json.RawMessage(`{"code":"600000","strategy":"ma","folds":3}`))
	if err != nil {
		t.Fatalf("walkforward 失败: %v", err)
	}
	if !strings.Contains(string(out), "样本外") {
		t.Fatalf("缺少样本外结论: %s", out)
	}
}

func TestToolToleratesEmptyArgsAndUnknownStrategy(t *testing.T) {
	k := newKernel(t)
	if _, err := k.CallTool(context.Background(), "quant.backtest", json.RawMessage(`{}`)); err == nil {
		t.Fatal("缺少 code 应报错")
	} else if !strings.Contains(err.Error(), "股票代码") {
		t.Fatalf("错误信息不够明确: %v", err)
	}
	out, err := k.CallTool(context.Background(), "quant.backtest", json.RawMessage(`{"code":"600000","strategy":"unknown"}`))
	if err != nil {
		t.Fatalf("未知策略应回落到买入持有: %v", err)
	}
	if !strings.Contains(string(out), "买入持有") {
		t.Fatalf("回落策略不符: %s", out)
	}
}

// TestRunReturnsPerFillTrades 覆盖契约 §3.7.1：
// 顶层 trades 是逐笔成交（可画买卖点），result.trades 仍是往返交易，两不相扰。
func TestRunReturnsPerFillTrades(t *testing.T) {
	k := newKernel(t)
	out, err := k.Invoke(context.Background(), "quant", "run", json.RawMessage(`{"code":"600000","strategy":"ma","params":{"fast":3,"slow":8}}`))
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	var payload struct {
		Trades []struct {
			Date   string  `json:"date"`
			Side   string  `json:"side"`
			Price  float64 `json:"price"`
			Qty    int     `json:"qty"`
			Reason string  `json:"reason"`
		} `json:"trades"`
		Result struct {
			Fills  []map[string]any `json:"fills"`
			Trades []map[string]any `json:"trades"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("返回不是 JSON: %s", out)
	}
	if len(payload.Trades) == 0 {
		t.Fatalf("顶层 trades 为空（回测应至少产生一次成交）: %s", out)
	}
	for _, fill := range payload.Trades {
		if fill.Side != "buy" && fill.Side != "sell" {
			t.Fatalf("side 非法: %+v", fill)
		}
		if fill.Date == "" || fill.Price <= 0 || fill.Qty <= 0 {
			t.Fatalf("成交字段不完整: %+v", fill)
		}
	}
	if len(payload.Result.Fills) != len(payload.Trades) {
		t.Fatalf("result.fills 与顶层 trades 应是同一份数据: %d vs %d", len(payload.Result.Fills), len(payload.Trades))
	}
	if len(payload.Result.Trades) == 0 {
		t.Fatal("result.trades（往返交易）不应为空")
	}
	if _, ok := payload.Result.Trades[0]["entryDate"]; !ok {
		t.Fatalf("result.trades 的往返语义被改了: %v", payload.Result.Trades[0])
	}
}

// TestOptimizeMethodAndTool 覆盖参数优化方法/工具：候选非空、报告含稳健性与样本外、不返回净值序列。
func TestOptimizeMethodAndTool(t *testing.T) {
	k := newKernel(t)
	out, err := k.Invoke(context.Background(), "quant", "optimize", json.RawMessage(`{"code":"600000","strategy":"ma","folds":3}`))
	if err != nil {
		t.Fatalf("optimize 失败: %v", err)
	}
	var payload struct {
		Result struct {
			Candidates []struct {
				Params    map[string]any `json:"params"`
				Stability float64        `json:"stability"`
			} `json:"candidates"`
			Best        map[string]any `json:"best"`
			WalkForward struct {
				Verdict string `json:"verdict"`
			} `json:"walkforward"`
			Report string `json:"report"`
			Note   string `json:"note"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("返回不是 JSON: %s", out)
	}
	if len(payload.Result.Candidates) == 0 {
		t.Fatalf("候选为空: %s", out)
	}
	if payload.Result.Best == nil {
		t.Fatalf("缺少推荐参数: %s", out)
	}
	for _, c := range payload.Result.Candidates {
		if c.Stability < 0 || c.Stability > 1 {
			t.Fatalf("稳健性越界: %+v", c)
		}
	}
	if payload.Result.WalkForward.Verdict == "" {
		t.Fatalf("缺少 walk-forward 结论: %s", out)
	}
	for _, key := range []string{"推荐参数", "邻域稳健性", "Walk-forward", "警告"} {
		if !strings.Contains(payload.Result.Report, key) {
			t.Fatalf("报告缺少 %q", key)
		}
	}
	if strings.Contains(string(out), "equity") {
		t.Fatalf("参数优化不应返回净值序列: %s", out)
	}

	out, err = k.CallTool(context.Background(), "quant.optimize", json.RawMessage(`{"code":"600000","strategy":"rsi","folds":2,"limit":200}`))
	if err != nil {
		t.Fatalf("quant.optimize 工具失败: %v", err)
	}
	if !strings.Contains(string(out), "report") || !strings.Contains(string(out), "candidates") {
		t.Fatalf("工具返回缺少摘要或报告: %s", out)
	}
}
