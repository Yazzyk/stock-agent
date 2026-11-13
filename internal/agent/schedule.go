package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// ---------- 自动值班调度 ----------

// 值班阶段（顺序固定，前端按此渲染三张值班卡）。
const (
	PhasePremarket  = "premarket"
	PhaseIntraday   = "intraday"
	PhasePostmarket = "postmarket"
)

// phaseOrder 是调度顺序：同一 tick 只触发一个阶段（单飞）。
var phaseOrder = []string{PhasePremarket, PhaseIntraday, PhasePostmarket}

// 默认调度参数。
const (
	DefaultPremarketAt    = "09:15"
	DefaultPostmarketAt   = "15:10"
	DefaultIntradayEvery  = 30
	DefaultIntradayWindow = "09:30-11:30,13:00-15:00"
	// DefaultSchedulerTick 是调度节拍：每 20s 看一次是否到点。
	DefaultSchedulerTick = 20 * time.Second
	// atGrace 是 at 型阶段的补跑宽限：错过 at 后这段时间内仍会补跑，
	// 避免"刚启动/刚开机就永远不跑"；超过宽限则等下一个交易日。
	atGrace = 90 * time.Minute
	// everyMinutes 的合法区间。
	minEveryMinutes = 5
	maxEveryMinutes = 240
)

// PhaseSchedule 是一个值班阶段的调度配置与最近一次运行结果。
type PhaseSchedule struct {
	Phase         string `json:"phase"`
	Enabled       bool   `json:"enabled"`
	At            string `json:"at,omitempty"`
	EveryMinutes  int    `json:"everyMinutes,omitempty"`
	Window        string `json:"window,omitempty"`
	NextRunAt     string `json:"nextRunAt,omitempty"`
	LastRunAt     string `json:"lastRunAt,omitempty"`
	LastStatus    string `json:"lastStatus,omitempty"`
	LastSessionID string `json:"lastSessionId,omitempty"`
}

// SchedulePatch 是 setSchedule 的入参：零值字段表示"不改"。
type SchedulePatch struct {
	Phase        string
	Enabled      *bool
	At           string
	EveryMinutes *int
	// Window 是盘中交易时段（例如 "09:30-11:30,13:00-15:00"），只有盘中阶段可改。
	Window string
}

// scheduleDoc 是 bucket agent_schedule 的落盘结构。
type scheduleDoc struct {
	Phases    []PhaseSchedule `json:"phases"`
	UpdatedAt string          `json:"updatedAt,omitempty"`
}

var (
	shanghaiOnce sync.Once
	shanghaiZone *time.Location
)

// shanghaiLoc 返回 A 股所在的时区（无 tzdata 时退化为固定 +08:00）。
func shanghaiLoc() *time.Location {
	shanghaiOnce.Do(func() {
		if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
			shanghaiZone = loc
			return
		}
		shanghaiZone = time.FixedZone("CST", 8*60*60)
	})
	return shanghaiZone
}

func defaultPhases() []PhaseSchedule {
	return []PhaseSchedule{
		{Phase: PhasePremarket, Enabled: true, At: DefaultPremarketAt},
		{Phase: PhaseIntraday, Enabled: true, EveryMinutes: DefaultIntradayEvery, Window: DefaultIntradayWindow},
		{Phase: PhasePostmarket, Enabled: true, At: DefaultPostmarketAt},
	}
}

// parseClockMinutes 把 "09:15" 解析成当天分钟数。
func parseClockMinutes(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// atTimeOn 返回 day 当天第 minutes 分钟对应的时刻（Asia/Shanghai）。
func atTimeOn(day time.Time, minutes int) time.Time {
	loc := shanghaiLoc()
	d := day.In(loc)
	return time.Date(d.Year(), d.Month(), d.Day(), minutes/60, minutes%60, 0, 0, loc)
}

// sameDay 判断两个时刻是不是（上海时区的）同一天。
func sameDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	loc := shanghaiLoc()
	da, db := a.In(loc), b.In(loc)
	return da.Year() == db.Year() && da.YearDay() == db.YearDay()
}

