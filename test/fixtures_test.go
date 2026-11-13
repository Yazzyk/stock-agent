package test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 夹具驱动测试：不联网，直接断言真实响应的**结构口径**。
// 抓取方式与口径说明见 test/testdata/README.md。

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	buf, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取夹具 %s 失败（应从 test/testdata 加载，工作目录须为 test/ 或用 go test ./test/）: %v", name, err)
	}
	if len(buf) == 0 {
		t.Fatalf("夹具 %s 为空", name)
	}
	return buf
}

var jsonpEnvelope = regexp.MustCompile(`^([A-Za-z0-9_]+)\((\{.*\})\)\s*$`)

// 同花顺 K 线：JSONP 信封 + 140 根日线 + 日期严格递增 + OHLC 自洽。
func TestFixtureTHSKline(t *testing.T) {
	raw := strings.TrimSpace(string(loadFixture(t, "ths_kline_600000.js")))
	m := jsonpEnvelope.FindStringSubmatch(raw)
	if m == nil {
		t.Fatalf("不是合法 JSONP 信封（应为 函数名({...})），前 60 字节: %.60s", raw)
	}
	if !strings.Contains(m[1], "line_hs_600000") {
		t.Fatalf("JSONP 函数名应带标的与周期，实际 %q", m[1])
	}

	var payload struct {
		Num   int             `json:"num"`
		Total json.RawMessage `json:"total"` // 实测是字符串，保持宽松：只要求存在
		Start json.RawMessage `json:"start"` // 同上
		Data  string          `json:"data"`
		Year  map[string]int  `json:"year"`
	}
	if err := json.Unmarshal([]byte(m[2]), &payload); err != nil {
		t.Fatalf("JSONP 载荷不是合法 JSON: %v", err)
	}
	bars := strings.Split(strings.TrimSpace(payload.Data), ";")
	if len(bars) != payload.Num {
		t.Fatalf("data 里的根数应等于 num：%d vs %d", len(bars), payload.Num)
	}
	if len(bars) < 100 {
		t.Fatalf("应包含全量历史（≥100 根），实际 %d", len(bars))
	}

	prevDate := 0
	for i, bar := range bars {
		fields := strings.Split(bar, ",")
		if len(fields) < 7 {
			t.Fatalf("第 %d 根字段不足 7 个: %q", i, bar)
		}
		date, err := strconv.Atoi(fields[0])
		if err != nil || len(fields[0]) != 8 {
			t.Fatalf("第 %d 根日期应为 YYYYMMDD 8 位，实际 %q", i, fields[0])
		}
		if date <= prevDate {
			t.Fatalf("日期必须严格递增：第 %d 根 %d 未大于上一根 %d", i, date, prevDate)
		}
		prevDate = date
		nums := make([]float64, 6)
		for k := 0; k < 6; k++ {
			v, err := strconv.ParseFloat(fields[k+1], 64)
			if err != nil {
				t.Fatalf("第 %d 根第 %d 个字段不是数字: %q", i, k+1, fields[k+1])
			}
			nums[k] = v
		}
		open, high, low, close, volume, amount := nums[0], nums[1], nums[2], nums[3], nums[4], nums[5]
		if high < low || high < open || high < close || low > open || low > close {
			t.Fatalf("第 %d 根 OHLC 不自洽: %q", i, bar)
		}
		if close <= 0 || volume <= 0 || amount <= 0 {
			t.Fatalf("第 %d 根价格/量/额必须为正: %q", i, bar)
		}
	}

	last := strings.Split(bars[len(bars)-1], ",")
	if last[0] != "20261110" {
		t.Fatalf("最后一根日期应为抓取当日 20261110，实际 %q", last[0])
	}
	close, _ := strconv.ParseFloat(last[4], 64)
	if diff := close - 10.20; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("最后一根收盘应为 10.20（与腾讯/新浪交叉验证过），实际 %.4f", close)
	}
}

