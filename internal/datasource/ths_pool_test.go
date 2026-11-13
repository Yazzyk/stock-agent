package datasource

import (
	"errors"
	"testing"
)

// 同花顺涨停池 + 新浪板块目录的离线解析测试：正常 / 空 / 畸形。

// TestParseTHSLimitPoolRealResponse 用真实响应钉住「涨停数/炸板数/跌停数」三个口径。
func TestParseTHSLimitPoolRealResponse(t *testing.T) {
	pool, err := parseTHSLimitPool(readTestdata(t, "ths_limit_up_pool.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if pool.Source != sourceTHS {
		t.Fatalf("来源 = %q，期望 %q", pool.Source, sourceTHS)
	}
	if pool.Date == "" {
		t.Fatal("日期不能为空")
	}
	// limit_up_count.today: num 涨停、history_num 曾涨停、open_num 炸板。
	today := pool.LimitUp
	if today <= 0 {
		t.Fatalf("涨停数 = %d，应大于 0", today)
	}
	if pool.Broken < 0 {
		t.Fatalf("炸板数 = %d，不应为负", pool.Broken)
	}
	if pool.LimitUp != today {
		t.Fatalf("涨停数口径不一致: %d", pool.LimitUp)
	}
	if len(pool.Stocks) == 0 {
		t.Fatal("梯队不能为空")
	}
	// 梯队按连板数降序。
	for i := 1; i < len(pool.Stocks); i++ {
		if pool.Stocks[i].Boards > pool.Stocks[i-1].Boards {
			t.Fatalf("梯队未按连板降序: 第 %d 行 %d 板 > 第 %d 行 %d 板",
				i+1, pool.Stocks[i].Boards, i, pool.Stocks[i-1].Boards)
		}
	}
	for _, stock := range pool.Stocks {
		if len(stock.Code) != 6 || stock.Name == "" {
			t.Fatalf("代码/名称不合法: %+v", stock)
		}
		if stock.Price <= 0 || stock.ChangePct < 4.5 {
			t.Fatalf("现价/涨跌幅不合法: %+v", stock)
		}
		if stock.Boards < 1 {
			t.Fatalf("连板数应 >=1: %+v", stock)
		}
		// 同花顺给封单额（order_amount），口径必须标注。
		if stock.AmountKind != "封单额" {
			t.Fatalf("AmountKind = %q，期望 封单额", stock.AmountKind)
		}
	}
}

// TestParseTHSLimitPoolEmptyAndBad 覆盖空响应与畸形输入。
func TestParseTHSLimitPoolEmptyAndBad(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"空响应", ""},
		{"非 JSON", "<html>Nginx forbidden</html>"},
		{"data 为空", `{"status_code":0,"data":null}`},
		{"上游错误码", `{"status_code":1,"status_msg":"参数错误","data":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseTHSLimitPool([]byte(tc.body)); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}
}

// TestParseTHSLimitPoolMalformedRows 覆盖坏行跳过：没有 code/name 的行不进梯队。
func TestParseTHSLimitPoolMalformedRows(t *testing.T) {
	body := []byte(`{"status_code":0,"data":{
		"date":"20261111",
		"info":[
			{"code":"","name":"缺代码","latest":10,"change_rate":10},
			{"code":"600000","name":"","latest":10,"change_rate":10},
			{"code":"600776","name":"东方通信","latest":13.65,"change_rate":9.99,"high_days":"3天2板","high_days_value":131075,"reason_type":"通信"},
			{"code":"301052","name":"首板股","latest":20.0,"change_rate":19.98,"high_days":"首板","latest_unused":0}
		],
		"limit_up_count":{"today":{"num":2,"history_num":3,"rate":0.66,"open_num":1}},
		"limit_down_count":{"today":{"num":0}}
	}}`)
	pool, err := parseTHSLimitPool(body)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(pool.Stocks) != 2 {
		t.Fatalf("好行数 = %d，期望 2（坏行应被跳过）", len(pool.Stocks))
	}
	if pool.LimitUp != 2 || pool.Broken != 1 || pool.LimitDown != 0 {
		t.Fatalf("汇总口径 = 涨停%d/炸板%d/跌停%d，期望 2/1/0", pool.LimitUp, pool.Broken, pool.LimitDown)
	}
	byCode := map[string]LimitUpStock{}
	for _, s := range pool.Stocks {
		byCode[s.Code] = s
	}
	if byCode["600776"].Boards != 2 {
		t.Fatalf("600776 连板 = %d，期望 2（high_days_value 高 16 位）", byCode["600776"].Boards)
	}
	if byCode["301052"].Boards != 1 {
		t.Fatalf("301052 首板 = %d，期望 1", byCode["301052"].Boards)
	}
}

// TestParseSinaHQNodesRealResponse 覆盖新浪板块目录：只取有资金流的两支。
func TestParseSinaHQNodesRealResponse(t *testing.T) {
	entries, err := parseSinaHQNodes(readTestdata(t, "sina_hqnodes.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	industries := entries[KindSector]
	concepts := entries[KindConcept]
	if len(industries) < 30 {
		t.Fatalf("行业数量 = %d，明显偏少", len(industries))
	}
	if len(concepts) < 100 {
		t.Fatalf("概念数量 = %d，明显偏少", len(concepts))
	}
	var foundDzxx bool
	for _, entry := range industries {
		if entry.Code == "new_dzxx" {
			foundDzxx = true
			if entry.Name != "电子信息" {
				t.Fatalf("new_dzxx 名称 = %q，期望 电子信息", entry.Name)
			}
		}
		if entry.Name == "" {
			t.Fatalf("行业名为空: %+v", entry)
		}
	}
	if !foundDzxx {
		t.Fatal("目录里应有 new_dzxx（电子信息），它是最常用的演示板块")
	}
	// 概念必须是 gn_ 前缀。
	for _, entry := range concepts {
		if len(entry.Code) < 3 || entry.Code[:3] != "gn_" {
			t.Fatalf("概念代码应以 gn_ 开头: %+v", entry)
		}
	}
}

// TestParseSinaHQNodesEmptyAndBad 覆盖空与畸形目录。
func TestParseSinaHQNodesEmptyAndBad(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"空响应", ""},
		{"空数组", "[]"},
		{"null", "null"},
		{"非 JSON", "<html></html>"},
		{"没有行业/概念两支", `["行情中心",[["A股",[["其它",[["x","","y"]]]]]]]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSinaHQNodes([]byte(tc.body)); err == nil {
				t.Fatal("应返回错误")
			}
		})
	}
}

