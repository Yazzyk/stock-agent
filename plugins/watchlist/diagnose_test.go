package watchlist

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

func activateWith(t *testing.T, data contract.DataService) *plugin.Context {
	t.Helper()
	p := New()
	ctx := newTestContext(t, data, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	return ctx
}

// TestCheckPassesDiagnosisThrough 保证 check 原样透传数据层诊断，且不触发同步。
func TestCheckPassesDiagnosisThrough(t *testing.T) {
	want := contract.SyncCheck{
		Local:       contract.SyncSource{ID: "ths-local", Label: "同花顺客户端", State: "denied", Detail: "没有权限"},
		Web:         contract.SyncSource{ID: "ths-web", Label: "同花顺网页端", State: "no-login"},
		Manual:      contract.SyncSource{ID: "manual", Label: "手工导入", State: "missing"},
		Recommended: "none",
		Ready:       false,
		Message:     "还没有可用的自选来源",
	}
	data := &fakeData{check: want}
	ctx := activateWith(t, data)
	check, ok := ctx.Methods.Get("check")
	if !ok {
		t.Fatal("缺少 check 方法")
	}
	out, err := check.Fn(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("check 失败: %v", err)
	}
	got, isCheck := out.(contract.SyncCheck)
	if !isCheck {
		t.Fatalf("返回值类型 = %T", out)
	}
	if got.Local.State != "denied" || got.Ready || got.Recommended != "none" {
		t.Fatalf("诊断未透传: %+v", got)
	}
	if data.syncResult.Success {
		t.Fatal("check 绝不能顺带触发同步")
	}
}

// TestCheckDegradesWithoutDiagnoser 保证数据层没实现扩展时给可操作文案，而不是 500。
func TestCheckDegradesWithoutDiagnoser(t *testing.T) {
	p := New()
	ctx := newTestContext(t, nil, newMemStore())
	if err := p.Activate(ctx); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	check, _ := ctx.Methods.Get("check")
	out, err := check.Fn(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("降级不应报错: %v", err)
	}
	got := out.(contract.SyncCheck)
	if got.Ready || got.Recommended != "none" || !strings.Contains(got.Message, "版本不匹配") {
		t.Fatalf("降级诊断 = %+v", got)
	}
	if len(got.Steps) != 1 || got.Steps[0].Action.Kind != "retry-sync" {
		t.Fatalf("降级应给一个重试动作: %+v", got.Steps)
	}
}

// TestSaveCookieVerifiesFirst 覆盖「先验证再落盘」的两条分支。
func TestSaveCookieVerifiesFirst(t *testing.T) {
	data := &fakeData{saveCount: 3, check: contract.SyncCheck{Ready: true, Recommended: "ths-web"}}
	ctx := activateWith(t, data)
	save, _ := ctx.Methods.Get("saveCookie")

	out, err := save.Fn(context.Background(), json.RawMessage(`{"cookie":"Cookie: a=1; b=2"}`))
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	reply := out.(cookieReply)
	if !reply.OK || !strings.Contains(reply.Message, "3") {
		t.Fatalf("保存结果 = %+v", reply)
	}
	if data.savedCookie != "Cookie: a=1; b=2" {
		t.Fatalf("原始 cookie 应交给数据层清洗，实际 %q", data.savedCookie)
	}
	if reply.Check == nil || !reply.Check.Ready {
		t.Fatalf("保存成功后应回填最新诊断: %+v", reply.Check)
	}

	data.saveErr = errors.New("登录态无效（cookie 已过期或复制不完整）")
	out, err = save.Fn(context.Background(), json.RawMessage(`{"cookie":"a=1"}`))
	if err != nil {
		t.Fatalf("校验失败不应返回 error（前端要展示原因）: %v", err)
	}
	reply = out.(cookieReply)
	if reply.OK || !strings.Contains(reply.Message, "登录态无效") {
		t.Fatalf("校验失败结果 = %+v", reply)
	}
}

// TestCaptureCookie 覆盖抓取成功、抓取失败、抓取到但校验不过三种情况。
func TestCaptureCookie(t *testing.T) {
	data := &fakeData{capture: "userid=abc", captureSrc: "safari", saveCount: 2}
	ctx := activateWith(t, data)
	capture, _ := ctx.Methods.Get("captureCookie")

	out, err := capture.Fn(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("抓取不应报 error: %v", err)
	}
	reply := out.(cookieReply)
	if !reply.OK || reply.Source != "safari" {
		t.Fatalf("抓取结果 = %+v", reply)
	}
	if !strings.Contains(reply.Message, "Chrome") {
		t.Fatalf("成功文案也要说明 Chrome 需粘贴，实际 %q", reply.Message)
	}

	data.captureErr = errors.New("检测到 Chromium 系浏览器 cookie，但它的值是钥匙串加密的：请粘贴")
	out, _ = capture.Fn(context.Background(), json.RawMessage("{}"))
	reply = out.(cookieReply)
	if reply.OK || reply.Source != "" || !strings.Contains(reply.Message, "粘贴") {
		t.Fatalf("抓取失败结果 = %+v", reply)
	}

	data.captureErr = nil
	data.saveErr = errors.New("登录态无效")
	out, _ = capture.Fn(context.Background(), json.RawMessage("{}"))
	reply = out.(cookieReply)
	if reply.OK || !strings.Contains(reply.Message, "校验未通过") {
		t.Fatalf("抓到但校验不过的结果 = %+v", reply)
	}
}

// TestPrivacyReply 钉住深链与路径：错了用户会点到一个空白的系统设置页。
func TestPrivacyReply(t *testing.T) {
	ctx := activateWith(t, &fakeData{})
	ctx.DataDir = "/tmp/guanlan-test"
	privacy, ok := ctx.Methods.Get("privacy")
	if !ok {
		t.Fatal("缺少 privacy 方法")
	}
	out, err := privacy.Fn(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	reply := out.(privacyReply)
	if reply.SettingsURL != "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles" {
		t.Fatalf("settingsUrl = %q", reply.SettingsURL)
	}
	if reply.DataDir != "/tmp/guanlan-test" {
		t.Fatalf("dataDir = %q", reply.DataDir)
	}
	if reply.Hint == "" || !strings.Contains(reply.Hint, "完全磁盘访问权限") {
		t.Fatalf("hint = %q", reply.Hint)
	}
}

// TestAppBundlePath 钉住 privacy.appPath 的口径：完全磁盘访问列表里要加的是 .app 包，
// 不是包内二进制；非包内路径必须原样返回，不能被误判。
func TestAppBundlePath(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"应用包内二进制", "/Applications/观澜.app/Contents/MacOS/guanlan", "/Applications/观澜.app"},
		{"含空格与中文的路径", "/Users/a b/My Apps/观 澜.app/Contents/MacOS/guanlan", "/Users/a b/My Apps/观 澜.app"},
		{"大写 Contents/MacOS", "/opt/Tool.APP/Contents/MacOS/tool", "/opt/Tool.APP"},
		{"普通二进制", "/usr/local/bin/guanlan", "/usr/local/bin/guanlan"},
		{"包内但不是 MacOS 目录", "/Applications/Foo.app/Contents/Resources/helper", "/Applications/Foo.app/Contents/Resources/helper"},
		{"Contents/MacOS 但外层不是 .app", "/tmp/build/Contents/MacOS/guanlan", "/tmp/build/Contents/MacOS/guanlan"},
		{"只叫 app 后缀但不是目录布局", "/tmp/Foo.app/guanlan", "/tmp/Foo.app/guanlan"},
		{"空路径", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appBundlePath(tc.in); got != tc.want {
				t.Fatalf("appBundlePath(%q) = %q，期望 %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPrivacyAppPathMatchesBundleRule 保证 privacy 方法真的用了上面这条规则（不是直接抛原始路径）。
func TestPrivacyAppPathMatchesBundleRule(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Skip("拿不到测试进程的可执行路径")
	}
	ctx := activateWith(t, &fakeData{})
	privacy, _ := ctx.Methods.Get("privacy")
	out, callErr := privacy.Fn(context.Background(), json.RawMessage("{}"))
	if callErr != nil {
		t.Fatal(callErr)
	}
	reply := out.(privacyReply)
	if reply.AppPath != appBundlePath(executable) {
		t.Fatalf("appPath = %q，期望按包规则换算成 %q", reply.AppPath, appBundlePath(executable))
	}
	if strings.HasSuffix(reply.AppPath, "/Contents/MacOS") || strings.Contains(reply.AppPath, "/Contents/MacOS/") {
		t.Fatalf("appPath 不能停留在包内二进制/目录: %q", reply.AppPath)
	}
}
