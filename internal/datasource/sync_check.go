package datasource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 同步向导用到的两个外部入口：
//   - 同花顺网页版个人中心（登录后 tab=zx 就是自选页），与 go-stock 的 ths_selfstock_web.go 一致；
//   - macOS「完全磁盘访问权限」深链，把用户直接送到正确的系统设置页，而不是让用户自己翻。
const (
	thsLoginURL        = "https://t.10jqka.com.cn/newcircle/user/userPersonal/?from=finance&tab=zx"
	privacySettingsURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"
)

// SyncSource.State 的取值（与 contract 注释里的枚举逐字一致）。
const (
	syncStateOK      = "ok"
	syncStateDenied  = "denied"
	syncStateNoLogin = "no-login"
	syncStateMissing = "missing"
	syncStateEmpty   = "empty"
)

// SyncCheck 做一次只读前置诊断：本机容器 / 网页登录态 / 手工文件三路各能不能用，
// 以及向导每一步该给用户什么动作。
//
// 纪律：不发任何网络请求、绝不触发同步 —— 前端向导会 2s 轮询它，
// 一旦 check 里带请求，60 次轮询就是 60 次上游调用。
func (s *Service) SyncCheck(_ context.Context) (contract.SyncCheck, error) {
	check := contract.SyncCheck{
		Local:     s.checkLocal(),
		Web:       s.checkWeb(),
		Manual:    s.checkManual(),
		CheckedAt: s.now().Format(time.RFC3339),
	}
	switch {
	case check.Local.Available:
		check.Recommended = "ths-local"
	case check.Web.Available:
		check.Recommended = "ths-web"
	case check.Manual.Available:
		check.Recommended = "manual"
	default:
		check.Recommended = "none"
	}
	// Ready 只在「真的有可用来源」时为 true，且 Recommended 必须指向那一个来源。
	check.Ready = check.Recommended != "none"
	if check.Ready {
		check.Message = "可以同步：" + recommendedLabel(check.Recommended)
	} else {
		check.Message = "还没有可用的自选来源：按向导任选一条完成——授权本机同花顺、登录网页版，或放一份 watchlist.json"
	}
	check.Steps = s.syncSteps(check)
	return check, nil
}

// checkLocal 探测 macOS 同花顺客户端容器，错误分类复用 scanTHSLocal。
func (s *Service) checkLocal() contract.SyncSource {
	local := contract.SyncSource{ID: "ths-local", Label: "同花顺客户端"}
	if s.home == "" {
		local.State = syncStateMissing
		local.Detail = "无法定位用户主目录，跳过本机同花顺容器"
		return local
	}
	groups, _, err := scanTHSLocal(s.home)
	switch {
	case err == nil && len(groups) > 0:
		local.Available, local.State = true, syncStateOK
		local.Groups = len(groups)
		local.Items = countGroupCodes(groups)
		local.Detail = fmt.Sprintf("已读到本机同花顺 %d 个分组、%d 只标的（只读，不修改客户端任何文件）", local.Groups, local.Items)
	case errors.Is(err, ErrNeedFullDiskAccess):
		local.State = syncStateDenied
		local.Detail = err.Error()
	case errors.Is(err, ErrTHSNoGroup):
		local.State = syncStateEmpty
		local.Detail = err.Error()
	case err != nil:
		local.State = syncStateMissing
		local.Detail = err.Error()
	default:
		local.State = syncStateEmpty
		local.Detail = "同花顺容器可读，但没有任何自选分组"
	}
	return local
}

// checkWeb 只看登录态是否已保存（环境变量优先，其次 <DataDir>/ths_cookie.txt），不做在线校验；
// 但会参考「上一次在线请求是否被服务端判失效」，避免把过期 cookie 一直报成可用。
func (s *Service) checkWeb() contract.SyncSource {
	web := contract.SyncSource{ID: "ths-web", Label: "同花顺网页端"}
	if s.thsCookie() == "" {
		web.State = syncStateNoLogin
		web.Detail = ErrNeedWebLogin.Error()
		return web
	}
	if reason := s.WebLoginInvalid(); reason != "" {
		web.State = syncStateNoLogin
		web.Detail = "已保存的登录态被服务端判定失效（" + reason + "）：请重新登录后保存或抓取"
		return web
	}
	web.Available, web.State = true, syncStateOK
	if strings.TrimSpace(os.Getenv(thsCookieEnv)) != "" {
		web.Detail = "已从环境变量 " + thsCookieEnv + " 读到登录态（未在线校验，同步时才真正请求）"
	} else {
		web.Detail = "已保存登录态 " + s.thsCookiePath() + "（未在线校验，同步时才真正请求）"
	}
	return web
}

