package datasource

import (
	"errors"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 腾讯 K 线后复权（hfq）的离线解析测试：正常 / 空 / 畸形。
// 口径：param 末尾 hfq -> data.<symbol>.hfqday；后复权价一定高于同日原始价。
func TestParseTxHfqKlineRealResponse(t *testing.T) {
	bars, err := parseTxHfqKline(readTestdata(t, "tx_fqkline_600000_hfq.json"), "sh600000", contract.PeriodDay)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(bars) == 0 {
		t.Fatal("应解析出 K 线")
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].Date <= bars[i-1].Date {
			t.Fatalf("未按时间升序: %s 之后是 %s", bars[i-1].Date, bars[i].Date)
		}
	}
	// 后复权必须高于原始序列：600000 累计分红送转较多，差异是数量级可见的。
	rawBars, err := parseTxRawKline(readTestdata(t, "tx_fqkline_600000_raw_day.json"), "sh600000", contract.PeriodDay)
	if err != nil {
		t.Fatalf("解析原始序列失败: %v", err)
	}
	if bars[0].Close <= rawBars[0].Close {
		t.Fatalf("后复权收盘 %v 应高于原始收盘 %v", bars[0].Close, rawBars[0].Close)
	}
	// 后复权不等于前复权：拿 qfq fixture 对比，两者不应相等。
	qfqBars, err := parseTxFqKline(readTestdata(t, "tx_fqkline_600000_qfq_day.json"), "sh600000", contract.PeriodDay)
	if err != nil {
		t.Fatalf("解析前复权失败: %v", err)
	}
	if bars[0].Close == qfqBars[0].Close {
		t.Fatalf("后复权与前复权收盘相同（%v），说明取错了键", bars[0].Close)
	}
}

func TestParseTxHfqKlineEmptyAndBad(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"空响应", ""},
		{"空对象", `{}`},
		{"非 JSON", "<html>502</html>"},
		{"data 为空", `{"code":0,"data":{}}`},
		{"缺标的", `{"code":0,"data":{"sh600519":{"hfqday":[["2026-11-10","1","1","1","1","1"]]}}}`},
		{"只有 qfq 键", `{"code":0,"data":{"sh600000":{"qfqday":[["2026-11-10","1","1","1","1","1"]]}}}`},
		{"hfqday 为空数组", `{"code":0,"data":{"sh600000":{"hfqday":[]}}}`},
		{"上游错误码", `{"code":-1,"msg":"bad","data":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTxHfqKline([]byte(tc.body), "sh600000", contract.PeriodDay); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}
	// 关键：只有 qfq 键时不能把 qfq 当 hfq 返回。
	onlyQfq := []byte(`{"code":0,"data":{"sh600000":{"qfqday":[["2026-11-10","1","1.1","1.2","0.9","100"]]}}}`)
	if _, err := parseTxHfqKline(onlyQfq, "sh600000", contract.PeriodDay); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("只有 qfq 键时应返回 ErrEmptyResponse，实际 %v", err)
	}
}

// TestParseTxHfqKlineSkipsMalformedRows 覆盖坏行跳过、好行照用。
func TestParseTxHfqKlineSkipsMalformedRows(t *testing.T) {
	body := []byte(`{"code":0,"data":{"sh600000":{"hfqday":[
		["2026-11-10","137.101","138.302","138.645","136.586","477827.000"],
		["2026-11-11"],
		["2026-11-12","0","0","0","0","0"],
		["2026-11-13","1","2","3","4","5"]
	]}}}`)
	bars, err := parseTxHfqKline(body, "sh600000", contract.PeriodDay)
	if err != nil {
		t.Fatalf("应跳过坏行并返回好行: %v", err)
	}
	if len(bars) != 2 {
		t.Fatalf("好行数 = %d，期望 2", len(bars))
	}
	if bars[0].Date != "2026-11-10" || bars[0].Close != 138.302 {
		t.Fatalf("首根 = %+v", bars[0])
	}
}

// TestTxKlineURLAdjust 覆盖三种复权后缀的 URL 形状（位置参数不能转义）。
func TestTxKlineURLAdjust(t *testing.T) {
	raw, err := txRawKlineURL("sh600000", contract.PeriodDay, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(raw, "param=sh600000,day,,,30,") {
		t.Fatalf("不复权 URL = %q", raw)
	}
	qfq, err := txFqKlineURL("sh600000", contract.PeriodDay, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(qfq, "param=sh600000,day,,,30,qfq") {
		t.Fatalf("前复权 URL = %q", qfq)
	}
	hfq, err := txHfqKlineURL("sh600000", contract.PeriodDay, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(hfq, "param=sh600000,day,,,30,hfq") {
		t.Fatalf("后复权 URL = %q", hfq)
	}
	if strings.Contains(hfq, "%2C") {
		t.Fatalf("逗号不能被转义: %q", hfq)
	}
}
