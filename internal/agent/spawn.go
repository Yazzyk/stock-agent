package agent

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------- 子智能体（编队） ----------

const (
	// maxSpawnChildren 是同一个父会话最多同时跑的子智能体数。
	maxSpawnChildren = 2
	// maxSpawnReportRunes 是回给父会话的报告长度上限（超出截断并标注）。
	maxSpawnReportRunes = 4000
	// treeScanLimit 是编队树扫描的最近会话条数。
	treeScanLimit = 100
)

// ctxKey 是 runCtx 上的内部键。
type ctxKey string

const ctxSessionKey ctxKey = "agent.sessionId"

// WithSession 把当前会话 id 放进 ctx：ReAct → Gateway → 工具 会原样透传，
// 工具（如 agent.spawn）据此知道自己属于哪个会话。
func WithSession(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxSessionKey, sessionID)
}

// SessionFrom 从 ctx 取会话 id。
func SessionFrom(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	v, ok := ctx.Value(ctxSessionKey).(string)
	if !ok || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

// SpawnRequest 是一次派生的入参。
type SpawnRequest struct {
	Task   string
	Worker string
	Label  string
	// Model 是显式指定的子会话模型；空表示继承父会话模型。
	Model string
}

// SpawnResult 是一次派生的结果。
type SpawnResult struct {
	SessionID string `json:"sessionId"`
	ParentID  string `json:"parentId"`
	Label     string `json:"label"`
	Worker    string `json:"worker"`
	Status    string `json:"status"`
	Report    string `json:"report,omitempty"`
	Tokens    int    `json:"tokens"`
	Steps     int    `json:"steps"`
	ElapsedMs int64  `json:"elapsedMs"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	Model     string `json:"model,omitempty"`
	// Async 表示这是后台派发的受理结果（还没跑完，Report 为空）。
	Async bool `json:"async,omitempty"`
}

// spawnPlan 是一次派生的准备结果（全部校验通过后的入参）。
type spawnPlan struct {
	ParentID   string
	Spec       WorkerSpec
	Label      string
	ChildModel string
	Task       string
}

// prepareSpawn 做派生前的全部校验：
// 主会话身份、父会话存在、task/worker 合法性、标签兜底与模型继承。
// 并发上限不在这里判：同步派生看会话表，异步派生看通道里的在跑数量。
func (r *Runtime) prepareSpawn(ctx context.Context, req SpawnRequest) (spawnPlan, error) {
	parentID, ok := SessionFrom(ctx)
	if !ok {
		return spawnPlan{}, fmt.Errorf("agent.spawn 只能在主会话里调用：当前上下文没有会话 id")
	}
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return spawnPlan{}, fmt.Errorf("agent.spawn 需要 task：写清要子智能体做什么（一句到一段话）")
	}
	parent, err := r.sessions.Get(parentID)
	if err != nil {
		return spawnPlan{}, fmt.Errorf("找不到父会话 %s：%v", parentID, err)
	}
	// 旧数据兼容：没有 Mode 但有 ParentID 的会话也算子智能体。
	if parent.Session.IsSubagent() {
		return spawnPlan{}, fmt.Errorf("子智能体不能再次派生（编队最多一层：只有主会话可以派生）")
	}
	// 子智能体必须显式指定 worker：不再默默回落到 chat。
	// 编译期就把"派给谁"说清楚，避免父会话把子任务跑成通用问答还以为派了专人。
	workerID := strings.TrimSpace(req.Worker)
	if workerID == "" {
		return spawnPlan{}, fmt.Errorf("agent.spawn 需要 worker：子智能体不再默认使用 chat。"+
			"请显式指定要派给谁（可用：%s）；没有合适的就先用 worker.create 创建一个，再派生", r.workerIDList())
	}
	spec, ok := r.strictLookupWorker(workerID)
	if !ok {
		return spawnPlan{}, fmt.Errorf("未知 worker：%s（可用：%s）", req.Worker, r.workerIDList())
	}
	if spec.ID == WorkerRouter {
		return spawnPlan{}, fmt.Errorf("不能把 RouterAgent 作为子智能体派生：RouterAgent 负责拆解与汇总，子任务请派给具体 worker（可用：%s），或先 worker.create 创建新的专用 worker", r.workerIDList())
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = truncateRunes(task, 20)
	}
	// 子会话默认继承父会话模型；显式 model 优先。
	childModel := strings.TrimSpace(req.Model)
	if childModel == "" {
		childModel = strings.TrimSpace(parent.Session.Model)
	}
	return spawnPlan{ParentID: parentID, Spec: spec, Label: label, ChildModel: childModel, Task: task}, nil
}

// Spawn 同步派生一个子智能体：用完整 ReAct 跑一遍子任务，返回报告前 4000 字。
//
// 边界（契约 §3.6）：
//   - 只有主会话能派生；子会话再派生直接报"子智能体不能再次派生"；
//   - 同一父会话最多同时 2 个后代；
//   - 父会话取消时子会话级联取消（子 ctx 派生自父 ctx）；
//   - 子任务失败不抛到父流程，转成中文错误放进结果里。
func (r *Runtime) Spawn(ctx context.Context, req SpawnRequest) (SpawnResult, error) {
	plan, err := r.prepareSpawn(ctx, req)
	if err != nil {
		return SpawnResult{}, err
	}
	if n := r.runningChildren(plan.ParentID); n >= maxSpawnChildren {
		return SpawnResult{}, fmt.Errorf("最多同时 %d 个子智能体在跑（当前 %d 个），请等其中一个结束再派生", maxSpawnChildren, n)
	}
	// 子 ctx 派生自父 ctx：父会话取消/超时，子会话一并取消。
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	return r.runChild(childCtx, plan, r.opts.IDGen()), nil
}

// SpawnAsync 后台派生一个子智能体（双工模式）：
// 立刻返回「已派发」，子任务在 goroutine 里跑完整 ReAct；
// 终态结果进入父会话的 childHub，等父会话的 ReAct 走到步骤边界时再注入。
//
// 这样 RouterAgent 不必阻塞在一个工具调用里：它可以继续向用户播报进度、
// 继续派发其它子任务，等结果回来再汇总。
func (r *Runtime) SpawnAsync(ctx context.Context, req SpawnRequest) (SpawnResult, error) {
	plan, err := r.prepareSpawn(ctx, req)
	if err != nil {
		return SpawnResult{}, err
	}
	hub := r.childHubOf(plan.ParentID)
	if hub == nil {
		// 不在一次运行内（例如前端/工具直接调用）：没有可回收的父 ReAct，
		// 后台结果没人接，退化成同步派生，宁可变慢也不丢报告。
		childCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		return r.runChild(childCtx, plan, r.opts.IDGen()), nil
	}
	childID := r.opts.IDGen()
	childCtx, cancel := context.WithCancel(ctx)
	if err := hub.begin(childID, plan.Label, cancel); err != nil {
		cancel()
		return SpawnResult{}, err
	}
	go func() {
		defer cancel()
		res := r.runChild(childCtx, plan, childID)
		hub.finish(childResultFrom(res))
		r.emitProgress(map[string]any{
			"sessionId":      plan.ParentID,
			"childSessionId": res.SessionID,
			"label":          res.Label,
			"worker":         res.Worker,
			"status":         res.Status,
			"phase":          "finished",
			"tokens":         res.Tokens,
			"elapsedMs":      res.ElapsedMs,
		})
	}()
	return SpawnResult{
		SessionID: childID,
		ParentID:  plan.ParentID,
		Label:     plan.Label,
		Worker:    string(plan.Spec.ID),
		Status:    StatusRunning,
		Model:     plan.ChildModel,
		Async:     true,
	}, nil
}

// runChild 是同步与异步派生的共用执行体：发 started/finished 编队事件，然后跑一次完整 ReAct。
func (r *Runtime) runChild(ctx context.Context, plan spawnPlan, childID string) SpawnResult {
	started := r.opts.Clock()
	r.emitChildEvent(childEventPayload{
		ParentSessionID: plan.ParentID,
		ChildSessionID:  childID,
		Label:           plan.Label,
		Worker:          string(plan.Spec.ID),
		Status:          StatusRunning,
		Phase:           "started",
		Model:           plan.ChildModel,
		StartedAt:       r.timestamp(started),
		At:              r.timestamp(started),
	})
	r.emitProgress(map[string]any{
		"sessionId":      plan.ParentID,
		"childSessionId": childID,
		"label":          plan.Label,
		"worker":         string(plan.Spec.ID),
		"status":         StatusRunning,
		"phase":          "started",
	})

	report, runErr := r.executeWith(ctx, execRequest{
		Spec: plan.Spec,
		Decision: RouteDecision{
			Worker: plan.Spec.ID,
			Fast:   true,
			Phase:  TradingPhase(r.opts.Clock()),
			Note:   "子智能体",
		},
		Input:     plan.Task,
		SessionID: childID,
		Model:     plan.ChildModel,
		Extras:    SessionMeta{ParentID: plan.ParentID, Mode: ModeSubagent, Label: plan.Label},
	})

	res := SpawnResult{
		SessionID: firstNonEmpty(report.SessionID, childID),
		ParentID:  plan.ParentID,
		Label:     plan.Label,
		Worker:    string(plan.Spec.ID),
		Status:    StatusOK,
		Tokens:    report.Usage.TotalTokens,
		Steps:     report.Steps,
		ElapsedMs: r.opts.Clock().Sub(started).Milliseconds(),
		Model:     report.Model,
	}
	switch {
	case runErr != nil:
		res.Status = StatusFailed
		res.Error = runErr.Error()
	case report.Error != "":
		res.Status = StatusFailed
		res.Error = chineseRunError(report.Error)
	}
	res.Report, res.Truncated = truncateReport(report.Report)
	if res.ElapsedMs < 0 {
		res.ElapsedMs = 0
	}

	finishedAt := r.opts.Clock()
	r.emitChildEvent(childEventPayload{
		ParentSessionID: plan.ParentID,
		ChildSessionID:  res.SessionID,
		Label:           plan.Label,
		Worker:          res.Worker,
		Status:          res.Status,
		Phase:           "finished",
		Model:           res.Model,
		Tokens:          res.Tokens,
		ElapsedMs:       res.ElapsedMs,
		Error:           res.Error,
		StartedAt:       r.timestamp(started),
		EndedAt:         r.timestamp(finishedAt),
		At:              r.timestamp(finishedAt),
	})
	return res
}

// childResultFrom 把对外结果转成通道内部的终态结构。
func childResultFrom(res SpawnResult) childResult {
	return childResult{
		SessionID: res.SessionID,
		Label:     res.Label,
		Worker:    res.Worker,
		Status:    res.Status,
		Report:    res.Report,
		Error:     res.Error,
		Tokens:    res.Tokens,
		Steps:     res.Steps,
		ElapsedMs: res.ElapsedMs,
		Truncated: res.Truncated,
	}
}

// childEventPayload 是 agent.child 事件的载荷（权威重放语义的来源）。
//
// 前端把「tree 方法的权威快照」与「agent.child 增量事件」合并时，必须能判断谁更新：
// 所以每条事件都带 at（事件发生时刻，RFC3339，毫秒级）与 startedAt/endedAt，
// 前端按 at 比较"事件是否比手里的节点状态新"，旧事件直接丢弃。
type childEventPayload struct {
	ParentSessionID string `json:"parentSessionId"`
	ChildSessionID  string `json:"childSessionId"`
	Label           string `json:"label"`
	Worker          string `json:"worker"`
	Status          string `json:"status"`
	Phase           string `json:"phase"` // started / finished
	Model           string `json:"model,omitempty"`
	Tokens          int    `json:"tokens"`
	ElapsedMs       int64  `json:"elapsedMs"`
	Error           string `json:"error,omitempty"`
	// At 是这次状态变化的时刻（RFC3339，带毫秒）。前端用它判断新旧，必填。
	At string `json:"at"`
	// AtMs 是 At 的毫秒时间戳（便于前端排序比较，不用解时间串）。
	AtMs int64 `json:"atMs"`
	// StartedAt / EndedAt 是子会话自身的起止时刻（重放会话时用来恢复耗时）。
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
}

// emitChildEvent 发出 agent.child 事件（自动补齐时间戳字段）。
func (r *Runtime) emitChildEvent(p childEventPayload) {
	if p.At == "" {
		p.At = r.timestamp(r.opts.Clock())
	}
	if ts, err := time.Parse(time.RFC3339Nano, p.At); err == nil {
		p.AtMs = ts.UnixMilli()
	}
	if p.StartedAt == "" {
		p.StartedAt = p.At
	}
	if p.EndedAt != "" {
		if ts, err := time.Parse(time.RFC3339Nano, p.EndedAt); err == nil && p.ElapsedMs == 0 {
			if st, err2 := time.Parse(time.RFC3339Nano, p.StartedAt); err2 == nil {
				p.ElapsedMs = ts.Sub(st).Milliseconds()
			}
		}
	}
	r.emit("agent.child", p)
}

// timestamp 是统一的 RFC3339 时间戳格式（带毫秒，上海时区）。
// 前端比较新旧依赖它的字典序单调性，所有事件时间必须走这里。
func (r *Runtime) timestamp(t time.Time) string {
	return t.In(shanghaiLoc()).Format("2006-01-02T15:04:05.000Z07:00")
}

// ToolText 把派生结果整理成给模型看的工具结果：
// 报告取前 4000 字，截断与失败都会明确标注。
func (s SpawnResult) ToolText() string {
	if strings.TrimSpace(s.Report) == "" {
		msg := "子智能体「" + s.Label + "」没有产出报告"
		if s.Error != "" {
			msg += "：" + s.Error
		}
		return msg + "（会话 " + s.SessionID + "，步数 " + strconv.Itoa(s.Steps) + "）"
	}
	var b strings.Builder
	b.WriteString("【子智能体完成】" + s.Label)
	b.WriteString("（worker=" + s.Worker + "，会话 " + s.SessionID)
	b.WriteString("，步数 " + strconv.Itoa(s.Steps) + "，token " + strconv.Itoa(s.Tokens))
	b.WriteString("，耗时 " + strconv.FormatInt(s.ElapsedMs, 10) + "ms）")
	b.WriteString(nl + nl)
	b.WriteString(s.Report)
	if s.Error != "" {
		b.WriteString(nl + nl + "【子任务有错误】" + s.Error)
	}
	return b.String()
}

// runningChildren 统计某父会话下正在跑的子智能体会话数。
func (r *Runtime) runningChildren(parentID string) int {
	metas, err := r.sessions.List(treeScanLimit)
	if err != nil {
		return 0
	}
	n := 0
	for _, m := range metas {
		if m.ParentID == parentID && m.Status == StatusRunning {
			n++
		}
	}
	return n
}

// AgentNode 是编队树里的一个会话节点。
//
// 字段契约（前端按此对齐，全部稳定）：
//   - sessionId：会话 id，节点唯一键；
//   - parentId：父会话 id；根节点为空串（**根的判据是 parentId 为空，不是 mode==="main"**：
//     旧会话可能没有 mode，但一定有 parentId 语义）；
//   - depth：根为 0，逐层 +1（前端按 depth 缩进，不要自己数 parentId）；
//   - childCount：直接子节点个数（前端画"还有 N 个"用，不要自己过滤求长度）；
//   - mode：main / subagent（旧数据回落成 main）；
//   - label：展示名（label > title > worker，永不为空）；
//   - role：展示用角色名（= label，给"role"命名的前端留的别名，值恒等于 label）；
//   - worker：worker id（router / premarket / 自定义 id）；
//   - status：running / ok / failed；
//   - startedAt / endedAt：RFC3339（endedAt 为空表示还在跑或没记录）；
//   - elapsedMs：耗时毫秒（未结束则算到现在）。
type AgentNode struct {
	SessionID  string `json:"sessionId"`
	ParentID   string `json:"parentId"`
	Depth      int    `json:"depth"`
	ChildCount int    `json:"childCount"`
	Label      string `json:"label"`
	Role       string `json:"role"`
	Mode       string `json:"mode"`
	Worker     string `json:"worker"`
	Status     string `json:"status"`
	Tokens     int    `json:"tokens"`
	Steps      int    `json:"steps"`
	StartedAt  string `json:"startedAt"`
	EndedAt    string `json:"endedAt,omitempty"`
	ElapsedMs  int64  `json:"elapsedMs"`
}

// AgentTree 是一个编队的完整视图：根节点 + 按 (depth, startedAt) 稳定排序的全部节点。
type AgentTree struct {
	// Root 是 parentId 为空的祖先节点（一定有值，除非树为空）。
	Root AgentNode `json:"root"`
	// Nodes 是从根开始的全部节点，顺序：(depth 升序, startedAt 升序, sessionId 升序)。
	// 前端直接按顺序渲染即可，不要自己再排序。
	Nodes []AgentNode `json:"nodes"`
	// RootSessionID 是 Root.SessionID 的显式别名（避免前端把 root 当成可选对象）。
	RootSessionID string `json:"rootSessionId"`
	// SessionID 是本次查询的会话 id（前端用来确认"这棵树是不是我要的"）。
	SessionID string `json:"sessionId,omitempty"`
	// Total 是节点总数（= len(Nodes)，给"共 N 个"的标题用）。
	Total int `json:"total"`
	// ActiveCount 是仍在 running 的节点数。
	ActiveCount int `json:"activeCount"`
	// UpdatedAt 是这份树的生成时刻（RFC3339，前端可据此与事件合并判新旧）。
	UpdatedAt string `json:"updatedAt"`
}

// agentLabelOf 取节点展示名：label > title > worker，永不为空。
func agentLabelOf(m SessionMeta) string {
	if label := strings.TrimSpace(m.Label); label != "" {
		return label
	}
	if title := strings.TrimSpace(m.Title); title != "" {
		return title
	}
	if worker := strings.TrimSpace(m.Worker); worker != "" {
		return worker
	}
	return m.ID
}

func agentNodeOf(m SessionMeta, now time.Time) AgentNode {
	label := agentLabelOf(m)
	ended := now
	node := AgentNode{
		SessionID: m.ID,
		ParentID:  m.ParentID,
		Label:     label,
		Role:      label,
		Mode:      m.ModeOrDefault(),
		Worker:    m.Worker,
		Status:    m.Status,
		Tokens:    m.Tokens,
		Steps:     m.Steps,
		StartedAt: m.StartedAt.Format(time.RFC3339),
	}
	if m.EndedAt != nil {
		ended = *m.EndedAt
		node.EndedAt = ended.Format(time.RFC3339)
	}
	node.ElapsedMs = ended.Sub(m.StartedAt).Milliseconds()
	if node.ElapsedMs < 0 {
		node.ElapsedMs = 0
	}
	return node
}

// Tree 返回某个会话所在编队的树：根（parentId 为空的那个祖先）+ 全部后代。
//
// 语义约定（前端按此对齐）：
//   - sessionId 为空时**不报错**，回退成"最近一次主会话的树"：前端首次进面板时
//     还不知道选哪个会话，直接调 tree 就该拿到可渲染的结果；
//   - 只扫最近 treeScanLimit 个会话（性能上限），扫不到时给可操作错误；
//   - 根节点严格取 parentId 为空的祖先；旧会话没有 parentId，各自成根；
//   - 节点按 (depth, startedAt, sessionId) 稳定排序，前端直接按顺序渲染。
func (r *Runtime) Tree(sessionID string) (AgentTree, error) {
	metas, err := r.sessions.List(treeScanLimit)
	if err != nil {
		return AgentTree{}, err
	}
	byID := make(map[string]SessionMeta, len(metas))
	childIDs := make(map[string][]string, len(metas))
	for _, m := range metas {
		byID[m.ID] = m
	}
	for _, m := range metas {
		if strings.TrimSpace(m.ParentID) == "" {
			continue
		}
		childIDs[m.ParentID] = append(childIDs[m.ParentID], m.ID)
	}

	id := strings.TrimSpace(sessionID)
	if id == "" {
		// 回退：最近一次主会话（没有 parentId 的会话里 StartedAt 最新的那个）。
		// List 已按 StartedAt 倒序，取第一个即可。
		for _, m := range metas {
			if strings.TrimSpace(m.ParentID) == "" {
				id = m.ID
				break
			}
		}
		if id == "" {
			return AgentTree{}, fmt.Errorf("还没有任何会话可以展示编队：先发一条消息再打开编队面板")
		}
	}

	target, ok := byID[id]
	if !ok {
		return AgentTree{}, fmt.Errorf("会话不存在：%s（只扫最近 %d 个会话，更早的会话不会出现在编队里）", id, treeScanLimit)
	}
	targetID := target.ID

	// 祖先链：从当前会话往上走到根（根 = parentId 为空）。
	root := target
	seen := map[string]bool{target.ID: true}
	for strings.TrimSpace(root.ParentID) != "" {
		parent, ok := byID[root.ParentID]
		if !ok || seen[parent.ID] {
			// 父会话不在扫描窗口内（或数据成环）：当前节点就是能看到的最高祖先。
			break
		}
		seen[parent.ID] = true
		root = parent
	}

	// 从根往下 BFS 收全部后代，同时记 depth。
	type queued struct {
		id    string
		depth int
	}
	order := make([]SessionMeta, 0, len(metas))
	depths := make(map[string]int, len(metas))
	order = append(order, root)
	depths[root.ID] = 0
	queue := []queued{{id: root.ID, depth: 0}}
	visited := map[string]bool{root.ID: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, cid := range childIDs[cur.id] {
			if visited[cid] {
				continue
			}
			visited[cid] = true
			child, ok := byID[cid]
			if !ok {
				continue
			}
			order = append(order, child)
			depths[cid] = cur.depth + 1
			queue = append(queue, queued{id: cid, depth: cur.depth + 1})
		}
	}

	now := r.opts.Clock()
	nodes := make([]AgentNode, 0, len(order))
	byNodeID := make(map[string]int, len(order))
	for _, m := range order {
		node := agentNodeOf(m, now)
		node.Depth = depths[m.ID]
		node.ChildCount = len(childIDs[m.ID])
		byNodeID[m.ID] = len(nodes)
		nodes = append(nodes, node)
	}
	// 稳定排序：(depth 升序, startedAt 升序, sessionId 升序)。
	// 用 sort.SliceStable 保证同一层里并发派生的节点顺序稳定可复现。
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Depth != nodes[j].Depth {
			return nodes[i].Depth < nodes[j].Depth
		}
		if nodes[i].StartedAt != nodes[j].StartedAt {
			return nodes[i].StartedAt < nodes[j].StartedAt
		}
		return nodes[i].SessionID < nodes[j].SessionID
	})

	active := 0
	for _, n := range nodes {
		if n.Status == StatusRunning {
			active++
		}
	}
	tree := AgentTree{
		Nodes:         nodes,
		RootSessionID: root.ID,
		SessionID:     targetID,
		Total:         len(nodes),
		ActiveCount:   active,
		UpdatedAt:     now.In(shanghaiLoc()).Format(time.RFC3339),
	}
	// 根节点 depth 恒为 0，排序后一定在第一位；这里仍按 id 精确定位，
	// 避免将来改变排序规则时 Root 和 Nodes[0] 悄悄不一致。
	if idx, ok := byNodeID[root.ID]; ok {
		tree.Root = nodes[idx]
	}
	return tree, nil
}

// truncateReport 截断报告并标注（第二个返回值表示是否截断）。
func truncateReport(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) <= maxSpawnReportRunes {
		return trimmed, false
	}
	return string(runes[:maxSpawnReportRunes]) + nl +
		"（报告共 " + strconv.Itoa(len(runes)) + " 字，以上为前 " + strconv.Itoa(maxSpawnReportRunes) + " 字，已截断）", true
}

// chineseRunError 把常见的英文运行期错误换成可读中文。
func chineseRunError(s string) string {
	switch {
	case strings.Contains(s, "context canceled"):
		return "子任务被取消（父会话已停止或超时）"
	case strings.Contains(s, "context deadline exceeded"):
		return "子任务超时（父会话已停止或超时）"
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
