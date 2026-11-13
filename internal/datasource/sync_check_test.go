package datasource

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// installTHSContainer 在临时 home 下伪造一个同花顺容器（用 testdata 里的真实分组归档）。
func installTHSContainer(t *testing.T, home string) string {
	t.Helper()
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
	return filepath.Join(home, thsContainerRel)
}

// TestSyncCheckLocalAvailable 覆盖「本机同花顺可用」分支：本地来源 recommended、向导里的本机步骤已完成。
func TestSyncCheckLocalAvailable(t *testing.T) {
	home := t.TempDir()
	installTHSContainer(t, home)
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = home

	check, err := service.SyncCheck(context.Background())
	if err != nil {
		t.Fatalf("诊断失败: %v", err)
	}
	if !check.Ready || check.Recommended != "ths-local" {
		t.Fatalf("recommended=%q ready=%v，期望 ths-local/true", check.Recommended, check.Ready)
	}
	if check.Local.State != syncStateOK || !check.Local.Available {
		t.Fatalf("local = %+v", check.Local)
	}
	if check.Local.Groups == 0 || check.Local.Items == 0 {
		t.Fatalf("local 应报出分组数与标的数: %+v", check.Local)
	}
	if check.Web.State != syncStateNoLogin {
		t.Fatalf("没有 cookie 时 web 应为 no-login，实际 %q", check.Web.State)
	}
	if check.Manual.State != syncStateMissing {
		t.Fatalf("没有 watchlist.json 时 manual 应为 missing，实际 %q", check.Manual.State)
	}
	if check.CheckedAt == "" {
		t.Fatal("checkedAt 不能为空")
	}
	step, ok := stepByID(check.Steps, "local")
	if !ok || !step.Done {
		t.Fatalf("local 步骤应为已完成: %+v", check.Steps)
	}
	if step.Action.Kind != "none" {
		t.Fatalf("local 可用时不该再给动作，实际 %q", step.Action.Kind)
	}
}

// TestSyncCheckNeedFullDiskAccess 覆盖「被 macOS 隐私保护拦住」分支：
// 容器目录读不了（chmod 000）时必须报 denied + open-settings，而不是「没装客户端」。
func TestSyncCheckNeedFullDiskAccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 会绕过文件权限，这个用例无法生效")
	}
	home := t.TempDir()
	container := installTHSContainer(t, home)
	if err := os.Chmod(container, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(container, 0o755) })

	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = home

	check, err := service.SyncCheck(context.Background())
	if err != nil {
		t.Fatalf("诊断失败: %v", err)
	}
	if check.Local.State != syncStateDenied || check.Local.Available {
		t.Fatalf("local = %+v，期望 denied", check.Local)
	}
	if !strings.Contains(check.Local.Detail, "完全磁盘访问权限") {
		t.Fatalf("限制原因应说清缺什么权限，实际 %q", check.Local.Detail)
	}
	step, ok := stepByID(check.Steps, "local")
	if !ok {
		t.Fatal("缺少 local 步骤")
	}
	if step.Action.Kind != "open-settings" || step.Action.Value != privacySettingsURL {
		t.Fatalf("local 步骤动作 = %+v，期望 open-settings 指向隐私设置深链", step.Action)
	}
	if check.Ready || check.Recommended != "none" {
		t.Fatalf("三路都不可用时必须 ready=false/recommended=none，实际 %v/%q", check.Ready, check.Recommended)
	}
}

// TestSyncCheckNeedWebLogin 覆盖「只剩网页登录这条路」分支：
// 向导必须给出登录页、抓取与复制数据目录三种动作。
func TestSyncCheckNeedWebLogin(t *testing.T) {
	t.Setenv(thsCookieEnv, "")
	dataDir := t.TempDir()
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: dataDir})
	service.home = t.TempDir() // 空的 home：没有同花顺容器

	check, err := service.SyncCheck(context.Background())
	if err != nil {
		t.Fatalf("诊断失败: %v", err)
	}
	if check.Ready || check.Recommended != "none" {
		t.Fatalf("没有可用来源时必须 ready=false/recommended=none，实际 %v/%q", check.Ready, check.Recommended)
	}
	if check.Web.State != syncStateNoLogin || !strings.Contains(check.Web.Detail, "t.10jqka.com.cn") {
		t.Fatalf("web = %+v，期望 no-login 且说明去哪儿登录", check.Web)
	}
	login, _ := stepByID(check.Steps, "web-login")
	if login.Done || login.Action.Kind != "open-url" || login.Action.Value != thsLoginURL {
		t.Fatalf("web-login 步骤 = %+v，期望 open-url 指向登录页", login)
	}
	capture, ok := stepByID(check.Steps, "web-capture")
	if !ok || capture.Action.Kind != "capture-cookie" || capture.Done {
		t.Fatalf("web-capture 步骤 = %+v，期望未完成且给了抓取动作", capture)
	}
	manual, _ := stepByID(check.Steps, "manual")
	if manual.Done || manual.Action.Kind != "copy" || manual.Action.Value != dataDir {
		t.Fatalf("manual 步骤 = %+v，期望 copy 指向数据目录", manual)
	}
}

// TestSyncCheckWebCookieReady 覆盖「已保存登录态」：recommended 必须指向 ths-web，且抓取步骤不再给按钮。
func TestSyncCheckWebCookieReady(t *testing.T) {
	t.Setenv(thsCookieEnv, "u=1; v=2")
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	service.home = t.TempDir()

	check, err := service.SyncCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.Ready || check.Recommended != "ths-web" {
		t.Fatalf("recommended=%q ready=%v，期望 ths-web/true", check.Recommended, check.Ready)
	}
	if check.Web.State != syncStateOK || !check.Web.Available {
		t.Fatalf("web = %+v", check.Web)
	}
	if strings.Contains(check.Web.Detail, "u=1") {
		t.Fatal("诊断详情不得回显 cookie 内容")
	}
	login, _ := stepByID(check.Steps, "web-login")
	if !login.Done {
		t.Fatalf("已保存登录态时 web-login 应已完成: %+v", login)
	}
	capture, _ := stepByID(check.Steps, "web-capture")
	if capture.Done {
		t.Fatal("抓取是一次性动作，Done 恒为 false")
	}
	if capture.Action.Kind != "none" {
		t.Fatalf("已登录时不该再给抓取按钮，实际 %q", capture.Action.Kind)
	}
}