// checkManual 探测 <DataDir>/watchlist.json 这条永远可用的兜底。
func (s *Service) checkManual() contract.SyncSource {
	manual := contract.SyncSource{ID: "manual", Label: "手工导入"}
	path := s.manualWatchlistPath()
	if path == "" {
		manual.State = syncStateMissing
		manual.Detail = "未配置数据目录，无法读取 watchlist.json"
		return manual
	}
	items, _, err := s.loadManualWatchlist()
	switch {
	case err == nil && len(items) > 0:
		manual.Available, manual.State = true, syncStateOK
		manual.Items = len(items)
		manual.Groups = len(groupsFromItems(items))
		manual.Detail = fmt.Sprintf("已读到 %s（%d 只）", path, len(items))
	case err == nil:
		manual.State = syncStateEmpty
		manual.Detail = path + " 存在但 items 为空"
	case errors.Is(err, os.ErrNotExist):
		manual.State = syncStateMissing
		manual.Detail = "数据目录下没有 watchlist.json（可手工放一份，items 元素含 code/name/group）"
	default:
		manual.State = syncStateMissing
		manual.Detail = err.Error()
	}
	return manual
}

// syncSteps 把三路诊断翻译成向导步骤，每步带一个可执行动作。
//
// 步骤 ID 保持稳定：local / web-login / web-capture / manual。
func (s *Service) syncSteps(check contract.SyncCheck) []contract.SyncStep {
	localAction := noneAction("无需操作")
	if !check.Local.Available && check.Local.State == syncStateDenied {
		localAction = contract.SyncAction{Kind: "open-settings", Label: "打开「完全磁盘访问权限」设置", Value: privacySettingsURL}
	}
	steps := []contract.SyncStep{{
		ID: "local", Title: "本机同花顺客户端",
		Detail: check.Local.Detail, Done: check.Local.Available, Action: localAction,
	}}

	loginAction := noneAction("已登录")
	if !check.Web.Available {
		loginAction = contract.SyncAction{Kind: "open-url", Label: "打开同花顺登录页", Value: thsLoginURL}
	}
	steps = append(steps, contract.SyncStep{
		ID: "web-login", Title: "网页登录态",
		Detail: check.Web.Detail, Done: check.Web.Available, Action: loginAction,
	})

	// 抓取是一次性动作而不是状态：只有在还没登录态时才给按钮，
	// Done 恒为 false，避免和 web-login 同时被当作「已完成/已就绪」。
	captureAction := contract.SyncAction{Kind: "none", Label: "已登录，无需抓取"}
	if !check.Web.Available {
		captureAction = contract.SyncAction{Kind: "capture-cookie", Label: "从浏览器抓取登录态"}
	}
	steps = append(steps, contract.SyncStep{
		ID: "web-capture", Title: "从本机浏览器抓取登录态",
		Detail: "Safari 可直接读取；Chrome/Edge 的 cookie 是加密的，需手动粘贴。抓取成功后才会保存为可用登录态",
		Done:   false, Action: captureAction,
	})

	manualAction := noneAction("无需操作")
	if !check.Manual.Available && s.dir != "" {
		manualAction = contract.SyncAction{Kind: "copy", Label: "复制数据目录路径", Value: s.dir}
	}
	steps = append(steps, contract.SyncStep{
		ID: "manual", Title: "手工导入 watchlist.json",
		Detail: check.Manual.Detail, Done: check.Manual.Available, Action: manualAction,
	})
	return steps
}

// markWebLoginInvalid 记录一次「服务端拒绝登录态」，只影响诊断展示，不删用户文件。
func (s *Service) markWebLoginInvalid(reason string) {
	s.webLoginMu.Lock()
	defer s.webLoginMu.Unlock()
	s.webLoginInvalid = strings.TrimSpace(reason)
}

// clearWebLoginInvalid 在登录态被成功验证后清除标记。
func (s *Service) clearWebLoginInvalid() {
	s.webLoginMu.Lock()
	defer s.webLoginMu.Unlock()
	s.webLoginInvalid = ""
}

// WebLoginInvalid 返回上一次登录态被拒的原因；空串表示没有已知问题。
func (s *Service) WebLoginInvalid() string {
	s.webLoginMu.Lock()
	defer s.webLoginMu.Unlock()
	return s.webLoginInvalid
}

// recommendedLabel 把 recommended 来源说成人话（面板顶部状态条直接用这句）。
func recommendedLabel(source string) string {
	switch source {
	case "ths-local":
		return "将使用本机同花顺客户端数据"
	case "ths-web":
		return "将使用同花顺网页登录态"
	case "manual":
		return "将使用手工导入的 watchlist.json"
	}
	return ""
}

func noneAction(label string) contract.SyncAction {
	return contract.SyncAction{Kind: "none", Label: label}
}

// countGroupCodes 统计分组里的标的总数（同花顺同一只票可能出现在多个分组，这里按分组累计，
// 与 SyncCheck 的 Items 语义一致：告诉用户「能读到多少条」，不是去重后的数量）。
func countGroupCodes(groups []thsGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Codes)
	}
	return total
}