// normalizeWindow 校验并规范化交易时段写法（"09:30-11:30,13:00-15:00"）：
// 每段必须是 HH:MM-HH:MM 且开始早于结束，最多两段（上午、下午）。
func normalizeWindow(raw string) (string, error) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bounds := strings.Split(part, "-")
		if len(bounds) != 2 {
			return "", fmt.Errorf("交易时段需要 HH:MM-HH:MM 格式（例如 09:30-11:30），收到 %q", part)
		}
		start, ok1 := parseClockMinutes(bounds[0])
		end, ok2 := parseClockMinutes(bounds[1])
		if !ok1 || !ok2 {
			return "", fmt.Errorf("交易时段需要 HH:MM-HH:MM 格式（例如 09:30-11:30），收到 %q", part)
		}
		if start >= end {
			return "", fmt.Errorf("交易时段的开始时间需早于结束时间，收到 %q", part)
		}
		out = append(out, fmt.Sprintf("%02d:%02d-%02d:%02d", start/60, start%60, end/60, end%60))
	}
	if len(out) == 0 {
		return "", fmt.Errorf("交易时段不能为空，例如 09:30-11:30,13:00-15:00")
	}
	if len(out) > 2 {
		return "", fmt.Errorf("交易时段最多两段（上午、下午），收到 %d 段", len(out))
	}
	return strings.Join(out, ","), nil
}

// parseWindow 把 "09:30-11:30,13:00-15:00" 解析成分钟区间（左闭右闭）。
func parseWindow(window string) [][2]int {
	out := make([][2]int, 0, 2)
	for _, part := range strings.Split(window, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bounds := strings.Split(part, "-")
		if len(bounds) != 2 {
			continue
		}
		start, ok1 := parseClockMinutes(bounds[0])
		end, ok2 := parseClockMinutes(bounds[1])
		if !ok1 || !ok2 || end <= start {
			continue
		}
		out = append(out, [2]int{start, end})
	}
	return out
}

// withinWindow 判断 now 是否落在交易时段窗口内的某个区间。
func withinWindow(now time.Time, window string) bool {
	ranges := parseWindow(window)
	if len(ranges) == 0 {
		ranges = parseWindow(DefaultIntradayWindow)
	}
	local := now.In(shanghaiLoc())
	minutes := local.Hour()*60 + local.Minute()
	for _, r := range ranges {
		if minutes >= r[0] && minutes <= r[1] {
			return true
		}
	}
	return false
}

// nextWindowStart 返回下一个交易时段窗口的起点（跳过周末）。
func nextWindowStart(now time.Time, window string) time.Time {
	ranges := parseWindow(window)
	if len(ranges) == 0 {
		ranges = parseWindow(DefaultIntradayWindow)
	}
	local := now.In(shanghaiLoc())
	minutes := local.Hour()*60 + local.Minute()
	for _, r := range ranges {
		if minutes < r[0] {
			return atTimeOn(local, r[0])
		}
	}
	for offset := 1; offset <= 4; offset++ {
		day := local.AddDate(0, 0, offset)
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		return atTimeOn(day, ranges[0][0])
	}
	return time.Time{}
}

// nextWeekdayAt 返回"下一个工作日的 at 时刻"（不含今天）。
func nextWeekdayAt(now time.Time, minutes int) time.Time {
	local := now.In(shanghaiLoc())
	for offset := 1; offset <= 4; offset++ {
		day := local.AddDate(0, 0, offset)
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		return atTimeOn(day, minutes)
	}
	return time.Time{}
}

