package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// writeManualWatchlist 写入手工导入文件（第三级兜底）。
func writeManualWatchlist(t *testing.T, dir string, items []contract.WatchItem) {
	t.Helper()
	payload := map[string]any{
		"updatedAt": "2026-11-10T15:00:00+08:00",
		"items":     items,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, watchFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWatchlistReadsManualFileWhenStoreEmpty(t *testing.T) {
	dir := t.TempDir()
	writeManualWatchlist(t, dir, []contract.WatchItem{
		{Code: "600000", Name: "浦发银行", Group: "自选股", Order: 1},
		{Code: "sz000001", Name: "平安银行", Group: "自选股", Order: 2},
		{Code: "300750", Group: "成长", Order: 1},
	})
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: dir})

	items, err := service.Watchlist(context.Background())
	if err != nil {
		t.Fatalf("Watchlist 失败: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("条目数 = %d，期望 3: %+v", len(items), items)
	}
	if items[0].Code != "600000" || items[0].Market != contract.MarketSH || items[0].Name != "浦发银行" {
		t.Fatalf("首条 = %+v", items[0])
	}
	// 代码归一：sz000001 应变成 000001 + sz
	if items[1].Code != "000001" || items[1].Market != contract.MarketSZ {
		t.Fatalf("代码归一失败: %+v", items[1])
	}
	// 缺省分组与名称
	if items[2].Group != "成长" {
		t.Fatalf("分组 = %q，期望 成长", items[2].Group)
	}
}

func TestWatchlistEmptyWhenNothingConfigured(t *testing.T) {
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	items, err := service.Watchlist(context.Background())
	if err != nil {
		t.Fatalf("没有自选时不应报错: %v", err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("应返回空切片，实际 %#v", items)
	}
}

func TestSyncManualFilePersistsToStore(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	writeManualWatchlist(t, dir, []contract.WatchItem{
		{Code: "600000", Name: "浦发银行", Group: "自选股", Order: 1},
		{Code: "000001", Name: "平安银行", Group: "自选股", Order: 2},
	})
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: store, DataDir: dir})
	// 隔离本机同花顺容器：这些用例只验证手工导入链路
	service.home = t.TempDir()

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步不应返回 error: %v", err)
	}
	if !result.Success {
		t.Fatalf("同步应成功: %+v", result)
	}
	if result.Source != "manual" {
		t.Fatalf("来源 = %q，期望 manual", result.Source)
	}
	if result.Added != 2 || result.Removed != 0 {
		t.Fatalf("新增/移除 = %d/%d，期望 2/0", result.Added, result.Removed)
	}
	if len(result.Groups) != 1 || result.Groups[0] != "自选股" {
		t.Fatalf("分组 = %v", result.Groups)
	}

	// 落盘后 Watchlist 应读 store
	items, err := service.Watchlist(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("store 条目数 = %d，期望 2", len(items))
	}
	if items[0].Source != "manual" {
		t.Fatalf("来源标记 = %q，期望 manual（同步来源要如实写进条目）", items[0].Source)
	}

	meta := service.WatchMeta()
	if meta.Source != "manual" || meta.UpdatedAt == "" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestSyncMergeAddedRemoved(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	writeManualWatchlist(t, dir, []contract.WatchItem{
		{Code: "600000", Group: "自选股", Order: 1},
		{Code: "000001", Group: "自选股", Order: 2},
	})
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: store, DataDir: dir})
	service.home = t.TempDir()
	if _, err := service.SyncTHSWatchlist(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 第二次同步：600000 保留，000001 移除，300750 新增
	writeManualWatchlist(t, dir, []contract.WatchItem{
		{Code: "600000", Group: "自选股", Order: 1},
		{Code: "300750", Group: "自选股", Order: 2},
	})
	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Removed != 1 {
		t.Fatalf("新增/移除 = %d/%d，期望 1/1", result.Added, result.Removed)
	}
	items, _ := service.Watchlist(context.Background())
	if len(items) != 2 {
		t.Fatalf("合并后条目数 = %d，期望 2", len(items))
	}
	for _, item := range items {
		if item.Code == "000001" {
			t.Fatal("已移除的代码不应残留")
		}
	}
}

func TestSyncNoSourceReportsNeedFlags(t *testing.T) {
	// 空的临时 home：既没有同花顺容器，也没有 cookie 与手工文件
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = t.TempDir()

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步不应返回 error: %v", err)
	}
	if result.Success {
		t.Fatalf("没有来源时不应报成功: %+v", result)
	}
	if !result.NeedWebLogin {
		t.Fatal("缺登录态时应置 NeedWebLogin")
	}
	if result.NeedFullDiskAccess {
		t.Fatal("没有同花顺容器时不应要求完全磁盘访问权限")
	}
	if !strings.Contains(result.Message, "没有可同步") {
		t.Fatalf("结果文案应说明缺什么，实际 %q", result.Message)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("应带上具体原因作为 warnings")
	}
}

