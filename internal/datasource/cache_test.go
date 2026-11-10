package datasource

import (
	"testing"
	"time"
)

func TestCacheTTL(t *testing.T) {
	clock := newFakeClock()
	c := newCache(clock.Now)

	c.put("k", "v1", ttlQuote)
	if got, ok := c.get("k"); !ok || got != "v1" {
		t.Fatalf("刚写入应命中，实际 ok=%v got=%v", ok, got)
	}

	// 差 1 秒还没过期
	clock.Advance(ttlQuote - time.Second)
	if _, ok := c.get("k"); !ok {
		t.Fatal("未过期不应失效")
	}

	// 超过 TTL 立即失效并清理
	clock.Advance(2 * time.Second)
	if _, ok := c.get("k"); ok {
		t.Fatal("过期后不应命中")
	}
	if c.size() != 0 {
		t.Fatalf("过期条目应被清理，实际还有 %d 条", c.size())
	}
}

func TestCacheOverwriteAndClear(t *testing.T) {
	clock := newFakeClock()
	c := newCache(clock.Now)
	c.put("k", 1, time.Minute)
	c.put("k", 2, time.Minute)
	if got, _ := c.get("k"); got != 2 {
		t.Fatalf("覆盖写入后应取到新值，实际 %v", got)
	}
	c.clear()
	if c.size() != 0 {
		t.Fatalf("clear 后应为空，实际 %d 条", c.size())
	}
}

func TestCacheNoTTLNeverExpires(t *testing.T) {
	clock := newFakeClock()
	c := newCache(clock.Now)
	c.put("k", "v", 0)
	clock.Advance(365 * 24 * time.Hour)
	if _, ok := c.get("k"); !ok {
		t.Fatal("ttl<=0 表示不过期")
	}
}

func TestTokenBucketRate(t *testing.T) {
	clock := newFakeClock()
	bucket := newTokenBucket(4, clock.Now)
	// 初始容量 4：前 4 次立即通过
	for i := 0; i < 4; i++ {
		if err := bucket.wait(t.Context()); err != nil {
			t.Fatalf("第 %d 次取令牌失败: %v", i+1, err)
		}
	}
	// 第 5 次需要等 250ms（4 QPS），用独立 goroutine + 时钟推进验证不会立即返回
	done := make(chan error, 1)
	go func() { done <- bucket.wait(t.Context()) }()
	select {
	case err := <-done:
		t.Fatalf("令牌耗尽后不应立即通过，err=%v", err)
	case <-time.After(60 * time.Millisecond):
	}
	clock.Advance(time.Second)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("补满令牌后应通过: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("补满令牌后仍未通过")
	}
}
