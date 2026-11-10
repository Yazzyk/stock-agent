package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

func testPolicy() contract.Policy {
	return contract.Policy{
		NetworkAllowlist: []string{"10jqka.com.cn", "*.eastmoney.com", "gtimg.cn", "deepseek.com"},
		AllowExec:        false,
		MaxReqPerMin:     60,
		MaxToolCalls:     24,
		MaxTokensPerRun:  60000,
	}
}

func TestAllowHost(t *testing.T) {
	g := NewGuard(testPolicy(), []string{t.TempDir()})
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"精确命中", "gtimg.cn", true},
		{"子域命中", "push2.eastmoney.com", true},
		{"通配符规则命中", "data.eastmoney.com", true},
		{"带端口", "EASTMONEY.COM:443", true},
		{"带协议与路径", "https://d.10jqka.com.cn/v6/line/hs_600000/01/last.js", true},
		{"大小写不敏感", "API.DeepSeek.com", true},
		{"未授权域", "evil.com", false},
		{"后缀伪装", "eastmoney.com.evil.com", false},
		{"无点拼接伪装", "noteastmoney.com", false},
		{"端口后接垃圾", "eastmoney.com:443.evil.com", false},
		{"端口非数字", "push2.eastmoney.com:abc", false},
		{"带引号注入", "\"eastmoney.com\"", false},
		{"空主机", "   ", false},
		{"只有路径", "/api/qt/stock", false},
	}
	for _, c := range cases {
		if got := g.AllowHost(c.host); got != c.want {
			t.Fatalf("%s: AllowHost(%q)=%v, want %v", c.name, c.host, got, c.want)
		}
	}
}

func TestCheckPath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "kb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kb", "a.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 软链接指向根目录之外，必须被拒绝。
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	g := NewGuard(testPolicy(), []string{root})

	ok := []string{
		root,
		filepath.Join(root, "kb", "a.md"),
		filepath.Join(root, "kb", "..", "kb", "a.md"),
		filepath.Join(root, "not-exist-yet", "b.json"),
	}
	for _, p := range ok {
		if err := g.CheckPath(p); err != nil {
			t.Fatalf("CheckPath(%q) 应通过，实际错误: %v", p, err)
		}
	}

	denied := []string{
		"",
		outside,
		filepath.Join(root, "..", "x"),
		filepath.Join(link, "leak.txt"),
		"/etc/hosts",
	}
	for _, p := range denied {
		if err := g.CheckPath(p); err == nil {
			t.Fatalf("CheckPath(%q) 应被拒绝", p)
		}
	}
}

func TestRedact(t *testing.T) {
	g := NewGuard(testPolicy(), []string{t.TempDir()})
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"sk 密钥", "request failed key=sk-abcdef123456", "request failed key=sk-***"},
		{"Bearer", "Authorization: Bearer eyJhbGciOi.payload.sig", "Authorization: Bearer ***"},
		{"cookie", "cookie: SESSION=abc123; theme=dark", "cookie: ***; theme=dark"},
		{"token 等号", "https://x/api?token=deadbeef123&a=1", "https://x/api?token=***&a=1"},
		{"api_key", "api_key=abcdef123456", "api_key=***"},
		{"JSON 字段", "{\"apiKey\":\"secretvalue\",\"model\":\"x\"}", "{\"apiKey\":***,\"model\":\"x\"}"},
		{"无密钥原样返回", "行情 600000 现价 12.34", "行情 600000 现价 12.34"},
	}
	for _, c := range cases {
		if got := g.Redact(c.in); got != c.want {
			t.Fatalf("%s: Redact(%q)=%q, want %q", c.name, c.in, got, c.want)
		}
	}
	if !strings.Contains(g.Redact("sk-abcdef123456"), "***") {
		t.Fatal("Redact 必须打码")
	}
}

func TestRateLimitAllow(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	g := NewGuard(testPolicy(), []string{t.TempDir()}).(*Guard)
	g.limiter = newTokenBucket(3, 3.0/60.0, clock)

	for i := 0; i < 3; i++ {
		if !g.limiter.Allow() {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if g.limiter.Allow() {
		t.Fatal("令牌耗尽后必须拒绝")
	}
	now = now.Add(20 * time.Second) // 20s * 0.05/s = 1 个令牌
	if !g.limiter.Allow() {
		t.Fatal("补充 1 个令牌后应放行一次")
	}
	if g.limiter.Allow() {
		t.Fatal("只补充了 1 个令牌，第二次必须拒绝")
	}
}

func TestRateLimitWait(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	g := NewGuard(testPolicy(), []string{t.TempDir()}).(*Guard)
	g.limiter = newTokenBucket(1, 1.0/60.0, clock) // 每分钟 1 个

	if !g.limiter.Allow() {
		t.Fatal("首个令牌应放行")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := g.limiter.Wait(ctx); err == nil {
		t.Fatal("令牌不足且 ctx 超时，Wait 必须返回错误")
	}

	// 时间推进后有令牌，Wait 立刻返回。
	now = now.Add(61 * time.Second)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := g.limiter.Wait(ctx2); err != nil {
		t.Fatalf("有可用令牌时 Wait 应成功: %v", err)
	}
}

func TestPolicySnapshotIsolated(t *testing.T) {
	policy := testPolicy()
	g := NewGuard(policy, []string{t.TempDir()})
	p := g.Policy()
	p.NetworkAllowlist[0] = "evil.com"
	p.PathRoots[0] = "/tmp/evil"
	if !g.AllowHost("gtimg.cn") {
		t.Fatal("修改返回的 Policy 快照不应影响 Guard 内部白名单")
	}
	if err := g.CheckPath(filepath.Join(t.TempDir(), "a")); err == nil {
		t.Fatal("修改返回的 PathRoots 不应扩大允许目录")
	}
}
