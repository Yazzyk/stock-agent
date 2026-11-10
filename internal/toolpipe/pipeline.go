// Package toolpipe 实现工具执行流水线：把「策略」从「工具实现」里赶出去。
//
// 一条调用会经过五段，每一段的职责是排他的：
//
//① pre    可 allow / deny / ask / rewrite      ← 权限、hook、沙箱策略
//② guard  单调拒绝，下游无法翻案                ← 安全底线
//③ exec   环绕执行：超时、重试、指标             ← 横切关注点
//④ body   工具本体，只返回规范结果
//⑤ post   accept / block / replace / 追加上下文  ← 结果改写
//
// 关键设计：deny 返回的是「结果」而不是 error。
// 因为模型必须看到「我被拒绝了」，然后改道——turn 不能因此死掉。
package toolpipe

import (
"context"
"fmt"
"strings"
)

// Call 是一次调用的不可变身份。约定：进入流水线后不再修改。
type Call struct {
CallID string
Name   string
Args   string // 原始 JSON 字符串
Agent  string // 发起者，便于按 agent 限权
}

// Result 是工具返回的规范结果。
type Result struct {
Content string
IsError bool
// Meta 是给 UI/日志用的结构化附加信息，不进入模型上下文。
Meta map[string]any
}

// Decision 是 pre 阶段的裁决。
type Decision struct {
Kind    Kind
Reason  string
Rewrite *Call
}

// Kind 是裁决类型。
type Kind int

const (
Allow Kind = iota
Deny
Ask
)

// Allow 放行。
func AllowD() Decision { return Decision{Kind: Allow} }

// DenyD 直接拒绝。
func DenyD(reason string) Decision { return Decision{Kind: Deny, Reason: reason} }

// AskD 转入人工审批。
func AskD(reason string) Decision { return Decision{Kind: Ask, Reason: reason} }

// Pre 是环绕式前置策略：调 next 表示委托给下游，不调表示短路。
type Pre func(ctx context.Context, call Call, next func(context.Context) (Decision, error)) (Decision, error)

// Guard 是单调守卫：返回非空理由即拒绝，且后续任何阶段都无法改回允许。
type Guard func(ctx context.Context, call Call) (denyReason string)

// Exec 是环绕执行（超时 / 重试 / 指标 / 沙箱在这里做）。
type Exec func(ctx context.Context, call Call, next func(context.Context) (Result, error)) (Result, error)

// Post 在工具返回后运行：可接受、阻止、替换、或追加模型可见上下文。
type Post func(ctx context.Context, call Call, res Result, next func(context.Context, Result) (Result, error)) (Result, error)

// Approver 是一次性人工审批通道。
// 硬规则：通道缺失或无法回答 = 拒绝，绝不是「默认允许」。
type Approver func(ctx context.Context, call Call, reason string) (approved bool, err error)

// Body 是工具本体。
type Body func(ctx context.Context, call Call) (Result, error)

// Pipeline 持有注册顺序——顺序就是语义。
type Pipeline struct {
pres     []Pre
guards   []Guard
execs    []Exec
posts    []Post
approver Approver
body     Body
}

// New 用工具本体构造流水线。
func New(body Body) *Pipeline { return &Pipeline{body: body} }

// UsePre 追加前置策略。
func (p *Pipeline) UsePre(h Pre) *Pipeline { p.pres = append(p.pres, h); return p }

// UseGuard 追加单调守卫。
func (p *Pipeline) UseGuard(g Guard) *Pipeline { p.guards = append(p.guards, g); return p }

// UseExec 追加执行包装器。
func (p *Pipeline) UseExec(e Exec) *Pipeline { p.execs = append(p.execs, e); return p }

// UsePost 追加结果改写器。
func (p *Pipeline) UsePost(h Post) *Pipeline { p.posts = append(p.posts, h); return p }

// UseApprover 装入审批通道。
func (p *Pipeline) UseApprover(a Approver) *Pipeline { p.approver = a; return p }

