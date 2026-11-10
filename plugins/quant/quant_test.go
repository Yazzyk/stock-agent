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
			Date:   "2026-01-" + pad2(i%28+1),
			Open:   price * 0.999, High: price * 1.01, Low: price * 0.99, Close: price, Volume: 10000,
		})
	}
	return bars, nil
}
func (fakeData) StockFundFlow(context.Context, string) (contract.FundFlow, error) {
	return contract.FundFlow{}, nil
}
func (fakeData) FundFlowRank(context.Context, string, int) ([]contract.FundFlow, error) { return nil, nil }
func (fakeData) Sectors(context.Context, string) ([]contract.Sector, error)            { return nil, nil }
func (fakeData) Watchlist(context.Context) ([]contract.WatchItem, error)                { return nil, nil }
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

func (s *memStore) Get(bucket, key string) ([]byte, error) { return nil, contract.ErrNotFound }
func (s *memStore) Put(bucket, key string, val []byte) error { return nil }
func (s *memStore) Delete(bucket, key string) error          { return nil }
func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) { return nil, nil }
func (s *memStore) Close() error                                         { return nil }

type okGuard struct{}

func (okGuard) AllowHost(string) bool   { return true }
func (okGuard) CheckPath(string) error  { return nil }
func (okGuard) Redact(s string) string  { return s }
func (okGuard) Policy() contract.Policy { return contract.Policy{AllowExec: false} }

type noAudit struct{}

func (noAudit) Record(contract.AuditEvent)                  {}
func (noAudit) List(int) ([]contract.AuditEvent, error)      { return nil, nil }

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
	if len(pv.Tools) != 1 || pv.Tools[0].Name != "quant.backtest" {
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
	if !strings.Contains(string(out), "均线交叉") {
		t.Fatalf("策略清单不符: %s", out)
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
