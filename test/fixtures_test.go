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

// 东财板块资金流：diff[] 带 f12/f14/f62，且 f62 的单位是「元」。
func TestFixtureEastmoneyBkzj(t *testing.T) {
	var payload struct {
		RC   int `json:"rc"`
		Data struct {
			Total int `json:"total"`
			Diff  []struct {
				Code string  `json:"f12"`
				Name string  `json:"f14"`
				Main float64 `json:"f62"`
			} `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loadFixture(t, "em_bkzj_industry.json"), &payload); err != nil {
		t.Fatalf("板块资金流不是合法 JSON: %v", err)
	}
	if payload.RC != 0 {
		t.Fatalf("rc 应为 0，实际 %d", payload.RC)
	}
	if payload.Data.Total < 50 || len(payload.Data.Diff) == 0 {
		t.Fatalf("应返回足够多的板块：total=%d diff=%d", payload.Data.Total, len(payload.Data.Diff))
	}

	var electronics *struct {
		Code string  `json:"f12"`
		Name string  `json:"f14"`
		Main float64 `json:"f62"`
	}
	for i := range payload.Data.Diff {
		item := &payload.Data.Diff[i]
		if item.Code == "" || item.Name == "" {
			t.Fatalf("第 %d 条缺少 code/name: %+v", i, item)
		}
		// 只卡上限：小板块的净额本来就可能只有几十万（元）；
		// 「单位是元而不是万元/亿元」由下面电子板块 ≈92 亿来钉死。
		if abs := absFloat(item.Main); abs > 1e12 {
			t.Fatalf("f62 量级异常（应为元，上限 1e12）：%s %s = %v", item.Code, item.Name, item.Main)
		}
		if item.Name == "电子" {
			electronics = item
		}
	}
	if electronics == nil {
		t.Fatal("夹具里应包含「电子」板块（青否抓取日排名变化则会失败，重抓夹具即可）")
	}
	yiYi := electronics.Main / 1e8
	if yiYi < 90 || yiYi > 94 {
		t.Fatalf("电子板块主力净额换算应为 92 亿量级（单位元），实际 %.2f 亿", yiYi)
	}
}

// 东财个股资金流分档口径：f52 = f55 + f56，且 f53 + f54 = -f52。
func TestFixtureEastmoneyFundFlowIdentity(t *testing.T) {
	var payload struct {
		Data struct {
			Code   string   `json:"code"`
			Name   string   `json:"name"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(loadFixture(t, "em_fflow_600000.json"), &payload); err != nil {
		t.Fatalf("个股资金流不是合法 JSON: %v", err)
	}
	if payload.Data.Code != "600000" || payload.Data.Name == "" {
		t.Fatalf("data 应带 code/name，实际: %+v", payload.Data)
	}
	if len(payload.Data.Klines) == 0 {
		t.Fatal("klines 不能为空")
	}
	fields := strings.Split(payload.Data.Klines[0], ",")
	if len(fields) != 6 {
		t.Fatalf("klines 每根应有 6 个字段（f51,f52,f53,f54,f55,f56），实际 %d: %v", len(fields), fields)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(fields[0]) {
		t.Fatalf("f51 应是 YYYY-MM-DD 日期，实际 %q", fields[0])
	}
	vals := make([]float64, 5)
	for i := range vals {
		v, err := strconv.ParseFloat(fields[i+1], 64)
		if err != nil {
			t.Fatalf("第 %d 个资金字段不是数字: %q", i+1, fields[i+1])
		}
		vals[i] = v
	}
	main, small, medium, large, super := vals[0], vals[1], vals[2], vals[3], vals[4]

	// 主力 = 大单 + 超大单（DOMAIN 2.1）
	if diff := main - (large + super); diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("主力净额应等于大单+超大单：f52=%.2f f55+f56=%.2f", main, large+super)
	}
	// 四档净额相消（净流入总量为 0，浮点/取整允许几元误差）
	if sum := small + medium + large + super; sum > 10 || sum < -10 {
		t.Fatalf("四档净额之和应≈0（互相抵消），实际 %.2f（小%.0f 中%.0f 大%.0f 超大%.0f）", sum, small, medium, large, super)
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
