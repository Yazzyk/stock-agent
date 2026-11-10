package datasource

import (
	"math"
	"testing"
)

func TestUnwrapJSONP(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"回调包裹", `quotebridge_v6_realhead_hs_600000_defer_last({"items":{"10":"10.20"}});`, `{"items":{"10":"10.20"}}`, true},
		{"纯 JSON", `{"a":1}`, `{"a":1}`, true},
		{"带前后空白", "  \n [1,2] \n ", "[1,2]", true},
		{"空响应", "", "", false},
		{"只有空白", "   ", "", false},
		{"没有括号", "not json at all", "", false},
		{"空括号", "cb()", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unwrapJSONP([]byte(tc.in))
			if tc.ok {
				if err != nil {
					t.Fatalf("unwrapJSONP 失败: %v", err)
				}
				if string(got) != tc.want {
					t.Fatalf("unwrapJSONP = %q，期望 %q", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("unwrapJSONP 应报错，实际返回 %q", got)
			}
		})
	}
}

func TestParseNum(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"10.20", 10.20, true},
		{"  7 ", 7, true},
		{"-0.25", -0.25, true},
		{"", 0, false},
		{"-", 0, false},
		{"--", 0, false},
		{"null", 0, false},
		{"abc", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseNum(tc.in)
		if ok != tc.ok {
			t.Fatalf("parseNum(%q) ok = %v，期望 %v", tc.in, ok, tc.ok)
		}
		if ok && math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("parseNum(%q) = %v，期望 %v", tc.in, got, tc.want)
		}
	}
}

func TestDecodeGBK(t *testing.T) {
	// 「浦发银行」的 GBK 字节
	raw := []byte{0xC6, 0xD6, 0xB7, 0xA2, 0xD2, 0xF8, 0xD0, 0xD0}
	got, err := decodeGBK(raw)
	if err != nil {
		t.Fatalf("decodeGBK 失败: %v", err)
	}
	if got != "浦发银行" {
		t.Fatalf("decodeGBK = %q，期望 浦发银行", got)
	}

	t.Run("真实腾讯响应可解码", func(t *testing.T) {
		body := readTestdata(t, "tx_quote_600000.txt")
		text, err := decodeGBK(body)
		if err != nil {
			t.Fatalf("解码真实响应失败: %v", err)
		}
		if !contains(text, "浦发银行") {
			t.Fatalf("解码后应包含股票名称，实际前 80 字节: %q", text[:min(80, len(text))])
		}
	})
}

func TestSplitNonEmpty(t *testing.T) {
	got := splitNonEmpty("a~~b~ ~c", "~")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("splitNonEmpty = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitNonEmpty[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
