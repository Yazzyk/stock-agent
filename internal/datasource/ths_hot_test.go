package datasource

import (
	"os"
	"testing"
)

// TestParseThsHot 校验同花顺热榜解析：市场映射、热度、排名变化、题材与上榜原因。
func TestParseThsHot(t *testing.T) {
	body, err := os.ReadFile("testdata/ths_hot_hour.json")
	if err != nil {
		t.Fatalf("读 fixture 失败: %v", err)
	}
	out, err := parseThsHot(body, 2)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("limit=2 应返回 2 条，实际 %d", len(out))
	}
	first := out[0]
	if first.Code == "" || first.Name == "" {
		t.Fatalf("条目缺代码/名称: %+v", first)
	}
	if first.Market != "SH" && first.Market != "SZ" {
		t.Fatalf("market 映射异常: %q", first.Market)
	}
	if first.Heat <= 0 || first.Rank <= 0 {
		t.Fatalf("热度/排名异常: %+v", first)
	}
	if len(first.ConceptTags) == 0 && first.Reason == "" {
		t.Fatalf("题材与上榜原因不应同时为空: %+v", first)
	}
}

// TestNormalizeHotType 校验榜单类型与条数边界。
func TestNormalizeHotType(t *testing.T) {
	if got, err := normalizeHotType(""); err != nil || got != "hour" {
		t.Fatalf("空 type = %q, %v，期望 hour", got, err)
	}
	if got, err := normalizeHotType("DAY"); err != nil || got != "day" {
		t.Fatalf("DAY = %q, %v，期望 day", got, err)
	}
	if _, err := normalizeHotType("week"); err == nil {
		t.Fatal("非法 type 应报错")
	}
	if got := normalizeHotLimit(0); got != defaultThsHot {
		t.Fatalf("默认 limit = %d，期望 %d", got, defaultThsHot)
	}
	if got := normalizeHotLimit(999); got != maxThsHot {
		t.Fatalf("上限 limit = %d，期望 %d", got, maxThsHot)
	}
}
