package watchlist

// 同步自助化：前置诊断、登录态保存、浏览器抓取与隐私设置入口。
//
// 数据层（internal/datasource）提供这些能力，但插件不 import 它 —— 用结构化类型断言，
// 这样演示模式/测试桩只要实现 contract.DataService 也能跑，不实现时给可读的降级说明，
// 而不是让整个面板因为「版本不匹配」直接白屏。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// macFullDiskAccessURL 是 macOS「完全磁盘访问权限」设置页深链（与 datasource 内部常量一致）。
const macFullDiskAccessURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"

// syncDiagnoser 是数据层可选提供的同步诊断能力。
// 方法签名与 internal/datasource 的实现逐字对齐；任一方法缺失即整体不可用（宁可整块降级，
// 也不要「诊断能用、保存不能用」这种半截状态）。
type syncDiagnoser interface {
	SyncCheck(ctx context.Context) (contract.SyncCheck, error)
	VerifyTHSCookie(ctx context.Context, cookie string) (int, error)
	SaveTHSCookie(ctx context.Context, cookie string) (int, error)
	CaptureBrowserTHSCookie() (cookie string, source string, err error)
}

// cookieReply 是 saveCookie / captureCookie 的统一返回：ok=false 时 message 必须可操作。
type cookieReply struct {
	OK      bool                `json:"ok"`
	Source  string              `json:"source,omitempty"`
	Message string              `json:"message"`
	Check   *contract.SyncCheck `json:"check,omitempty"`
}

// privacyReply 告诉前端「去哪儿授权」以及本应用装在哪。
type privacyReply struct {
	SettingsURL string `json:"settingsUrl"`
	AppPath     string `json:"appPath"`
	DataDir     string `json:"dataDir"`
	Hint        string `json:"hint"`
}

// methodCheck 只读诊断，绝不触发同步。
func (p *Plugin) methodCheck(ctx context.Context, _ json.RawMessage) (any, error) {
	diagnoser, ok := p.diagnoser()
	if !ok {
		return unavailableCheck("当前数据层没有提供同步诊断（datasource 版本不匹配），请升级应用后重试"), nil
	}
	check, err := diagnoser.SyncCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("诊断同步条件失败: %w", err)
	}
	return check, nil
}

type cookieArgs struct {
	Cookie string `json:"cookie"`
}

// methodSaveCookie 先验证再落盘：校验不过绝不写文件，否则用户会以为「保存成功=可以用」。
func (p *Plugin) methodSaveCookie(ctx context.Context, payload json.RawMessage) (any, error) {
	var args cookieArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	diagnoser, ok := p.diagnoser()
	if !ok {
		return cookieReply{Message: "当前数据层不支持保存登录态（datasource 版本不匹配）"}, nil
	}
	count, err := diagnoser.SaveTHSCookie(ctx, args.Cookie)
	if err != nil {
		return cookieReply{Message: err.Error(), Check: p.currentCheck(ctx, diagnoser)}, nil
	}
	return cookieReply{
		OK:      true,
		Message: fmt.Sprintf("登录态已校验并保存，网页端可读到 %d 只自选", count),
		Check:   p.currentCheck(ctx, diagnoser),
	}, nil
}

// methodCaptureCookie 从本机浏览器尽力抓登录态：抓到就顺手校验+保存；抓不到给可读原因，不报 500。
func (p *Plugin) methodCaptureCookie(ctx context.Context, _ json.RawMessage) (any, error) {
	diagnoser, ok := p.diagnoser()
	if !ok {
		return cookieReply{Message: "当前数据层不支持从浏览器抓取登录态（datasource 版本不匹配）"}, nil
	}
	cookie, source, err := diagnoser.CaptureBrowserTHSCookie()
	if err != nil {
		return cookieReply{Message: err.Error(), Check: p.currentCheck(ctx, diagnoser)}, nil
	}
	count, saveErr := diagnoser.SaveTHSCookie(ctx, cookie)
	if saveErr != nil {
		return cookieReply{
			Source:  source,
			Message: fmt.Sprintf("已从%s读到 cookie，但校验未通过：%v", sourceLabelOf(source), saveErr),
			Check:   p.currentCheck(ctx, diagnoser),
		}, nil
	}
	return cookieReply{
		OK:      true,
		Source:  source,
		Message: fmt.Sprintf("已从%s读取登录态并通过校验（可读到 %d 只自选）；Chrome/Edge 的登录态仍需手动粘贴", sourceLabelOf(source), count),
		Check:   p.currentCheck(ctx, diagnoser),
	}, nil
}

