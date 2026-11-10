package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "guanlan.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "guanlan.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	if db.Path() != path {
		t.Fatalf("Path = %q, 期望 %q", db.Path(), path)
	}
	// 建表迁移应当已经能读写
	if err := db.Put("b", "k", []byte("v")); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
}

func TestGetPutDelete(t *testing.T) {
	db := openTemp(t)

	if _, err := db.Get("watchlist", "missing"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("Get 不存在的键应返回 contract.ErrNotFound，实际: %v", err)
	}

	cases := []struct {
		name string
		val  []byte
	}{
		{"普通文本", []byte("600000 浦发银行")},
		{"空值", []byte{}},
		{"二进制", []byte{0x00, 0xff, 0x01, 0x00}},
		{"中文与换行", []byte("第一行\n第二行")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Put("watchlist", "k1", tc.val); err != nil {
				t.Fatalf("Put 失败: %v", err)
			}
			got, err := db.Get("watchlist", "k1")
			if err != nil {
				t.Fatalf("Get 失败: %v", err)
			}
			if string(got) != string(tc.val) {
				t.Fatalf("Get = %q, 期望 %q", got, tc.val)
			}
			// 覆盖写入
			if err := db.Put("watchlist", "k1", []byte("覆盖")); err != nil {
				t.Fatalf("覆盖 Put 失败: %v", err)
			}
			if got, _ = db.Get("watchlist", "k1"); string(got) != "覆盖" {
				t.Fatalf("覆盖后 Get = %q, 期望 覆盖", got)
			}
			if err := db.Delete("watchlist", "k1"); err != nil {
				t.Fatalf("Delete 失败: %v", err)
			}
			if _, err := db.Get("watchlist", "k1"); !errors.Is(err, contract.ErrNotFound) {
				t.Fatalf("删除后 Get 应返回 ErrNotFound，实际: %v", err)
			}
			// 幂等删除
			if err := db.Delete("watchlist", "k1"); err != nil {
				t.Fatalf("重复 Delete 应返回 nil，实际: %v", err)
			}
		})
	}

	// bucket 隔离：同 key 不同 bucket 互不影响
	if err := db.Put("watchlist", "same", []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put("market_cache", "same", []byte("B")); err != nil {
		t.Fatal(err)
	}
	a, _ := db.Get("watchlist", "same")
	b, _ := db.Get("market_cache", "same")
	if string(a) != "A" || string(b) != "B" {
		t.Fatalf("bucket 隔离失效: watchlist=%q market_cache=%q", a, b)
	}
}