func TestSyncNeedFullDiskAccess(t *testing.T) {
	home := t.TempDir()
	container := filepath.Join(home, thsContainerRel)
	if err := os.MkdirAll(container, 0o755); err != nil {
		t.Fatal(err)
	}
	// 容器目录本身不可读：模拟 macOS 隐私保护拒绝
	if err := os.Chmod(container, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(container, 0o755) })
	if _, err := os.ReadDir(container); err == nil {
		t.Skip("当前用户可读 chmod 000 目录（可能以 root 运行），跳过权限用例")
	}

	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = home
	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步不应返回 error: %v", err)
	}
	if !result.NeedFullDiskAccess {
		t.Fatalf("应标记 NeedFullDiskAccess: %+v", result)
	}
}

func TestSyncFromLocalContainer(t *testing.T) {
	home := t.TempDir()
	publicDir := filepath.Join(home, thsContainerRel, thsCloudStoreRel, "blockstock_999999", "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 把真实分组归档放进临时容器（只含分组名与代码）
	entries, err := os.ReadDir(filepath.Join("testdata", "ths_local"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join("testdata", "ths_local", entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(publicDir, entry.Name()), raw, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	store := newMemStore()
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: store, DataDir: t.TempDir()})
	service.home = home

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !result.Success || result.Source != "ths-local" {
		t.Fatalf("应从本机容器同步: %+v", result)
	}
	if len(result.Groups) == 0 {
		t.Fatal("分组为空")
	}
	if result.Added == 0 {
		t.Fatalf("应有新增条目: %+v", result)
	}
	if result.NeedFullDiskAccess || result.NeedWebLogin {
		t.Fatalf("本地成功时不应要求授权: %+v", result)
	}
	items, err := service.Watchlist(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != result.Added {
		t.Fatalf("store 条目数 %d 与新增数 %d 不一致", len(items), result.Added)
	}
}

func TestSyncWithoutStore(t *testing.T) {
	dir := t.TempDir()
	writeManualWatchlist(t, dir, []contract.WatchItem{{Code: "600000", Group: "自选股"}})
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: nil, DataDir: dir})
	service.home = t.TempDir()

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步不应返回 error: %v", err)
	}
	if result.Success {
		t.Fatal("没有 Store 时不能假装保存成功")
	}
	if !strings.Contains(result.Message, "保存失败") {
		t.Fatalf("应说明保存失败原因，实际 %q", result.Message)
	}
}

func TestManualFileInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, watchFileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: dir})
	service.home = t.TempDir()

	if _, err := service.Watchlist(context.Background()); err == nil {
		t.Fatal("手工文件格式错误应报错")
	}
	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步不应返回 error: %v", err)
	}
	if result.Success {
		t.Fatal("手工文件损坏时不应报成功")
	}
}

func TestManualFilePathGuard(t *testing.T) {
	dir := t.TempDir()
	writeManualWatchlist(t, dir, []contract.WatchItem{{Code: "600000", Group: "自选股"}})
	guard := newFakeGuard()
	guard.denyPaths = []string{dir}
	service := newTestService(t, Config{Guard: guard, Store: newMemStore(), DataDir: dir})

	if _, err := service.Watchlist(context.Background()); err == nil {
		t.Fatal("自选文件路径被安全策略拒绝时应报错")
	}
}
func TestSyncFillsNamesFromSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	// 手工文件只给代码不给名字，同步时应该用批量快照补上
	writeManualWatchlist(t, dir, []contract.WatchItem{{Code: "600000", Group: "自选股"}})
	transport := (&fakeTransport{}).
		add("qt.gtimg.cn/q=", readTestdata(t, "tx_quote_600000.txt"))

	service, _ := newServiceWithTransport(t, store, transport)
	service.cfg.DataDir = dir
	service.dir = dir
	service.home = t.TempDir()

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !result.Success {
		t.Fatalf("同步应成功: %+v", result)
	}
	items, err := service.Watchlist(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("条目数 = %d，期望 1", len(items))
	}
	if items[0].Name != "浦发银行" {
		t.Fatalf("同步应回填名称，实际 %q", items[0].Name)
	}
	// 名称要进 market_cache，离线也能显示
	if cached := service.CachedName(mustParse(t, "sh600000")); cached != "浦发银行" {
		t.Fatalf("名称缓存 = %q，期望 浦发银行", cached)
	}
}

