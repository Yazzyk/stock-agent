package datasource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 来源名用于 Health 归因与日志，禁止随意新增（Health 输出会直接展示给用户）。
const (
	sourceTHS       = "ths"
	sourceEastMoney = "eastmoney"
	sourceSina      = "sina"
	sourceTencent   = "tencent"
	sourceStore     = "store"
	sourceWatchlist = "watchlist"
)

const (
	defaultHTTPTimeout = 8 * time.Second
	defaultQPS         = 4.0
	defaultRetries     = 2
	defaultBackoff     = 200 * time.Millisecond
	maxBodyBytes       = 8 << 20
	userAgent          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var (
	// ErrGuardDenied 表示目标主机不在网络安全白名单内。
	ErrGuardDenied = errors.New("datasource: 目标主机不在网络安全白名单内")
	// ErrEmptyResponse 表示服务端返回空响应体（东财 kline 端点会直接断连，必须当作可重试失败）。
	ErrEmptyResponse = errors.New("datasource: 响应体为空")
)

// request 描述一次抓取。source 必填，用于 Health 与审计归因。
type request struct {
	source  string
	url     string
	referer string
	gbk     bool
	headers map[string]string
}

// sourceState 记录某个数据源最近一次调用结果，Health 直接读它，不发新请求。
type sourceState struct {
	lastErr  string
	lastOK   time.Time
	lastFail time.Time
	failures int
	success  int
}

type client struct {
	guard   contract.Guard
	http    *http.Client
	limiter *tokenBucket
	log     *slog.Logger
	retries int
	backoff time.Duration

	mu      sync.Mutex
	sources map[string]*sourceState
	now     func() time.Time
}

func newClient(cfg Config, now func() time.Time) *client {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &client{
		guard:   cfg.Guard,
		http:    &http.Client{Timeout: timeout},
		limiter: newTokenBucket(defaultQPS, now),
		log:     logger,
		retries: defaultRetries,
		backoff: defaultBackoff,
		sources: map[string]*sourceState{},
		now:     now,
	}
}

// get 执行一次带安全校验、限速、重试的 GET，返回原始字节（gbk 请求已在内部转成 UTF-8）。
func (c *client) get(ctx context.Context, req request) ([]byte, error) {
	if req.source == "" {
		req.source = "unknown"
	}
	parsed, err := url.Parse(req.url)
	if err != nil {
		return nil, fmt.Errorf("datasource: 请求地址非法: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("datasource: 只允许 http/https，实际 %q", parsed.Scheme)
	}
	if c.guard != nil && !c.guard.AllowHost(parsed.Host) {
		err := fmt.Errorf("%w: %s", ErrGuardDenied, parsed.Host)
		c.record(req.source, err)
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			delay := c.backoff * time.Duration(1<<uint(attempt-1))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}
		body, retryable, err := c.do(ctx, req, parsed)
		if err == nil {
			c.record(req.source, nil)
			return body, nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			break
		}
		c.log.Debug("数据源请求失败，准备重试", "source", req.source, "attempt", attempt+1, "error", err)
	}
	c.record(req.source, lastErr)
	return nil, lastErr
}

// do 发一次请求；retryable 表示失败是否值得重试（网络错误、5xx、空响应体）。
func (c *client) do(ctx context.Context, req request, parsed *url.URL) ([]byte, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("datasource: 构造请求失败: %w", err)
	}
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Accept", "*/*")
	// 千万不要手动设置 Accept-Encoding：Go 的 Transport 只有在自己加这个头时才会自动解压，
	// 手动设了就会拿到 gzip 原始字节（0x1f 0x8b），再交给 json.Unmarshal 必炸。
	if req.referer != "" {
		httpReq.Header.Set("Referer", req.referer)
	}
	for k, v := range req.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, true, fmt.Errorf("datasource: 请求 %s 失败: %w", parsed.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		retryable := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return nil, retryable, fmt.Errorf("datasource: %s 返回 HTTP %d", parsed.Host, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, true, fmt.Errorf("datasource: 读取 %s 响应失败: %w", parsed.Host, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		// 东财 push2 的 kline 端点会直接断连并留下空响应，这里必须可重试而不是把空当成功。
		return nil, true, fmt.Errorf("%w: %s", ErrEmptyResponse, parsed.Host)
	}
	if req.gbk {
		text, err := decodeGBK(raw)
		if err != nil {
			return nil, false, err
		}
		return []byte(text), false, nil
	}
	return raw, false, nil
}

func (c *client) record(source string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sources[source]
	if state == nil {
		state = &sourceState{}
		c.sources[source] = state
	}
	if err == nil {
		state.success++
		state.failures = 0
		state.lastErr = ""
		state.lastOK = c.now()
		return
	}
	state.failures++
	state.lastErr = err.Error()
	state.lastFail = c.now()
}

// knownSources 是需要出现在 Health 里的固定数据源清单。
// 固定列出的好处是面板永远能展示「哪些源没被用过」，而不是根据调用记录动态变形。
var knownSources = []string{sourceTHS, sourceEastMoney, sourceSina, sourceTencent}

// health 把各数据源的最近一次结果整理成可读文案（不发新请求）。
func (c *client) health() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(knownSources))
	for _, name := range knownSources {
		out[name] = "未使用"
	}
	for name, state := range c.sources {
		switch {
		case state.lastErr != "":
			out[name] = "失败: " + state.lastErr
		case state.success > 0:
			out[name] = fmt.Sprintf("可用（最近成功 %s，累计 %d 次）", state.lastOK.Format("15:04:05"), state.success)
		default:
			out[name] = "未使用"
		}
	}
	return out
}

// tokenBucket 是最小令牌桶：默认 4 QPS、容量 4，避免把数据源打到限流。
// 不用 x/time/rate 是为了不新增依赖，且这里只需要「匀速 + 允许小突发」。
type tokenBucket struct {
	mu       sync.Mutex
	rate     float64
	capacity float64
	tokens   float64
	last     time.Time
	now      func() time.Time
}

func newTokenBucket(qps float64, now func() time.Time) *tokenBucket {
	if qps <= 0 {
		qps = defaultQPS
	}
	if now == nil {
		now = time.Now
	}
	return &tokenBucket{rate: qps, capacity: qps, tokens: qps, last: now(), now: now}
}

func (b *tokenBucket) wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		now := b.now()
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * b.rate
			if b.tokens > b.capacity {
				b.tokens = b.capacity
			}
			b.last = now
		}
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		need := (1 - b.tokens) / b.rate
		b.mu.Unlock()
		wait := time.Duration(need * float64(time.Second))
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