// scheduleDue 判断某阶段在 now 时刻是否应当触发；第二个返回值是给 UI 的中文原因。
// 规则（Asia/Shanghai，周一至周五；节假日暂用工作日近似）：
//   - 盘前/复盘：at 到点、且当天未跑、且在 90 分钟补跑宽限内；
//   - 盘中：在交易时段窗口内，且距上次运行已满 everyMinutes。
func scheduleDue(now time.Time, p PhaseSchedule, last time.Time) (bool, string) {
	if !p.Enabled {
		return false, "阶段已关闭"
	}
	local := now.In(shanghaiLoc())
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
		return false, "非工作日（节假日可能误触发，当前按工作日近似）"
	}
	switch p.Phase {
	case PhaseIntraday:
		every := p.EveryMinutes
		if every <= 0 {
			every = DefaultIntradayEvery
		}
		window := strings.TrimSpace(p.Window)
		if window == "" {
			window = DefaultIntradayWindow
		}
		if !withinWindow(local, window) {
			return false, "不在交易时段（" + window + "）"
		}
		if !last.IsZero() && local.Sub(last) < time.Duration(every)*time.Minute {
			return false, fmt.Sprintf("距上次运行不足 %d 分钟", every)
		}
		return true, "盘中定时巡检"
	case PhasePremarket, PhasePostmarket:
		minutes, ok := parseClockMinutes(p.At)
		if !ok {
			return false, "未配置触发时间（格式 HH:MM，例如 09:15）"
		}
		at := atTimeOn(local, minutes)
		if local.Before(at) {
			return false, "尚未到触发时间 " + p.At
		}
		if !last.IsZero() && sameDay(local, last) {
			return false, "今天已经跑过"
		}
		if local.After(at.Add(atGrace)) {
			return false, "已超过补跑宽限（" + p.At + " 起 90 分钟），等下一个交易日"
		}
		return true, "到点自动值班"
	}
	return false, "未知阶段"
}

// nextRunTime 计算某阶段的下次运行时刻；算不出来返回零值。
func nextRunTime(now time.Time, p PhaseSchedule, last time.Time) time.Time {
	if !p.Enabled {
		return time.Time{}
	}
	local := now.In(shanghaiLoc())
	switch p.Phase {
	case PhaseIntraday:
		every := p.EveryMinutes
		if every <= 0 {
			every = DefaultIntradayEvery
		}
		window := strings.TrimSpace(p.Window)
		if window == "" {
			window = DefaultIntradayWindow
		}
		if !withinWindow(local, window) {
			return nextWindowStart(local, window)
		}
		if last.IsZero() || !withinWindow(last, window) || local.Sub(last) >= time.Duration(every)*time.Minute {
			return local
		}
		next := last.Add(time.Duration(every) * time.Minute)
		if withinWindow(next, window) {
			return next
		}
		return nextWindowStart(next, window)
	case PhasePremarket, PhasePostmarket:
		minutes, ok := parseClockMinutes(p.At)
		if !ok {
			return time.Time{}
		}
		at := atTimeOn(local, minutes)
		ranToday := !last.IsZero() && sameDay(local, last)
		if !ranToday && !local.Before(at) && !local.After(at.Add(atGrace)) {
			return local
		}
		if !ranToday && local.Before(at) {
			return at
		}
		return nextWeekdayAt(local, minutes)
	}
	return time.Time{}
}

// ---------- 调度器 ----------

// SchedulerOptions 是调度器的依赖集合（插件在 Activate 时注入）。
type SchedulerOptions struct {
	Runtime   *Runtime
	Templates *TemplateStore
	Store     contract.Store
	Clock     func() time.Time
	Logger    *slog.Logger
	Tick      time.Duration
}

// Scheduler 是自动值班调度器：Activate 时启动、Deactivate 时停止。
// 单飞（同一时刻只跑一个自动任务）、当天去重、无模型不触发只记 lastStatus。
type Scheduler struct {
	rt        *Runtime
	templates *TemplateStore
	store     contract.Store
	clock     func() time.Time
	log       *slog.Logger
	tick      time.Duration

	mu     sync.Mutex
	phases map[string]*PhaseSchedule
	busy   bool
	cancel context.CancelFunc
	loaded bool
}

// NewScheduler 构造调度器（不启动，需调用 Start）。
func NewScheduler(opts SchedulerOptions) *Scheduler {
	s := &Scheduler{
		rt:        opts.Runtime,
		templates: opts.Templates,
		store:     opts.Store,
		clock:     opts.Clock,
		log:       opts.Logger,
		tick:      opts.Tick,
	}
	if s.templates == nil && s.rt != nil {
		s.templates = s.rt.Templates()
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.tick <= 0 {
		s.tick = DefaultSchedulerTick
	}
	s.load()
	return s
}

func (s *Scheduler) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
}

