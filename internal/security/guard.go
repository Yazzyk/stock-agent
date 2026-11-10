// Package security 实现 contract.Guard 与 contract.Audit：
// 域名白名单后缀匹配、路径根校验、请求限速令牌桶、密钥脱敏，以及 append-only 审计日志。
package security

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ErrPathDenied 表示目标路径不在安全策略允许的根目录内。
var ErrPathDenied = errors.New("安全策略：路径不在允许目录内")

// Guard 是 contract.Guard 的唯一实现，并发安全。
type Guard struct {
	policy  contract.Policy
	roots   []string
	allowed []string
	limiter *tokenBucket
}

var _ contract.Guard = (*Guard)(nil)

// NewGuard 构造 Guard。roots 为空时回落到 policy.PathRoots。
// 根目录会转成绝对路径并清理，用于 CheckPath 的前缀判定。
func NewGuard(policy contract.Policy, roots []string) contract.Guard {
	if len(roots) == 0 {
		roots = policy.PathRoots
	}
	normalized := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		normalized = append(normalized, abs)
	}
	allowed := make([]string, 0, len(policy.NetworkAllowlist))
	for _, h := range policy.NetworkAllowlist {
		if n := normalizeHostRule(h); n != "" {
			allowed = append(allowed, n)
		}
	}
	perMin := policy.MaxReqPerMin
	if perMin <= 0 {
		perMin = 120
	}
	return &Guard{
		policy:  policy,
		roots:   normalized,
		allowed: allowed,
		limiter: newTokenBucket(float64(perMin), float64(perMin)/60.0, time.Now),
	}
}

// Policy 返回策略快照（切片做防御性拷贝，避免调用方改到内部状态）。
func (g *Guard) Policy() contract.Policy {
	p := g.policy
	p.NetworkAllowlist = append([]string{}, g.policy.NetworkAllowlist...)
	p.PathRoots = append([]string{}, g.roots...)
	return p
}

// AllowHost 判断主机是否命中白名单：完全相等或以 "."+规则 结尾的才算命中，
// 避免 "eastmoney.com.evil.com" 这类后缀伪装。
func (g *Guard) AllowHost(host string) bool {
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	for _, rule := range g.allowed {
		if h == rule || strings.HasSuffix(h, "."+rule) {
			return true
		}
	}
	return false
}

// CheckPath 校验路径是否落在允许的根目录内。
// 会解析已存在祖先的真实路径，防止 ".." 与软链接把写入带出根目录。
func (g *Guard) CheckPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("%w：路径为空", ErrPathDenied)
	}
	if strings.ContainsRune(p, 0x00) {
		return fmt.Errorf("%w：路径包含非法字符", ErrPathDenied)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return fmt.Errorf("%w：无法解析路径 %q", ErrPathDenied, p)
	}
	real := evalExisting(abs)
	for _, root := range g.roots {
		// 同时用原始根与真实根比较：macOS 上 /var 会解析成 /private/var。
		if withinRoot(real, root) || withinRoot(real, evalExisting(root)) {
			return nil
		}
	}
	return fmt.Errorf("%w：%s（允许目录：%s）", ErrPathDenied, p, strings.Join(g.roots, ", "))
}

// Redact 抹掉文本中的密钥。覆盖 sk-、Bearer、cookie、token=、api_key 等常见形态。
func (g *Guard) Redact(s string) string { return Redact(s) }

// Limiter 返回限速器（contract.Guard 不含限速方法，需要限速的调用方用这里取）。
func (g *Guard) Limiter() RateLimiter { return g.limiter }

// RateLimiter 是令牌桶限速器。
type RateLimiter interface {
	// Allow 尝试立刻取一个令牌，取不到返回 false。
	Allow() bool
	// Wait 阻塞等待一个令牌，ctx 取消则返回错误。
	Wait(ctx context.Context) error
	// Tokens 返回当前可用令牌数（用于测试与观测）。
	Tokens() float64
}

// ---------- 脱敏 ----------