// TestParseSinaHQNodesSkipsMalformedBranch 覆盖坏分支跳过、好分支照用。
func TestParseSinaHQNodesSkipsMalformedBranch(t *testing.T) {
	body := []byte(`["行情中心",[["A股",[
		["坏分支",[["没有三元组"]]],
		["新浪行业",[["电子信息","","new_dzxx"],["电力行业","","new_dlhy"]]],
		["概念板块",[["华为汽车","","gn_hwqc"]]]
	]]]]`)
	entries, err := parseSinaHQNodes(body)
	if err != nil {
		t.Fatalf("应能解析出好分支: %v", err)
	}
	if len(entries[KindSector]) != 2 {
		t.Fatalf("行业数 = %d，期望 2", len(entries[KindSector]))
	}
	if len(entries[KindConcept]) != 1 || entries[KindConcept][0].Code != "gn_hwqc" {
		t.Fatalf("概念不符: %+v", entries[KindConcept])
	}
}

// TestNormalizeSectorCategory 覆盖板块代码校验：新浪 category 通过，旧 BK 与纯数字拒绝。
func TestNormalizeSectorCategory(t *testing.T) {
	ok := []string{"new_dzxx", "NEW_DZXX", " new_dzxx ", "gn_hwqc", "new_ysbz"}
	for _, raw := range ok {
		got, err := normalizeSectorCategory(raw)
		if err != nil {
			t.Fatalf("%q 应通过，实际 %v", raw, err)
		}
		if got != normalizeLower(raw) {
			t.Fatalf("%q 归一结果 = %q", raw, got)
		}
	}
	bad := []string{"", "   ", "600519", "BK1201", "90.BK1201", "new-dzxx", "éé"}
	for _, raw := range bad {
		if _, err := normalizeSectorCategory(raw); err == nil {
			t.Fatalf("%q 应被拒绝", raw)
		}
	}
	// 旧 BK 代码的错误必须带可操作提示。
	_, err := normalizeSectorCategory("BK1201")
	if !errors.Is(err, errLegacySectorCode) {
		t.Fatalf("BK1201 应返回 errLegacySectorCode，实际 %v", err)
	}
}

func normalizeLower(s string) string {
	out := ""
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			r = r + ('a' - 'A')
		}
		if r == ' ' {
			continue
		}
		out += string(r)
	}
	return out
}
