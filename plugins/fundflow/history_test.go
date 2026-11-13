package fundflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// TestHistoryDefaultsAndClamp 覆盖 history 的默认值、上限与入参校验。
func TestHistoryDefaultsAndClamp(t *testing.T) {
	data := &fakeData{history: contract.FundFlowHistory{
		Name:   "贵州茅台",
		Source: "sina",
		Points: []contract.FundFlowPoint{{Date: "2026-11-10", MainNet: 99230912, Close: 1439}},
	}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	history, _ := ctx.Methods.Get("history")

	out, err := history.Fn(context.Background(), json.RawMessage(`{"code":"600519"}`))
	if err != nil {
		t.Fatalf("history 失败: %v", err)
	}
	got := out.(contract.FundFlowHistory)
	if data.lastDays != 20 || got.Days != 20 {
		t.Fatalf("默认天数 = %d/%d，期望 20", data.lastDays, got.Days)
	}
	if got.Code != "600519" || got.Name != "贵州茅台" || len(got.Points) != 1 {
		t.Fatalf("结果 = %+v", got)
	}
	if got.Points[0].Close != 1439 {
		t.Fatalf("收盘价丢了: %+v", got.Points[0])
	}

	if _, err := history.Fn(context.Background(), json.RawMessage(`{"code":"600519","days":999}`)); err != nil {
		t.Fatal(err)
	}
	if data.lastDays != 120 {
		t.Fatalf("days 上限 = %d，期望 120", data.lastDays)
	}

	for _, payload := range []string{`{}`, `{"code":"  "}`} {
		if _, err := history.Fn(context.Background(), json.RawMessage(payload)); err == nil {
			t.Fatalf("%s 应报错", payload)
		}
	}
}

// TestHistoryDegradesWithoutService 保证数据层没实现扩展时给中文提示而不是 panic。
func TestHistoryDegradesWithoutService(t *testing.T) {
	p := New()
	ctx := newTestContext(t, nil)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	history, _ := ctx.Methods.Get("history")
	_, err := history.Fn(context.Background(), json.RawMessage(`{"code":"600519"}`))
	if err == nil || !strings.Contains(err.Error(), "版本不匹配") {
		t.Fatalf("应给出可读的降级说明，实际 %v", err)
	}
	batch, _ := ctx.Methods.Get("batch")
	if _, err := batch.Fn(context.Background(), json.RawMessage(`{"codes":["600519"]}`)); err == nil || !strings.Contains(err.Error(), "版本不匹配") {
		t.Fatalf("batch 降级说明不对: %v", err)
	}
}

// TestBatchNormalizesAndReturns 覆盖去重、上限与返回结构。
func TestBatchNormalizesAndReturns(t *testing.T) {
	data := &fakeData{snapshot: []contract.FundFlow{
		{Code: "600519", Name: "贵州茅台", Date: "2026-11-10", MainNet: 99230912, Source: "sina"},
		{Code: "000858", Name: "五粮液", Date: "2026-11-10", MainNet: -210964789, Source: "sina"},
	}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	batch, _ := ctx.Methods.Get("batch")

	out, err := batch.Fn(context.Background(), json.RawMessage(`{"codes":["600519"," 600519 ","000858"]}`))
	if err != nil {
		t.Fatalf("batch 失败: %v", err)
	}
	reply := out.(batchReply)
	if len(reply.Items) != 2 || reply.Date != "2026-11-10" || reply.Source != "sina" {
		t.Fatalf("返回 = %+v", reply)
	}
	if len(data.lastCodes) != 2 || data.lastCodes[0] != "600519" || data.lastCodes[1] != "000858" {
		t.Fatalf("去重后的入参 = %v", data.lastCodes)
	}

	if _, err := batch.Fn(context.Background(), json.RawMessage(`{"codes":[]}`)); err == nil {
		t.Fatal("空 codes 应报错")
	}
	many := make([]string, 0, maxBatchCodes+1)
	for i := 0; i <= maxBatchCodes; i++ {
		many = append(many, fmt.Sprintf("6000%02d", i))
	}
	payload, err := json.Marshal(map[string]any{"codes": many})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.Fn(context.Background(), json.RawMessage(payload)); err == nil || !strings.Contains(err.Error(), "50") {
		t.Fatalf("超过上限应报错，实际 %v", err)
	}
}

// TestBatchKeepsPartialItems 保证「部分失败但有成功项」时不整批报错。
func TestBatchKeepsPartialItems(t *testing.T) {
	data := &fakeData{
		snapshot:    []contract.FundFlow{{Code: "600519", Date: "2026-11-10", MainNet: 1}},
		snapshotErr: errors.New("datasource: 部分代码取数失败"),
	}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	batch, _ := ctx.Methods.Get("batch")
	out, err := batch.Fn(context.Background(), json.RawMessage(`{"codes":["600519","600001"]}`))
	if err != nil {
		t.Fatalf("有成功项时不应整批失败: %v", err)
	}
	if items := out.(batchReply).Items; len(items) != 1 {
		t.Fatalf("成功项被吞了: %+v", items)
	}

	// 一条都没成功时才是真失败
	data.snapshot = nil
	if _, err := batch.Fn(context.Background(), json.RawMessage(`{"codes":["600519"]}`)); err == nil {
		t.Fatal("全失败应报错")
	}
}

// TestToolFlowHistory 保证工具层与方法是同一份行为，且 Summary 不超 40 字（L1 披露要求）。
func TestToolFlowHistory(t *testing.T) {
	data := &fakeData{history: contract.FundFlowHistory{Name: "贵州茅台"}}
	p := New()
	ctx := newTestContext(t, data)
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	tool, ok := ctx.Tools.Get("flow.history")
	if !ok {
		t.Fatal("缺少工具 flow.history")
	}
	if runes := []rune(tool.Summary); len(runes) > 40 {
		t.Fatalf("Summary 超 40 字: %d", len(runes))
	}
	if tool.Risk != "network" {
		t.Fatalf("flow.history 需要联网，risk = %q", tool.Risk)
	}
	out, err := tool.Run(context.Background(), json.RawMessage(`{"code":"600519","days":10}`))
	if err != nil {
		t.Fatalf("工具执行失败: %v", err)
	}
	history := out.(contract.FundFlowHistory)
	if history.Code != "600519" || data.lastDays != 10 {
		t.Fatalf("工具未透传参数: %+v days=%d", history, data.lastDays)
	}
}