// TestSyncCheckManualFallback 覆盖「靠手工 watchlist.json」：这是没有权限也没有登录态时的最后一条路。
func TestSyncCheckManualFallback(t *testing.T) {
	t.Setenv(thsCookieEnv, "")
	dir := t.TempDir()
	writeManualWatchlist(t, dir, []contract.WatchItem{
		{Code: "600000", Name: "浦发银行", Group: "自选股"},
		{Code: "000001", Name: "平安银行", Group: "自选股"},
	})
	service := newTestService(t, Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: dir})
	service.home = t.TempDir()

	check, err := service.SyncCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.Ready || check.Recommended != "manual" {
		t.Fatalf("recommended=%q ready=%v，期望 manual/true", check.Recommended, check.Ready)
	}
	if check.Manual.Items != 2 || check.Manual.State != syncStateOK {
		t.Fatalf("manual = %+v，期望 2 只 ok", check.Manual)
	}
	manual, _ := stepByID(check.Steps, "manual")
	if !manual.Done || manual.Action.Kind != "none" {
		t.Fatalf("manual 步骤 = %+v", manual)
	}
}

// TestSyncCheckNoNetwork 钉住「check 绝不联网」这条纪律：前端会 2s 轮询它。
func TestSyncCheckNoNetwork(t *testing.T) {
	t.Setenv(thsCookieEnv, "u=1")
	transport := &fakeTransport{}
	service, _ := newServiceWithTransport(t, newMemStore(), transport)
	service.home = t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := service.SyncCheck(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls := len(transport.calls); calls != 0 {
		t.Fatalf("check 打了 %d 次请求，必须为 0", calls)
	}
}

// TestSyncResultBackfillsCheck 保证 sync 返回里带最新诊断（前端据此收起向导）。
func TestSyncResultBackfillsCheck(t *testing.T) {
	t.Setenv(thsCookieEnv, "")
	dir := t.TempDir()
	writeManualWatchlist(t, dir, []contract.WatchItem{{Code: "600000", Group: "自选股"}})
	transport := (&fakeTransport{}).add("qt.gtimg.cn/q=", readTestdata(t, "tx_quote_600000.txt"))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)
	service.dir = dir
	service.cfg.DataDir = dir
	service.home = t.TempDir()

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	if !result.Success || result.Source != "manual" {
		t.Fatalf("应从手工文件同步: %+v", result)
	}
	if result.Check == nil {
		t.Fatal("同步成功后必须回填 Check")
	}
	if result.Check.Recommended != "manual" || !result.Check.Ready {
		t.Fatalf("回填的诊断 = %+v，期望 manual/ready", result.Check)
	}
}

func stepByID(steps []contract.SyncStep, id string) (contract.SyncStep, bool) {
	for _, step := range steps {
		if step.ID == id {
			return step, true
		}
	}
	return contract.SyncStep{}, false
}

// TestSyncCheckMarksExpiredCookie 是「过期 cookie 别一直报 ok」的回归：
// 同步被服务端判失效后，诊断必须转为 no-login，否则前端会对着过期登录态反复自动同步。
func TestSyncCheckMarksExpiredCookie(t *testing.T) {
	t.Setenv(thsCookieEnv, "u=1")
	transport := (&fakeTransport{}).add("getSelfStockWithMarket", []byte(`{"errorCode":-1,"errorMsg":"未登录","result":[]}`))
	service, _ := newServiceWithTransport(t, newMemStore(), transport)
	service.home = t.TempDir()

	// 同步前：有 cookie 就报 ok（诊断不在线校验，这是刻意的）
	before, err := service.SyncCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !before.Web.Available {
		t.Fatalf("同步前 web 应为可用: %+v", before.Web)
	}

	result, err := service.SyncTHSWatchlist(context.Background())
	if err != nil {
		t.Fatalf("同步不应返回 error: %v", err)
	}
	if !result.NeedWebLogin {
		t.Fatalf("服务端拒绝登录态时应置 NeedWebLogin: %+v", result)
	}
	if result.Check == nil || result.Check.Web.Available || result.Check.Web.State != syncStateNoLogin {
		t.Fatalf("同步后回填的诊断应显示登录态失效: %+v", result.Check)
	}
	if result.Check.Ready {
		t.Fatal("没有任何可用来源时 ready 必须为 false")
	}

	// 换一份可用 cookie 校验成功后，标记要自动清掉（不需要重启）
	transport.routes = nil
	transport.add("getSelfStockWithMarket", []byte(thsWebFixture))
	if _, err := service.VerifyTHSCookie(context.Background(), "u=2"); err != nil {
		t.Fatalf("新 cookie 应校验通过: %v", err)
	}
	if reason := service.WebLoginInvalid(); reason != "" {
		t.Fatalf("校验成功后应清除失效标记，实际 %q", reason)
	}
	after, err := service.SyncCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !after.Web.Available || after.Web.State != syncStateOK {
		t.Fatalf("恢复后 web 应重新可用: %+v", after.Web)
	}
	if !after.Ready || after.Recommended != "ths-web" {
		t.Fatalf("恢复后 recommended 应指向 ths-web: %+v", after)
	}
}
