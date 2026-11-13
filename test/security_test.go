package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/security"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

func allowlistPolicy(root string) contract.Policy {
	return contract.Policy{
		NetworkAllowlist: []string{"10jqka.com.cn", "gtimg.cn", "sinajs.cn", "sina.com.cn"},
		PathRoots:        []string{root},
		AllowExec:        false,
		MaxReqPerMin:     2,
		MaxToolCalls:     8,
		MaxTokensPerRun:  10000,
	}
}

// 路径穿越必须被拒绝：绝对越权路径、.. 逃逸、软链接逃逸、空/NUL 路径。
func TestGuardCheckPath(t *testing.T) {
	root := t.TempDir()
	g := security.NewGuard(allowlistPolicy(root), nil)

	allowed := []string{
		root,
		filepath.Join(root, "store.db"),
		filepath.Join(root, "sub", "deep", "file.json"),
		filepath.Join(root, ".", "a"),
		filepath.Join(root, "sub", "..", "b"),
	}
	for _, p := range allowed {
		if err := g.CheckPath(p); err != nil {
			t.Fatalf("根目录内路径应放行: %s（%v）", p, err)
		}
	}

	denied := []struct {
		name string
		path string
	}{
		{"系统目录", "/etc/passwd"},
		{"家目录", filepath.Join(mustHome(t), ".ssh", "id_rsa")},
		{"上级逃逸", filepath.Join(root, "..", "escape.txt")},
		{"多级逃逸", filepath.Join(root, "sub", "..", "..", "escape.txt")},
		{"纯相对逃逸", "../escape.txt"},
		{"空路径", ""},
		{"NUL 字节", "a\x00b"},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			err := g.CheckPath(tc.path)
			if err == nil {
				t.Fatalf("越权路径应被拒绝: %q", tc.path)
			}
			if !strings.Contains(err.Error(), "不允许") && !strings.Contains(err.Error(), "安全策略") {
				t.Fatalf("拒绝信息应说明安全策略，实际: %v", err)
			}
		})
	}

	// 软链接逃逸：根内链接指向根外，必须按真实路径判定
	t.Run("软链接逃逸", func(t *testing.T) {
		outside := t.TempDir()
		link := filepath.Join(root, "escape-link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("当前文件系统不支持软链接，跳过: %v", err)
		}
		if err := g.CheckPath(filepath.Join(link, "secret.txt")); err == nil {
			t.Fatalf("经软链接指向根目录外的路径应被拒绝: %s", link)
		}
	})

	// 前缀伪造：根目录名相似但不能命中（/tmp/root 与 /tmp/root-evil）
	t.Run("同前缀兄弟目录", func(t *testing.T) {
		sibling := root + "-evil"
		if err := g.CheckPath(filepath.Join(sibling, "x")); err == nil {
			t.Fatalf("同前缀兄弟目录不应放行: %s", sibling)
		}
	})
}

func mustHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("无法取得家目录，跳过: %v", err)
	}
	return home
}

// 非白名单域名必须拒绝，且不能被后缀伪装与端口/协议写法绕过。
func TestGuardAllowHost(t *testing.T) {
	root := t.TempDir()
	g := security.NewGuard(allowlistPolicy(root), nil)

	cases := []struct {
		host string
		want bool
	}{
		{"hq.sinajs.cn", true},
		{"sina.com.cn", true},
		{"SINAJS.CN", true},
		{"vip.stock.finance.sina.com.cn:443", true},
		{"https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/MoneyFlow.ssi_ssfx_flzjtj?daima=sh600000", true},
		{"feed.mix.sina.com.cn", true},
		{"d.10jqka.com.cn", true},
		{"qt.gtimg.cn", true},
		{"sinajs.cn.evil.com", false},
		{"evil.com", false},
		{"notsinajs.cn", false},
		// 东财域名已从白名单移除：它现在应当被拒绝，而不是被放行。
		{"push2.eastmoney.com", false},
		{"data.eastmoney.com", false},
		{"evil.com:443.sinajs.cn", false},
		// 端口后带垃圾 / 非数字端口：authority 非法，必须拒绝（backend-core 已收紧 normalizeHost）
		{"sinajs.cn:443.evil.com", false},
		{"hq.sinajs.cn:abc", false},
		{"SINAJS.CN:443", true},
		{"https://d.10jqka.com.cn/v6/line/hs_600000/01/last.js", true},
		{"", false},
		{"127.0.0.1", false},
		{"localhost", false},
	}
	for _, tc := range cases {
		if got := g.AllowHost(tc.host); got != tc.want {
			t.Fatalf("AllowHost(%q) = %v，期望 %v", tc.host, got, tc.want)
		}
	}

}

// 密钥脱敏：sk-、Bearer、cookie、token=、api_key 都不能原样漏出。
func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		secret string // 脱敏后不允许出现的明文
		must   string // 脱敏后必须出现的脱敏标记
	}{
		{"DeepSeek key", "调用失败: sk-proj-abcdef1234567890 无效", "abcdef1234567890", "sk-***"},
		{"Bearer token", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc.def", "eyJhbGciOiJIUzI1NiJ9", "Bearer ***"},
		{"token 参数", "GET https://x.test/a?token=abcd1234efgh&x=1", "abcd1234efgh", "token=***"},
		{"cookie 头", "Cookie: sessionid=zzz999yyy; path=/", "zzz999yyy", "***"},
		{"api_key", "config api_key=SECRET12345678 done", "SECRET12345678", "***"},
		{"password 字段", `{"password":"hunter2hunter2"}`, "hunter2hunter2", "***"},
		{"set-cookie", "Set-Cookie: sid=abc999xyz; HttpOnly", "abc999xyz", "***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := security.Redact(tc.in)
			if strings.Contains(out, tc.secret) {
				t.Fatalf("脱敏后仍含明文密钥 %q:\n原: %s\n出: %s", tc.secret, tc.in, out)
			}
			if !strings.Contains(out, tc.must) {
				t.Fatalf("脱敏后应保留标记 %q，实际: %s", tc.must, out)
			}
		})
	}

	// 多次出现全部脱敏
	t.Run("多次出现", func(t *testing.T) {
		in := "a sk-aaaa1111 b sk-bbbb2222 c"
		out := security.Redact(in)
		if strings.Contains(out, "aaaa1111") || strings.Contains(out, "bbbb2222") {
			t.Fatalf("多次出现的密钥应全部脱敏: %s", out)
		}
	})

	// 普通文本不受影响（避免过度脱敏把行情内容打码）
	t.Run("普通文本", func(t *testing.T) {
		in := "浦发银行 600000 收盘 10.20，主力净流入 1.2 亿"
		if out := security.Redact(in); out != in {
			t.Fatalf("普通文本不应被改动:\n原: %s\n出: %s", in, out)
		}
	})
}

// 令牌桶：初始容量 = MaxReqPerMin，用完后立刻拒绝，Wait 可用 ctx 取消。
func TestGuardRateLimiter(t *testing.T) {
	root := t.TempDir()
	g := security.NewGuard(allowlistPolicy(root), nil).(*security.Guard)
	lim := g.Limiter()

	if lim.Tokens() < 1 {
		t.Fatalf("初始令牌数应≥1，实际 %.2f", lim.Tokens())
	}
	if !lim.Allow() || !lim.Allow() {
		t.Fatal("前两次应放行（容量 2）")
	}
	if lim.Allow() {
		t.Fatal("令牌耗尽后应拒绝（MaxReqPerMin=2）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := lim.Wait(ctx); err == nil {
		t.Fatal("令牌不足时 Wait 应随 ctx 超时返回错误")
	}
}