func TestListPrefixOrdered(t *testing.T) {
	db := openTemp(t)
	// 故意乱序写入，验证按 key 升序返回
	items := map[string]string{
		"grp/自选股/600000":  "浦发银行",
		"grp/自选股/000001":  "平安银行",
		"grp/自选股/300750":  "宁德时代",
		"grp/自选股/300750b": "重复前缀",
		"grp/观察/600519":   "贵州茅台",
		"meta/updatedAt":  "2026-11-10",
	}
	for k, v := range items {
		if err := db.Put("watchlist", k, []byte(v)); err != nil {
			t.Fatalf("Put %s 失败: %v", k, err)
		}
	}
	if err := db.Put("other", "grp/自选股/999999", []byte("不该出现")); err != nil {
		t.Fatal(err)
	}

	got, err := db.List("watchlist", "grp/自选股/")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	wantKeys := []string{
		"grp/自选股/000001",
		"grp/自选股/300750",
		"grp/自选股/300750b",
		"grp/自选股/600000",
	}
	if len(got) != len(wantKeys) {
		t.Fatalf("List 返回 %d 条，期望 %d 条: %+v", len(got), len(wantKeys), got)
	}
	for i, want := range wantKeys {
		if got[i].Key != want {
			t.Fatalf("第 %d 条 key = %q，期望 %q", i+1, got[i].Key, want)
		}
	}

	// 空前缀 = 整桶
	all, err := db.List("watchlist", "")
	if err != nil {
		t.Fatalf("List 整桶失败: %v", err)
	}
	if len(all) != len(items) {
		t.Fatalf("整桶 List 返回 %d 条，期望 %d 条", len(all), len(items))
	}
	// 无命中返回空切片而不是 nil
	none, err := db.List("watchlist", "not-exist/")
	if err != nil {
		t.Fatalf("List 无命中失败: %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Fatalf("无命中应返回空切片，实际: %#v", none)
	}
}

func TestListPrefixWildcardEscaped(t *testing.T) {
	db := openTemp(t)
	// 前缀里含 SQL 通配符，必须按字面匹配
	for _, k := range []string{"a%b", "a%b/1", "axb/1", "a_b"} {
		if err := db.Put("b", k, []byte(k)); err != nil {
			t.Fatalf("Put %s 失败: %v", k, err)
		}
	}
	got, err := db.List("b", "a%")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(got) != 2 || got[0].Key != "a%b" || got[1].Key != "a%b/1" {
		t.Fatalf("通配符前缀匹配错误: %+v", got)
	}
}

func TestListPrefixUpperBound(t *testing.T) {
	db := openTemp(t)
	for _, k := range []string{"a/1", "a/2", "b/1"} {
		if err := db.Put("b", k, []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	// 前缀末字节为 0xfe 时上界应为 0xff，不能把 b/1 带出来
	got, err := db.List("b", "a/")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("前缀边界错误: %+v", got)
	}
}

func TestConcurrentAccess(t *testing.T) {
	db := openTemp(t)
	const writers = 8
	const perWriter = 25

	var wg sync.WaitGroup
	errs := make(chan error, writers*3)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := fmt.Sprintf("k/%02d/%03d", w, i)
				if err := db.Put("watchlist", key, []byte(key)); err != nil {
					errs <- err
					return
				}
				if _, err := db.Get("watchlist", key); err != nil && !errors.Is(err, contract.ErrNotFound) {
					errs <- err
					return
				}
				if _, err := db.List("watchlist", fmt.Sprintf("k/%02d/", w)); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	// 并发读与删，验证不会死锁或报 database is locked
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < perWriter; i++ {
			if _, err := db.Get("watchlist", fmt.Sprintf("k/00/%03d", i)); err != nil && !errors.Is(err, contract.ErrNotFound) {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发访问失败: %v", err)
	}

	items, err := db.List("watchlist", "")
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(items) != writers*perWriter {
		t.Fatalf("并发写入后共 %d 条，期望 %d 条", len(items), writers*perWriter)
	}
}

func TestReopenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guanlan.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if err := db.Put("watchlist", "grp/自选股/600000", []byte("浦发银行")); err != nil {
		t.Fatalf("Put 失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("重新 Open 失败: %v", err)
	}
	defer func() { _ = again.Close() }()
	got, err := again.Get("watchlist", "grp/自选股/600000")
	if err != nil {
		t.Fatalf("重开后 Get 失败: %v", err)
	}
	if string(got) != "浦发银行" {
		t.Fatalf("重开后 Get = %q, 期望 浦发银行", got)
	}
}

func TestCloseRejectsAccess(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "guanlan.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("重复 Close 应返回 nil，实际: %v", err)
	}
	if _, err := db.Get("b", "k"); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 Get 应返回 ErrClosed，实际: %v", err)
	}
	if err := db.Put("b", "k", []byte("v")); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 Put 应返回 ErrClosed，实际: %v", err)
	}
	if _, err := db.List("b", ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 List 应返回 ErrClosed，实际: %v", err)
	}
	if err := db.Delete("b", "k"); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后 Delete 应返回 ErrClosed，实际: %v", err)
	}
}

func TestEmptyArgsRejected(t *testing.T) {
	db := openTemp(t)
	if _, err := db.Get("", "k"); err == nil {
		t.Fatal("空 bucket 的 Get 应报错")
	}
	if err := db.Put("b", "", []byte("v")); err == nil {
		t.Fatal("空 key 的 Put 应报错")
	}
	if _, err := db.List("", ""); err == nil {
		t.Fatal("空 bucket 的 List 应报错")
	}
}

func TestOpenEmptyPath(t *testing.T) {
	if _, err := Open("  "); err == nil {
		t.Fatal("空路径 Open 应报错")
	}
}