// 脱敏规则：命中后保留字段名/前缀，值替换成 ***。
var redactRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`sk-[A-Za-z0-9_-]{4,}`), "sk-***"},
	{regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{4,}`), "${1}***"},
	{regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|apikey|access[_-]?token|refresh[_-]?token|token|secret|password|passwd|cookie)["']?\s*[=:]\s*)["']?[^\s"',;&}]{3,}["']?`), "${1}***"},
	{regexp.MustCompile(`(?i)(set-cookie\s*:\s*)[^\r\n]+`), "${1}***"},
}

// Redact 是包级脱敏函数，便于在 Guard 之外复用。
func Redact(s string) string {
	if s == "" {
		return s
	}
	out := s
	for _, rule := range redactRules {
		out = rule.re.ReplaceAllString(out, rule.repl)
	}
	return out
}

// ---------- 主机归一化 ----------

// normalizeHost 从 URL / host[:port] / 纯域名里取出小写主机名。
// 任何"看起来像 authority 但不合法"的输入（端口不是数字、带多余路径等）都返回空串，
// 走拒绝分支，避免 "eastmoney.com:443.evil.com" 这种伪装被后缀匹配放过。
func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if strings.Contains(h, "://") {
		u, err := url.Parse(h)
		if err != nil {
			return ""
		}
		h = u.Hostname()
	} else {
		if i := strings.IndexAny(h, "/?#"); i >= 0 {
			h = h[:i]
		}
		if i := strings.LastIndex(h, "@"); i >= 0 {
			h = h[i+1:]
		}
		switch {
		case strings.HasPrefix(h, "["):
			j := strings.Index(h, "]")
			if j < 0 {
				return ""
			}
			h = h[1:j]
		case strings.Contains(h, ":"):
			i := strings.Index(h, ":")
			if !allDigits(h[i+1:]) {
				return ""
			}
			h = h[:i]
		}
	}
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" || strings.Contains(h, "..") || !validHostChars(h) {
		return ""
	}
	return h
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validHostChars 只接受域名/IP 允许的字符，挡住空格、引号、斜杠之类。
func validHostChars(h string) bool {
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == ':':
		default:
			return false
		}
	}
	return true
}

// normalizeHostRule 归一化白名单条目，支持 "*.example.com"、".example.com"、"example.com"。
// 通配前缀要在主机名校验之前剥掉，否则 "*" 会被 validHostChars 判为非法。
func normalizeHostRule(rule string) string {
	r := strings.TrimSpace(rule)
	r = strings.TrimPrefix(r, "*.")
	r = strings.TrimPrefix(r, ".")
	return normalizeHost(r)
}

// ---------- 路径 ----------

// evalExisting 解析路径中已存在部分的真实路径，其余部分原样拼回。
func evalExisting(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(real)
	}
	dir := filepath.Dir(p)
	if dir == p {
		return filepath.Clean(p)
	}
	return filepath.Join(evalExisting(dir), filepath.Base(p))
}

// withinRoot 判断 path 是否等于 root 或位于 root 之下。
func withinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(os.PathSeparator))
}

// ---------- 令牌桶 ----------

type tokenBucket struct {
	mu        sync.Mutex
	capacity  float64
	perSecond float64
	tokens    float64
	last      time.Time
	now       func() time.Time
}

func newTokenBucket(capacity, perSecond float64, now func() time.Time) *tokenBucket {
	if capacity <= 0 {
		capacity = 1
	}
	if perSecond <= 0 {
		perSecond = capacity / 60
	}
	if now == nil {
		now = time.Now
	}
	return &tokenBucket{
		capacity:  capacity,
		perSecond: perSecond,
		tokens:    capacity,
		last:      now(),
		now:       now,
	}
}

func (b *tokenBucket) refillLocked() {
	now := b.now()
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens += elapsed * b.perSecond
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
}

func (b *tokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (b *tokenBucket) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("安全策略：等待限速令牌被取消: %w", err)
		}
		b.mu.Lock()
		b.refillLocked()
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		deficit := 1 - b.tokens
		wait := time.Duration(deficit / b.perSecond * float64(time.Second))
		b.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("安全策略：等待限速令牌被取消: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (b *tokenBucket) Tokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refillLocked()
	return b.tokens
}