func (s *Scheduler) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	byPhase := map[string]*PhaseSchedule{}
	for _, p := range defaultPhases() {
		v := p
		byPhase[p.Phase] = &v
	}
	if s.store != nil {
		if raw, err := s.store.Get(scheduleBucket, scheduleDocKey); err == nil && len(raw) > 0 {
			var doc scheduleDoc
			if err := json.Unmarshal(raw, &doc); err == nil {
				for _, p := range doc.Phases {
					if _, ok := byPhase[p.Phase]; !ok {
						continue
					}
					v := p
					byPhase[p.Phase] = &v
				}
			}
		}
	}
	s.phases = byPhase
	s.refreshNextLocked()
}

// refreshNextLocked 按当前时间重算三个阶段的 nextRunAt（纯派生字段，不单独落盘）。
func (s *Scheduler) refreshNextLocked() {
	now := s.clock()
	for _, phase := range phaseOrder {
		p := s.phases[phase]
		if p == nil {
			continue
		}
		next := nextRunTime(now, *p, parseTime(p.LastRunAt))
		if next.IsZero() {
			p.NextRunAt = ""
			continue
		}
		p.NextRunAt = next.In(shanghaiLoc()).Format(time.RFC3339)
	}
}

// parseTime 解析 RFC3339；空串或格式不对返回零值。
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Phases 返回三个阶段的调度状态（顺序：盘前/盘中/复盘）。
func (s *Scheduler) Phases() []PhaseSchedule {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	s.refreshNextLocked()
	out := make([]PhaseSchedule, 0, len(phaseOrder))
	for _, phase := range phaseOrder {
		if p := s.phases[phase]; p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// phase 取某阶段的状态副本。
func (s *Scheduler) phase(phase string) (PhaseSchedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	p, ok := s.phases[phase]
	if !ok {
		return PhaseSchedule{}, false
	}
	return *p, true
}

// Templates 返回模板列表，并把各自阶段的调度状态合并进去（前端一张卡读全量）。
func (s *Scheduler) Templates() []AgentTemplate {
	if s == nil {
		return nil
	}
	var tpls []AgentTemplate
	if s.templates != nil {
		tpls = s.templates.All()
	}
	s.mu.Lock()
	s.loadLocked()
	s.refreshNextLocked()
	merged := make(map[string]PhaseSchedule, len(s.phases))
	for k, v := range s.phases {
		merged[k] = *v
	}
	s.mu.Unlock()
	for i := range tpls {
		if p, ok := merged[tpls[i].ID]; ok {
			tpls[i].Schedule = p
		}
	}
	return tpls
}

// SaveTemplate 保存提示词（并可选改开关），返回合并了调度状态的模板列表。
func (s *Scheduler) SaveTemplate(id WorkerID, prompt string, enabled *bool) ([]AgentTemplate, error) {
	return s.SaveTemplateWithModel(id, prompt, enabled, nil)
}

// SaveTemplateWithModel 保存提示词/开关/模型（model 为 nil 时保持原值），返回模板列表。
func (s *Scheduler) SaveTemplateWithModel(id WorkerID, prompt string, enabled *bool, model *string) ([]AgentTemplate, error) {
	if s == nil || s.templates == nil {
		return nil, fmt.Errorf("模板存储未就绪，请重启应用后再试")
	}
	if _, err := s.templates.SaveWithModel(id, prompt, enabled, model); err != nil {
		return nil, err
	}
	return s.Templates(), nil
}

// ResetTemplate 恢复默认提示词，返回合并了调度状态的模板列表。
func (s *Scheduler) ResetTemplate(id WorkerID) ([]AgentTemplate, error) {
	if s == nil || s.templates == nil {
		return nil, fmt.Errorf("模板存储未就绪，请重启应用后再试")
	}
	if _, err := s.templates.Reset(id); err != nil {
		return nil, err
	}
	return s.Templates(), nil
}

// SetSchedule 修改某个阶段的调度（enabled/at/everyMinutes 都是可选的）。
func (s *Scheduler) SetSchedule(patch SchedulePatch) ([]PhaseSchedule, error) {
	if s == nil {
		return nil, fmt.Errorf("调度器未就绪，请重启应用后再试")
	}
	phase := strings.TrimSpace(strings.ToLower(patch.Phase))
	switch phase {
	case PhasePremarket, PhaseIntraday, PhasePostmarket:
	default:
		return nil, fmt.Errorf("未知阶段：%s（可用：premarket / intraday / postmarket）", patch.Phase)
	}
	if patch.At != "" && phase == PhaseIntraday {
		return nil, fmt.Errorf("盘中阶段用 everyMinutes 控制频率，不支持 at；要看时段请用 window")
	}
	if patch.At != "" {
		if _, ok := parseClockMinutes(patch.At); !ok {
			return nil, fmt.Errorf("at 需要 HH:MM 格式（例如 09:15），收到 %q", patch.At)
		}
	}
	if patch.Window != "" {
		if phase != PhaseIntraday {
			return nil, fmt.Errorf("只有盘中阶段支持 window（交易时段）；盘前/复盘请用 at")
		}
		normalized, err := normalizeWindow(patch.Window)
		if err != nil {
			return nil, err
		}
		patch.Window = normalized
	}
	if patch.EveryMinutes != nil {
		if phase != PhaseIntraday {
			return nil, fmt.Errorf("只有盘中阶段支持 everyMinutes（盘前/复盘用 at）")
		}
		v := *patch.EveryMinutes
		if v < minEveryMinutes || v > maxEveryMinutes {
			return nil, fmt.Errorf("everyMinutes 需在 %d..%d 之间（盘中默认 %d）", minEveryMinutes, maxEveryMinutes, DefaultIntradayEvery)
		}
	}

	s.mu.Lock()
	s.loadLocked()
	target, ok := s.phases[phase]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("阶段 %s 的调度状态缺失，请重启应用", phase)
	}
	if patch.Enabled != nil {
		target.Enabled = *patch.Enabled
	}
	if patch.At != "" {
		target.At = strings.TrimSpace(patch.At)
	}
	if patch.EveryMinutes != nil {
		target.EveryMinutes = *patch.EveryMinutes
	}
	if patch.Window != "" {
		target.Window = patch.Window
	}
	s.refreshNextLocked()
	doc := s.snapshotLocked()
	s.mu.Unlock()

	if err := s.save(doc); err != nil {
		return nil, err
	}
	s.emit("", "调度已更新")
	return s.Phases(), nil
}

// snapshotLocked 生成落盘用的阶段列表副本。
func (s *Scheduler) snapshotLocked() scheduleDoc {
	phases := make([]PhaseSchedule, 0, len(phaseOrder))
	for _, phase := range phaseOrder {
		if p := s.phases[phase]; p != nil {
			phases = append(phases, *p)
		}
	}
	return scheduleDoc{Phases: phases, UpdatedAt: s.clock().In(shanghaiLoc()).Format(time.RFC3339)}
}

func (s *Scheduler) save(doc scheduleDoc) error {
	if s.store == nil {
		return nil
	}
	buf, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("调度状态序列化失败：%w", err)
	}
	if err := s.store.Put(scheduleBucket, scheduleDocKey, buf); err != nil {
		return fmt.Errorf("保存调度状态失败：%w", err)
	}
	return nil
}

