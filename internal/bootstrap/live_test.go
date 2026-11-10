//go:build live

// 真实网络冒烟测试（需要外网）。默认不跑，用 go test -tags live 触发：
//
//	go test -tags live ./internal/bootstrap/ -run TestLive -v
//
// 断言只检查"形状"（非空、正数、字段合理），不做数值断言——避免把行情波动写进测试。
package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

func liveKernel(t *testing.T) (contract.DataService, func()) {
	t.Helper()
	cfg := Config{DataDir: t.TempDir(), LogLevel: "error"}
	k, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("bootstrap 失败: %v", err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	return k.Data(), func() { _ = k.Stop(context.Background()) }
}

func TestLiveQuote(t *testing.T) {
	data, stop := liveKernel(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	quotes, err := data.Quote(ctx, []string{"600000", "000001"})
	if err != nil {
		t.Fatalf("行情获取失败: %v", err)
	}
	if len(quotes) == 0 {
		t.Fatal("没有返回行情")
	}
	for _, q := range quotes {
		if q.Code == "" || q.Name == "" {
			t.Fatalf("行情缺字段: %+v", q)
		}
		if q.Price <= 0 && !q.Stale {
			t.Fatalf("现价异常且未标记 stale: %+v", q)
		}
		t.Logf("%s %s 现价=%.2f 涨跌幅=%.2f%% 量=%.0f手 来源=%s stale=%v",
			q.Code, q.Name, q.Price, q.ChangePct, q.Volume, q.Source, q.Stale)
	}
}

func TestLiveBars(t *testing.T) {
	data, stop := liveKernel(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bars, err := data.Bars(ctx, "600000", contract.PeriodDay, 30, contract.AdjustNone)
	if err != nil {
		t.Fatalf("K 线获取失败: %v", err)
	}
	if len(bars) < 10 {
		t.Fatalf("K 线数量偏少: %d", len(bars))
	}
	for _, b := range bars {
		if b.Date == "" || b.Close <= 0 || b.Open <= 0 || b.High < b.Low {
			t.Fatalf("K 线数据异常: %+v", b)
		}
	}
	t.Logf("拿到 %d 根：%s ~ %s，最后一根收 %.2f", len(bars), bars[0].Date, bars[len(bars)-1].Date, bars[len(bars)-1].Close)
}

func TestLiveSectorFundFlow(t *testing.T) {
	data, stop := liveKernel(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	flows, err := data.FundFlowRank(ctx, "sector", 5)
	if err != nil {
		t.Fatalf("板块资金流失败: %v", err)
	}
	if len(flows) == 0 {
		t.Fatal("没有板块资金流数据")
	}
	for _, f := range flows {
		if f.Name == "" {
			t.Fatalf("板块缺名称: %+v", f)
		}
	}
	t.Logf("板块资金流前 5：%s 主力 %.2f 亿", flows[0].Name, flows[0].MainNet/1e8)
}

func TestLiveStockFundFlow(t *testing.T) {
	data, stop := liveKernel(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f, err := data.StockFundFlow(ctx, "600000")
	if err != nil {
		t.Fatalf("个股资金流失败: %v", err)
	}
	if f.Code != "600000" {
		t.Fatalf("代码不符: %+v", f)
	}
	t.Logf("600000 主力净额 %.2f 万，超大单 %.2f 万", f.MainNet/1e4, f.SuperNet/1e4)
}

func TestLiveHealth(t *testing.T) {
	data, stop := liveKernel(t)
	defer stop()
	h := data.Health(context.Background())
	if len(h) == 0 {
		t.Fatal("Health 为空")
	}
	for k, v := range h {
		t.Logf("健康 %s = %s", k, v)
	}
}