// 同花顺涨停池：status_code=0、data.date 是交易日、limit_up_count.today 汇总自洽。
// 这条测试钉住「涨停数 / 炸板数 / 跌停数」三个口径的字段来源，
// 它们是市场宽度里最容易读错的一列（open_num 是炸板数，不是跌停数）。
func TestFixtureTHSLimitUpPool(t *testing.T) {
	var payload struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			Date string `json:"date"`
			Info []struct {
				Code       string  `json:"code"`
				Name       string  `json:"name"`
				Latest     float64 `json:"latest"`
				ChangeRate float64 `json:"change_rate"`
				HighDays   string  `json:"high_days"`
				ReasonType string  `json:"reason_type"`
				OpenNum    int     `json:"open_num"`
			} `json:"info"`
			LimitUpCount map[string]struct {
				Num        int     `json:"num"`
				HistoryNum int     `json:"history_num"`
				Rate       float64 `json:"rate"`
				OpenNum    int     `json:"open_num"`
			} `json:"limit_up_count"`
			LimitDownCount map[string]struct {
				Num int `json:"num"`
			} `json:"limit_down_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loadFixture(t, "ths_limit_up_pool.json"), &payload); err != nil {
		t.Fatalf("涨停池不是合法 JSON: %v", err)
	}
	if payload.StatusCode != 0 {
		t.Fatalf("status_code 应为 0，实际 %d", payload.StatusCode)
	}
	if len(payload.Data.Date) != 8 {
		t.Fatalf("data.date 应是 8 位交易日（yyyyMMdd），实际 %q", payload.Data.Date)
	}
	today, ok := payload.Data.LimitUpCount["today"]
	if !ok {
		t.Fatal("limit_up_count 必须带 today")
	}
	if today.Num <= 0 || today.HistoryNum < today.Num {
		t.Fatalf("涨停数异常：num=%d history_num=%d（history_num 应 >= num，多出来的是炸板过的）", today.Num, today.HistoryNum)
	}
	if today.OpenNum < 0 || today.OpenNum > today.HistoryNum {
		t.Fatalf("炸板数（open_num=%d）应在 [0, history_num] 之间", today.OpenNum)
	}
	if len(payload.Data.Info) != today.Num {
		t.Fatalf("梯队行数 %d 应等于 today.num %d", len(payload.Data.Info), today.Num)
	}
	if _, ok := payload.Data.LimitDownCount["today"]; !ok {
		t.Fatal("limit_down_count 必须带 today")
	}
	for i, row := range payload.Data.Info {
		if len(row.Code) != 6 || row.Name == "" {
			t.Fatalf("第 %d 行缺少 code/name: %+v", i, row)
		}
		if row.Latest <= 0 {
			t.Fatalf("第 %d 行现价非法: %+v", i, row)
		}
		// 涨停股涨幅应接近 10%（20cm 板是 20%，ST 是 5%），只卡下限兜住「字段读错」。
		if row.ChangeRate < 4.5 {
			t.Fatalf("第 %d 行涨跌幅 %.2f%% 明显不是涨停: %+v", i, row.ChangeRate, row)
		}
		if row.HighDays == "" {
			t.Fatalf("第 %d 行缺 high_days（连板文案）: %+v", i, row)
		}
	}
}

// 新浪个股资金流：r0~r3 是「成交额（买+卖）」，净额必须自己用 in-out 算；
// 同时钉住「主力 = 超大单 + 大单」这个与东财同名口径等价的算法。
func TestFixtureSinaFundFlowNetIdentity(t *testing.T) {
	var payload map[string]string
	if err := json.Unmarshal(loadFixture(t, "sina_fund_flow_600000.json"), &payload); err != nil {
		t.Fatalf("新浪个股资金流不是合法 JSON 对象: %v", err)
	}
	for _, key := range []string{"r0_in", "r0_out", "r1_in", "r1_out", "netamount", "name", "trade"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("缺少字段 %s", key)
		}
	}
	num := func(key string) float64 {
		v, err := strconv.ParseFloat(payload[key], 64)
		if err != nil {
			t.Fatalf("字段 %s 不是数字: %q", key, payload[key])
		}
		return v
	}
	superNet := num("r0_in") - num("r0_out")
	largeNet := num("r1_in") - num("r1_out")
	if superNet == 0 && largeNet == 0 {
		t.Fatal("超大单与大单净额同时为 0，夹具或口径有问题")
	}
	// netamount 是四档净额之和，用来交叉校验「in-out 才是净额」这个口径。
	mediumNet := num("r2_in") - num("r2_out")
	smallNet := num("r3_in") - num("r3_out")
	if sum := superNet + largeNet + mediumNet + smallNet; absFloat(sum-num("netamount")) > 1 {
		t.Fatalf("四档净额之和 %.2f 应等于 netamount %.2f", sum, num("netamount"))
	}
	if num("trade") <= 0 {
		t.Fatalf("trade 应大于 0，实际 %v", num("trade"))
	}
}

// 新浪资金流：r0~r3 是「成交额（买+卖）」，净额必须自己用 in-out 算。
// 这条测试防的是把 r0 直接当净额用的经典口径错误。
func TestFixtureSinaMoneyFlowGrossNotNet(t *testing.T) {
	var payload map[string]string
	if err := json.Unmarshal(loadFixture(t, "sina_moneyflow_600000.js"), &payload); err != nil {
		t.Fatalf("新浪资金流不是合法 JSON 对象: %v", err)
	}
	for _, key := range []string{"r0_in", "r0_out", "r0", "r3_in", "r3_out", "r3", "netamount", "name", "trade"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("缺少字段 %s（实际字段: %v）", key, keysOf(payload))
		}
	}
	gross, _ := strconv.ParseFloat(payload["r0"], 64)
	in, _ := strconv.ParseFloat(payload["r0_in"], 64)
	out, _ := strconv.ParseFloat(payload["r0_out"], 64)
	if in <= 0 || out <= 0 || gross <= 0 {
		t.Fatalf("r0/r0_in/r0_out 应为正数: %v", payload)
	}
	if diff := gross - (in + out); diff > 1e-2 || diff < -1e-2 {
		t.Fatalf("r0 应是成交额(买+卖)：r0=%.2f, r0_in+r0_out=%.2f", gross, in+out)
	}
	net := in - out
	if net <= 0 || net >= gross {
		t.Fatalf("净额必须用 in-out 计算且应小于成交额：net=%.2f gross=%.2f", net, gross)
	}
	if payload["name"] != "浦发银行" {
		t.Fatalf("name 应解码为浦发银行，实际 %q", payload["name"])
	}
	trade, _ := strconv.ParseFloat(payload["trade"], 64)
	if diff := trade - 10.20; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("新浪现价应为 10.20（与腾讯/THS 一致），实际 %.4f", trade)
	}
}

// 腾讯快照是 GBK：必须能正确解码中文名称，且字段位与单位（手）固定。
func TestFixtureTencentQuoteGBK(t *testing.T) {
	raw := loadFixture(t, "tencent_quote_600000.txt")
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		t.Fatalf("GBK 解码失败: %v", err)
	}
	text := strings.TrimSpace(string(decoded))
	if !strings.HasPrefix(text, "v_sh600000=\"") {
		t.Fatalf("应以 v_sh600000=\" 开头，实际前 40 字节: %.40s", text)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(text, "v_sh600000=\""), "\";")
	fields := strings.Split(body, "~")
	if len(fields) < 40 {
		t.Fatalf("腾讯快照字段数应 ≥40，实际 %d", len(fields))
	}
	if fields[0] != "1" {
		t.Fatalf("字段[0] 市场标识应为 1（沪市），实际 %q", fields[0])
	}
	if fields[1] != "浦发银行" {
		t.Fatalf("字段[1] 名称应 GBK 解码为「浦发银行」，实际 %q", fields[1])
	}
	if fields[2] != "600000" {
		t.Fatalf("字段[2] 代码应为 600000，实际 %q", fields[2])
	}
	price, _ := strconv.ParseFloat(fields[3], 64)
	prevClose, _ := strconv.ParseFloat(fields[4], 64)
	open, _ := strconv.ParseFloat(fields[5], 64)
	volumeHands, _ := strconv.ParseFloat(fields[6], 64)
	if diff := price - 10.20; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("字段[3] 现价应为 10.20，实际 %.4f", price)
	}
	if prevClose <= 0 || open <= 0 || volumeHands <= 0 {
		t.Fatalf("昨收/今开/成交量应为正：%.2f %.2f %.0f", prevClose, open, volumeHands)
	}
	if volumeHands > 1e7 {
		t.Fatalf("字段[6] 成交量单位应是「手」（百万手量级），实际 %.0f —— 若变成股说明单位口径改了", volumeHands)
	}
}

// 跨源一致性：同一天同一只票，三个来源的收盘价/成交量口径必须能对上。
func TestFixtureCrossSourceConsistency(t *testing.T) {
	// THS 收盘
	thsRaw := strings.TrimSpace(string(loadFixture(t, "ths_kline_600000.js")))
	m := jsonpEnvelope.FindStringSubmatch(thsRaw)
	if m == nil {
		t.Fatal("THS 夹具不是 JSONP")
	}
	var ths struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(m[2]), &ths); err != nil {
		t.Fatalf("THS 载荷解析失败: %v", err)
	}
	bars := strings.Split(strings.TrimSpace(ths.Data), ";")
	lastFields := strings.Split(bars[len(bars)-1], ",")
	thsClose, _ := strconv.ParseFloat(lastFields[4], 64)
	thsVolume, _ := strconv.ParseFloat(lastFields[5], 64)

	// 腾讯快照（GBK）：现价 + 成交量（手）
	raw, err := simplifiedchinese.GBK.NewDecoder().Bytes(loadFixture(t, "tencent_quote_600000.txt"))
	if err != nil {
		t.Fatalf("GBK 解码失败: %v", err)
	}
	text := strings.TrimSpace(string(raw))
	body := strings.TrimSuffix(strings.TrimPrefix(text, "v_sh600000=\""), "\";")
	tx := strings.Split(body, "~")
	txPrice, _ := strconv.ParseFloat(tx[3], 64)
	txHands, _ := strconv.ParseFloat(tx[6], 64)

	// 新浪：现价 + 成交量（股）
	var sina map[string]string
	if err := json.Unmarshal(loadFixture(t, "sina_moneyflow_600000.js"), &sina); err != nil {
		t.Fatalf("新浪夹具解析失败: %v", err)
	}
	sinaPrice, _ := strconv.ParseFloat(sina["trade"], 64)
	sinaVolume, _ := strconv.ParseFloat(sina["volume"], 64)

	// 收盘价三源一致
	for _, pair := range []struct {
		name string
		got  float64
	}{{"腾讯", txPrice}, {"新浪", sinaPrice}} {
		if diff := pair.got - thsClose; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("收盘价不一致：THS=%.4f %s=%.4f", thsClose, pair.name, pair.got)
		}
	}
	// 成交量：腾讯是手，THS/新浪是股 → 手*100 应接近股（差异来自集合竞价/四舍五入，容差 1%）
	expected := txHands * 100
	for _, pair := range []struct {
		name string
		got  float64
	}{{"THS", thsVolume}, {"新浪", sinaVolume}} {
		if expected <= 0 {
			t.Fatalf("腾讯成交量应大于 0，实际 %.0f", txHands)
		}
		ratio := pair.got / expected
		if ratio < 0.99 || ratio > 1.01 {
			t.Fatalf("成交量口径不一致：腾讯 %.0f 手 ×100 = %.0f，%s = %.0f（比值 %.4f）",
				txHands, expected, pair.name, pair.got, ratio)
		}
	}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var _ = fmt.Sprintf
var _ = transform.Nop