// updatePhase 改某个阶段状态并落盘（落盘失败只记日志，不打断调度）。
func (s *Scheduler) updatePhase(phase string, mutate func(*PhaseSchedule)) {
	s.mu.Lock()
	s.loadLocked()
	p, ok := s.phases[phase]
	if !ok {
		s.mu.Unlock()
		return
	}
	mutate(p)
	s.refreshNextLocked()
	doc := s.snapshotLocked()
	s.mu.Unlock()
	if err := s.save(doc); err != nil {
		s.log.Warn("调度状态落盘失败", "phase", phase, "error", err)
	}
}

// Start 启动 20s tick 的调度协程（插件 Activate 调用）。
func (s *Scheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	interval := s.tick
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				s.TickOnce(runCtx, s.clock())
			}
		}
	}()
}

// Stop 停止调度协程（插件 Deactivate 调用）。
func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// TickOnce 检查一次是否到点；到点就异步跑一个阶段（单飞）。
// 导出便于测试注入时间与上下文。
func (s *Scheduler) TickOnce(ctx context.Context, now time.Time) {
	if s == nil || s.rt == nil {
		return
	}
	// 用户手动运行优先：任何任务在跑就把自动任务顺延到下一个 tick。
	if len(s.rt.Running()) > 0 {
		return
	}
	s.mu.Lock()
	s.loadLocked()
	s.refreshNextLocked()
	if s.busy {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	for _, phase := range phaseOrder {
		p, ok := s.phase(phase)
		if !ok {
			continue
		}
		due, reason := scheduleDue(now, p, parseTime(p.LastRunAt))
		if !due {
			continue
		}
		tpl := s.templateOf(phase)
		if !tpl.Enabled {
			s.noteStatus(phase, "模板已关闭（在值班卡里打开后再跑）")
			return
		}
		// 未配置模型不触发：只记 lastStatus，不刷错误。
		if err := s.rt.HasModel(ctx); err != nil {
			s.noteStatus(phase, "未配置模型")
			return
		}
		s.fire(ctx, phase, tpl, now, reason)
		return
	}
}

// templateOf 取某个值班阶段的模板。
//
// 收敛后 premarket/intraday/postmarket 不再是内置常驻、而是自定义（预设）worker，
// 但**值班调度必须照旧能跑**：这里按 id 精确查找，
// 查不到时用静态默认（标题取预设文案 + 默认 prompt）兜底，绝不让自动值班因为
// "worker 不再内置"而静默失效。
func (s *Scheduler) templateOf(phase string) AgentTemplate {
	id := WorkerID(phase)
	if s.templates != nil {
		if tpl, ok := s.templates.Get(id); ok {
			return tpl
		}
	}
	tpl := AgentTemplate{ID: phase, Title: phase, Prompt: defaultPrompt(id), DefaultPrompt: defaultPrompt(id), Enabled: true}
	if m, ok := presetWorkerMetaOf(id); ok {
		tpl.Title = m.Title
		tpl.Description = m.Description
	}
	return tpl
}

// noteStatus 只更新 lastStatus（且只在变化时落盘/发事件），避免每个 tick 刷屏。
func (s *Scheduler) noteStatus(phase, status string) {
	s.mu.Lock()
	p := s.phases[phase]
	changed := p != nil && p.LastStatus != status
	s.mu.Unlock()
	if !changed {
		return
	}
	s.updatePhase(phase, func(p *PhaseSchedule) { p.LastStatus = status })
	s.emit(phase, status)
}

// fire 触发一次自动值班：先落 lastRunAt 做当天去重，跑完再写结果。
func (s *Scheduler) fire(parent context.Context, phase string, tpl AgentTemplate, now time.Time, reason string) {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return
	}
	s.busy = true
	s.mu.Unlock()

	s.updatePhase(phase, func(p *PhaseSchedule) {
		p.LastRunAt = now.In(shanghaiLoc()).Format(time.RFC3339)
		p.LastStatus = "运行中"
	})
	s.emit(phase, reason+"："+tpl.Title+" 已开始")

	go func() {
		defer func() {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}()
		report, err := s.rt.RunScheduledWithModel(parent, WorkerID(phase), tpl.Prompt, tpl.Model)
		status := "完成"
		switch {
		case err != nil:
			status = "失败：" + err.Error()
		case report.Error != "":
			status = "失败：" + report.Error
		}
		s.updatePhase(phase, func(p *PhaseSchedule) {
			p.LastStatus = status
			p.LastSessionID = report.SessionID
		})
		s.emit(phase, status)
	}()
}

// emit 广播 agent.schedule 事件（前端据此刷新倒计时与上次结果）。
func (s *Scheduler) emit(phase, reason string) {
	if s.rt == nil {
		return
	}
	s.mu.Lock()
	s.loadLocked()
	s.refreshNextLocked()
	next := ""
	if p := s.phases[phase]; p != nil {
		next = p.NextRunAt
	}
	busy := s.busy
	s.mu.Unlock()
	if phase == "" {
		// 全局变更（setSchedule）：把三个阶段一起给前端，便于一次刷新。
		payload := map[string]any{"running": busy, "reason": reason, "phases": s.Phases()}
		s.rt.emit("agent.schedule", payload)
		return
	}
	s.rt.emit("agent.schedule", map[string]any{
		"phase":     phase,
		"nextRunAt": next,
		"running":   busy,
		"reason":    reason,
	})
}