// methodPrivacy 给前端「打开完全磁盘访问设置」所需的一切（深链 + 应用路径 + 数据目录）。
func (p *Plugin) methodPrivacy(_ context.Context, _ json.RawMessage) (any, error) {
	appPath := ""
	if executable, err := os.Executable(); err == nil {
		appPath = appBundlePath(executable)
	}
	return privacyReply{
		SettingsURL: macFullDiskAccessURL,
		AppPath:     appPath,
		DataDir:     p.ctx.DataDir,
		Hint: "首次使用本机同花顺同步前，需要在「系统设置 → 隐私与安全性 → 完全磁盘访问权限」" +
			"里勾选本应用，然后重启应用；未授权时也可以改用网页登录态或手工 watchlist.json",
	}, nil
}

// appBundlePath 把「.app 包内的可执行文件」换算成「.app 包目录」。
//
// 完全磁盘访问权限列表里用户需要添加的是 .app 包，而不是包内二进制：
//   - /Applications/观澜.app/Contents/MacOS/guanlan → /Applications/观澜.app
//   - /usr/local/bin/guanlan（非包内二进制）        → 原样返回
//
// 判定必须三级都对上（目录名 Contents、MacOS，且最外层以 .app 结尾），
// 否则形如 /tmp/build/Contents/MacOS/x 的普通构建产物会被误判成应用包。
func appBundlePath(executable string) string {
	cleaned := strings.TrimSpace(executable)
	if cleaned == "" {
		return ""
	}
	macOSDir := filepath.Dir(cleaned)
	if !strings.EqualFold(filepath.Base(macOSDir), "MacOS") {
		return cleaned
	}
	contentsDir := filepath.Dir(macOSDir)
	if !strings.EqualFold(filepath.Base(contentsDir), "Contents") {
		return cleaned
	}
	appDir := filepath.Dir(contentsDir)
	if !strings.HasSuffix(strings.ToLower(filepath.Base(appDir)), ".app") {
		return cleaned
	}
	return appDir
}

// diagnoser 取数据层的诊断能力；没有就返回 false，由调用方给出中文降级说明。
func (p *Plugin) diagnoser() (syncDiagnoser, bool) {
	if p.ctx == nil || p.ctx.Data == nil {
		return nil, false
	}
	diagnoser, ok := p.ctx.Data.(syncDiagnoser)
	return diagnoser, ok
}

// currentCheck 拿一次最新诊断回填到返回里；失败就不回填（前端会用自己的轮询兜底）。
func (p *Plugin) currentCheck(ctx context.Context, diagnoser syncDiagnoser) *contract.SyncCheck {
	check, err := diagnoser.SyncCheck(ctx)
	if err != nil {
		return nil
	}
	return &check
}

// unavailableCheck 是「数据层不支持诊断」时的降级结果：仍然给出三路状态与一个重试动作。
func unavailableCheck(message string) contract.SyncCheck {
	return contract.SyncCheck{
		Local:       contract.SyncSource{ID: "ths-local", Label: "同花顺客户端", State: "missing", Detail: message},
		Web:         contract.SyncSource{ID: "ths-web", Label: "同花顺网页端", State: "missing", Detail: message},
		Manual:      contract.SyncSource{ID: "manual", Label: "手工导入", State: "missing", Detail: message},
		Recommended: "none",
		Ready:       false,
		Message:     message,
		CheckedAt:   time.Now().Format(time.RFC3339),
		Steps: []contract.SyncStep{{
			ID: "retry", Title: "重新诊断", Detail: message, Done: false,
			Action: contract.SyncAction{Kind: "retry-sync", Label: "重新诊断"},
		}},
	}
}

// sourceLabelOf 把抓取来源转成中文，避免 UI 里出现裸英文 id。
func sourceLabelOf(source string) string {
	switch strings.TrimSpace(source) {
	case "safari":
		return "Safari"
	case "":
		return "本机浏览器"
	default:
		return source
	}
}