// Run 执行一次调用，永不返回 error 给上层（工具错误会被收敛成 isError 结果）。
func (p *Pipeline) Run(ctx context.Context, call Call) Result {
decision, err := p.runPre(ctx, 0, call)
if err != nil {
return Result{Content: "error: " + err.Error(), IsError: true}
}
if decision.Rewrite != nil {
call = *decision.Rewrite
}

switch decision.Kind {
case Deny:
return Result{Content: "denied: " + decision.Reason, IsError: true}
case Ask:
if p.approver == nil {
return Result{Content: "denied: 审批通道不可用", IsError: true}
}
ok, aerr := p.approver(ctx, call, decision.Reason)
if aerr != nil || !ok {
return Result{Content: "denied: 审批被拒绝或不可用", IsError: true}
}
}

// 单调守卫：pre 的任何 rewrite 都不能绕过它
for _, g := range p.guards {
if reason := g(ctx, call); reason != "" {
return Result{Content: "denied: " + reason, IsError: true}
}
}

res, err := p.runExec(ctx, 0, call)
if err != nil {
res = Result{Content: "error: " + err.Error(), IsError: true}
}

out, perr := p.runPost(ctx, 0, call, res)
if perr != nil {
return Result{Content: "error: " + perr.Error(), IsError: true}
}
return out
}

func (p *Pipeline) runPre(ctx context.Context, i int, call Call) (Decision, error) {
if i >= len(p.pres) {
return AllowD(), nil
}
return p.pres[i](ctx, call, func(c context.Context) (Decision, error) {
return p.runPre(c, i+1, call)
})
}

func (p *Pipeline) runExec(ctx context.Context, i int, call Call) (Result, error) {
if i >= len(p.execs) {
return p.body(ctx, call)
}
return p.execs[i](ctx, call, func(c context.Context) (Result, error) {
return p.runExec(c, i+1, call)
})
}

func (p *Pipeline) runPost(ctx context.Context, i int, call Call, res Result) (Result, error) {
if i >= len(p.posts) {
return res, nil
}
return p.posts[i](ctx, call, res, func(c context.Context, r Result) (Result, error) {
return p.runPost(c, i+1, call, r)
})
}

// ---------------------------------------------------------------------------
// 现成的几个策略，开箱即用
// ---------------------------------------------------------------------------

// RequireApproval 让匹配的调用走人工审批。
func RequireApproval(match func(Call) (bool, string)) Pre {
return func(ctx context.Context, call Call, next func(context.Context) (Decision, error)) (Decision, error) {
if ok, reason := match(call); ok {
return AskD(reason), nil
}
return next(ctx)
}
}

// DenyPattern 拒绝参数里包含指定子串的调用（演示用，真实场景应解析 JSON 后判定）。
func DenyPattern(substr, reason string) Guard {
return func(ctx context.Context, call Call) string {
if strings.Contains(call.Args, substr) {
return reason
}
return ""
}
}

// WithTimeout 给执行阶段加超时包装。
func WithTimeout(millis int) Exec {
return func(ctx context.Context, call Call, next func(context.Context) (Result, error)) (Result, error) {
c, cancel := context.WithTimeout(ctx, millisDuration(millis))
defer cancel()

type out struct {
res Result
err error
}
done := make(chan out, 1)
go func() {
r, e := next(c)
done <- out{r, e}
}()

select {
case <-c.Done():
return Result{}, fmt.Errorf("超时（>%dms）", millis)
case o := <-done:
return o.res, o.err
}
}
}

// AppendNotice 在结果末尾追加一行说明（演示 post 阶段改写模型可见内容）。
func AppendNotice(fn func(Call, Result) string) Post {
return func(ctx context.Context, call Call, res Result, next func(context.Context, Result) (Result, error)) (Result, error) {
out, err := next(ctx, res)
if err != nil {
return out, err
}
if notice := fn(call, out); notice != "" {
out.Content += "\n" + notice
}
return out, nil
}
}
