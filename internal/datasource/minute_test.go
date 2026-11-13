package datasource

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// TestParseTencentMinuteFallback 覆盖降级源：累计量差分成分钟量、13:00 跳过、15:00 后截断。
func TestParseTencentMinuteFallback(t *testing.T) {
	body := []byte(`{
		"code":0,
		"data":{
			"sh600519":{
				"data":{
					"data":[
						"0930 10.00 100 100000.00",
						"0931 10.10 300 302000.00",
						"1130 10.20 500 505000.00",
						"1300 10.30 900 909000.00",
						"1301 10.40 1000 1013000.00",
						"1500 10.50 1200 1217000.00",
						"1501 10.50 1300 1317000.00"
					],
					"date":"20261111"
				},
				"qt":{"sh600519":["1","贵州茅台","600519","10.50","10.00","10.00"]}
			}
		}
	}`)
	data, err := parseTencentMinute(body, "sh600519")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if data.Name != "贵州茅台" || data.Date != "2026-11-11" || math.Abs(data.PrevClose-10) > 1e-9 {
		t.Fatalf("元信息 = %+v", data)
	}
	if len(data.Points) != 5 {
		t.Fatalf("点数 = %d，期望 5（13:00 与 15:01 都不输出）", len(data.Points))
	}
	wants := []struct {
		time   string
		price  float64
		volume float64
		amount float64
	}{
		{"09:30", 10.00, 100, 100000},
		{"09:31", 10.10, 200, 202000},
		{"11:30", 10.20, 200, 203000},
		{"13:01", 10.40, 100, 104000},
		{"15:00", 10.50, 200, 204000},
	}
	for i, want := range wants {
		got := data.Points[i]
		if got.Time != want.time || math.Abs(got.Price-want.price) > 1e-9 ||
			math.Abs(got.Volume-want.volume) > 1e-9 || math.Abs(got.Amount-want.amount) > 1e-9 {
			t.Fatalf("第 %d 点 = %+v，期望 %+v", i, got, want)
		}
		if got.Avg <= 0 {
			t.Fatalf("第 %d 点缺均价: %+v", i, got)
		}
	}
	// 均价 = 累计成交额 / (累计成交量 × 100)
	if math.Abs(data.Points[1].Avg-302000.0/30000.0) > 1e-9 {
		t.Fatalf("均价 = %v，期望 %v", data.Points[1].Avg, 302000.0/30000.0)
	}
}

func TestParseTencentMinuteErrors(t *testing.T) {
	for _, body := range []string{"", "<html>", `{"code":0}`, `{"code":0,"data":{}}`, `{"code":0,"data":{"sh600519":{"data":{"data":[]}}}}`} {
		if _, err := parseTencentMinute([]byte(body), "sh600519"); err == nil {
			t.Fatalf("%q 应报错", body)
		}
	}
}

// TestServiceMinutePrimaryAndCache 覆盖服务层：腾讯主链路、字段归一、60s 缓存。
func TestServiceMinutePrimaryAndCache(t *testing.T) {
	transport := (&fakeTransport{}).add("appstock/app/minute/query", tencentMinuteFixture())
	service, clock := newServiceWithTransport(t, newMemStore(), transport)

	series, err := service.Minute(context.Background(), "600000")
	if err != nil {
		t.Fatalf("取分时失败: %v", err)
	}
	if series.Code != "600000" || series.Name != "浦发银行" || series.Source != sourceTencent {
		t.Fatalf("元信息 = %+v", series)
	}
	if series.Date != "2026-11-11" || math.Abs(series.PrevClose-10.00) > 1e-9 {
		t.Fatalf("日期/昨收 = %q/%v", series.Date, series.PrevClose)
	}
	if len(series.Points) != 3 {
		t.Fatalf("点数 = %d，期望 3", len(series.Points))
	}
	// 第二次命中缓存
	if _, err := service.Minute(context.Background(), "sh600000"); err != nil {
		t.Fatal(err)
	}
	if calls := transport.countCalls("appstock/app/minute/query"); calls != 1 {
		t.Fatalf("缓存未命中：请求数 = %d，期望 1", calls)
	}
	// 60s 后过期，重新取数
	clock.Advance(61 * time.Second)
	if _, err := service.Minute(context.Background(), "600000"); err != nil {
		t.Fatal(err)
	}
	if calls := transport.countCalls("appstock/app/minute/query"); calls != 2 {
		t.Fatalf("缓存过期后请求数 = %d，期望 2", calls)
	}
}

// TestServiceMinuteFallbackToSina 覆盖腾讯不可用时降级到新浪 1 分钟 K 线。
func TestServiceMinuteFallbackToSina(t *testing.T) {
	transport := (&fakeTransport{}).
		add("CN_MarketData.getKLineData", readTestdata(t, "sina_kline_1min_600000.json"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	series, err := service.Minute(context.Background(), "sh600000")
	if err != nil {
		t.Fatalf("新浪兜底失败: %v", err)
	}
	if series.Source != sourceSina {
		t.Fatalf("来源 = %q，期望 %q", series.Source, sourceSina)
	}
	if series.Date == "" || len(series.Points) == 0 {
		t.Fatalf("兜底结果为空: %+v", series)
	}
	for _, point := range series.Points {
		if point.Price <= 0 {
			t.Fatalf("兜底价格必须为正: %+v", point)
		}
	}
}

func TestServiceMinuteRejectsBadInput(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	for _, code := range []string{"", "abc", "not-a-code"} {
		if _, err := service.Minute(context.Background(), code); err == nil {
			t.Fatalf("%q 应报错", code)
		}
	}
	if _, err := service.Minute(context.Background(), "BK1036"); err == nil || !strings.Contains(err.Error(), "板块") {
		t.Fatalf("板块代码应给出可操作提示，实际 %v", err)
	}
}

// TestServiceMinuteNoData 两条链路都不可用时必须返回 ErrNoData + 中文可操作提示。
func TestServiceMinuteNoData(t *testing.T) {
	service, _ := newServiceWithTransport(t, newMemStore(), &fakeTransport{})
	_, err := service.Minute(context.Background(), "600519")
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("错误 = %v，期望 ErrNoData", err)
	}
	if !strings.Contains(err.Error(), "分时") || !strings.Contains(err.Error(), "停牌") {
		t.Fatalf("错误提示不够可操作: %v", err)
	}
}

// tencentMinuteFixture 是一份最小可用的腾讯 minute 响应（累计量 -> 差分后 3 个点）。
func tencentMinuteFixture() []byte {
	return []byte(`{"code":0,"data":{"sh600000":{"data":{"data":["0930 10.00 100 100000.00","0931 10.10 300 302000.00","1500 10.50 1200 1217000.00"],"date":"20261111"},"qt":{"sh600000":["1","浦发银行","600000","10.50","10.00","10.00"]}}}}`)
}