func TestSyncFromLocalContainerFillsNamesAndSource(t *testing.T) {
	home := t.TempDir()
	publicDir := filepath.Join(home, thsContainerRel, thsCloudStoreRel, "blockstock_999999", "public")
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "ths_local"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join("testdata", "ths_local", entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(publicDir, entry.Name()), raw, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	// 快照源只提供 600000 一只，用来验证「能取到名字的条目被回填、取不到的保持空但同步不失败」
	transport := (&fakeTransport{}).
		add("qt.gtimg.cn/q=", readTestdata(t, "tx_quote_600000.txt"))
	store := newMemStore()
	service, _ := newServiceWithTransport(t, store, transport)
	service.home = home

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !result.Success || result.Source != "ths-local" {
		t.Fatalf("应从本机容器同步: %+v", result)
	}
	items, err := service.Watchlist(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Source != "ths-local" {
			t.Fatalf("条目来源 = %q，期望 ths-local", item.Source)
		}
	}
	// fixture 里的代码与快照样本（600000）不重合，所以这里只验证「回填失败不影响同步」；
	// 回填机制本身由 TestFillWatchNames 直接覆盖。
	if len(items) == 0 {
		t.Fatal("同步后条目不应为空")
	}
}

func TestFillWatchNames(t *testing.T) {
	transport := (&fakeTransport{}).
		add("qt.gtimg.cn/q=", readTestdata(t, "tx_quote_600000.txt"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	items := []contract.WatchItem{
		{Code: "600000", Market: contract.MarketSH, Group: "自选股"},
		{Code: "600001", Market: contract.MarketSH, Group: "自选股"},
	}
	got := service.fillWatchNames(context.Background(), items, nil)
	if got[0].Name != "浦发银行" {
		t.Fatalf("600000 名称 = %q，期望 浦发银行（快照里有就该回填）", got[0].Name)
	}
	if got[1].Name != "" {
		t.Fatalf("600001 名称 = %q，快照里没有就该保持空", got[1].Name)
	}
	if cached := service.CachedName(mustParse(t, "sh600000")); cached != "浦发银行" {
		t.Fatalf("名称缓存 = %q，期望 浦发银行", cached)
	}
}

func TestQuoteManyCodesUsesBatchFallback(t *testing.T) {
	transport := standardRoutes(t)
	service, _ := newServiceWithTransport(t, newMemStore(), transport)

	codes := make([]string, 0, thsPerCodeLimit+2)
	codes = append(codes, "600000")
	for i := 1; i < thsPerCodeLimit+2; i++ {
		codes = append(codes, fmt.Sprintf("60%04d", i))
	}
	quotes, err := service.Quote(context.Background(), codes)
	if err != nil && !errors.Is(err, ErrPartial) {
		t.Fatalf("批量查询失败: %v", err)
	}
	if len(quotes) != 1 || quotes[0].Name != "浦发银行" {
		t.Fatalf("行情结果 = %+v", quotes)
	}
	if quotes[0].Source != sourceTencent {
		t.Fatalf("超过 %d 只时应走批量源，实际来源 %q", thsPerCodeLimit, quotes[0].Source)
	}
	if transport.countCalls("/v6/realhead/") != 0 {
		t.Fatalf("超过 %d 只时不应逐只抓同花顺，实际 %d 次", thsPerCodeLimit, transport.countCalls("/v6/realhead/"))
	}
}

func mustParse(t *testing.T, raw string) Code {
	t.Helper()
	code, err := ParseCode(raw)
	if err != nil {
		t.Fatalf("ParseCode(%q) 失败: %v", raw, err)
	}
	return code
}
func TestSyncReplacesStaleName(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	// 库里已经有一条乱码名字（历史版本留下的），同步必须用快照覆盖它
	stale := contract.WatchItem{Code: "600000", Market: contract.MarketSH, Group: "自选股", Name: "\uFFFD\uFFFD", Source: "ths"}
	raw, _ := json.Marshal(stale)
	if err := store.Put(bucketWatchlist, prefixWatchItem+"sh600000", raw); err != nil {
		t.Fatal(err)
	}
	writeManualWatchlist(t, dir, []contract.WatchItem{{Code: "600000", Group: "自选股"}})
	transport := (&fakeTransport{}).
		add("qt.gtimg.cn/q=", readTestdata(t, "tx_quote_600000.txt"))

	service, _ := newServiceWithTransport(t, store, transport)
	service.dir = dir
	service.cfg.DataDir = dir
	service.home = t.TempDir()

	if _, err := service.SyncTHSWatchlist(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	items, err := service.Watchlist(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("条目数 = %d，期望 1", len(items))
	}
	if items[0].Name != "浦发银行" {
		t.Fatalf("旧乱码名字应被快照覆盖，实际 %q", items[0].Name)
	}
}

func TestSyncKeepsPreviousNameWhenSnapshotFails(t *testing.T) {
	dir := t.TempDir()
	store := newMemStore()
	kept := contract.WatchItem{Code: "600000", Market: contract.MarketSH, Group: "自选股", Name: "用户手填的名字"}
	raw, _ := json.Marshal(kept)
	if err := store.Put(bucketWatchlist, prefixWatchItem+"sh600000", raw); err != nil {
		t.Fatal(err)
	}
	writeManualWatchlist(t, dir, []contract.WatchItem{{Code: "600000", Group: "自选股"}})
	// 快照源全部不可用
	transport := (&fakeTransport{}).
		addStatus("qt.gtimg.cn", 400).
		addStatus("hq.sinajs.cn", 400)

	service, _ := newServiceWithTransport(t, store, transport)
	service.dir = dir
	service.cfg.DataDir = dir
	service.home = t.TempDir()

	if _, err := service.SyncTHSWatchlist(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	items, _ := service.Watchlist(context.Background())
	if len(items) != 1 || items[0].Name != "用户手填的名字" {
		t.Fatalf("快照失败时应保留旧名字，实际 %+v", items)
	}
}
