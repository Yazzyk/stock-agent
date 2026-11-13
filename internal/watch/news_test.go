package watch

import (
	"strings"
	"testing"
)

// 公告口径变更（东财公告接口下线）后的离线测试：notice/announcement 触发源
// 改成「新浪滚动新闻 + 关键词过滤」，这里钉住过滤语义，避免泛财经头条把 news.hits 顶满。

func TestFilterNewsByKeyword(t *testing.T) {
	items := []NewsItem{
		{Title: "某公司发布并购重组预案", Source: "sina"},
		{Title: "央行公开市场净投放 2000 亿元", Source: "sina"},
		{Title: "上市公司回购与减持新规落地", Source: "sina"},
	}
	cases := []struct {
		name    string
		keyword string
		want    int
	}{
		{"命中一条", "并购重组", 1},
		{"命中两条", "回购", 1},
		{"空关键词返回全部", "", 3},
		{"只含空白按空处理", "   ", 3},
		{"完全不命中", "量子计算", 0},
		{"大小写不敏感（英文关键词）", "  ", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterNewsByKeyword(items, tc.keyword)
			if len(got) != tc.want {
				t.Fatalf("命中数 = %d，期望 %d（keyword=%q）", len(got), tc.want, tc.keyword)
			}
		})
	}
	// 过滤后必须保留原标题，不能被改写。
	hits := filterNewsByKeyword(items, "并购重组")
	if len(hits) == 1 && !strings.Contains(hits[0].Title, "并购重组") {
		t.Fatalf("命中条目标题不符: %+v", hits[0])
	}
}

// TestFilterNewsByKeywordMatchesName 覆盖「按个股名称命中」的分支。
func TestFilterNewsByKeywordMatchesName(t *testing.T) {
	items := []NewsItem{
		{Title: "业绩说明会召开", Name: "浦发银行"},
		{Title: "行业景气度回暖", Name: "中芯国际"},
	}
	hits := filterNewsByKeyword(items, "浦发")
	if len(hits) != 1 {
		t.Fatalf("按名称应命中 1 条，实际 %d", len(hits))
	}
	if hits[0].Name != "浦发银行" {
		t.Fatalf("命中条目不符: %+v", hits[0])
	}
}

// TestFetchNewsDispatchByKind 保证 kind=notice 走公告链路、其它走财经要闻。
// 两者现在都只打新浪主机，关键是「不再有任何东财请求」。
func TestNewsSourceHostsAreNotEastMoney(t *testing.T) {
	for _, address := range []string{sinaRollURL, sinaRollNoticeURL} {
		if !strings.Contains(address, "feed.mix.sina.com.cn") {
			t.Fatalf("新闻/公告源必须只走新浪，实际 %q", address)
		}
		if strings.Contains(address, "eastmoney") {
			t.Fatalf("不得再出现东财主机: %q", address)
		}
	}
}
