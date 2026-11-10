// Package tools 提供几个示例工具，并演示两种写工具的方式：
//
//utils.InferTool  —— 用 struct tag 自动生成 JSON Schema（推荐）
//utils.NewTool    —— 手写 schema.ToolInfo（需要精细控制时）
//
// 以及一个关键桥接：把 Eino 工具包进 toolpipe 流水线。
package tools

import (
"context"
"fmt"
"os"
"path/filepath"
"strconv"
"strings"
"time"

"github.com/cloudwego/eino/components/tool"
"github.com/cloudwego/eino/components/tool/utils"
"github.com/cloudwego/eino/schema"

"github.com/yazzyk/stock-agent/internal/toolpipe"
)

// EchoArgs 是 echo 工具的入参。
// ⚠️ tag 名是 jsonschema，不是 json；json 只决定字段名。
type EchoArgs struct {
Text string `json:"text" jsonschema:"description=要回显的文本,required"`
}

// NowArgs 是 now 工具的入参。
type NowArgs struct {
// Timezone 例如 Asia/Shanghai，空表示本地时区。
Timezone string `json:"timezone" jsonschema:"description=IANA 时区名，例如 Asia/Shanghai"`
}

// CalcArgs 是 calc 工具的入参。
type CalcArgs struct {
A  float64 `json:"a" jsonschema:"description=左操作数,required"`
Op string  `json:"op" jsonschema:"description=运算符，可选 + - * /,required"`
B  float64 `json:"b" jsonschema:"description=右操作数,required"`
}

// ReadFileArgs 是 read_file 工具的入参。
type ReadFileArgs struct {
Path string `json:"path" jsonschema:"description=相对工作区根目录的路径,required"`
}

// Echo 回显文本。
func Echo() tool.InvokableTool {
t, err := utils.InferTool("echo", "原样回显一段文本，用于验证工具链路",
func(_ context.Context, in *EchoArgs) (string, error) {
return in.Text, nil
})
if err != nil {
panic(err)
}
return t
}

// Now 返回当前时间。
func Now() tool.InvokableTool {
t, err := utils.InferTool("now", "获取当前时间",
func(_ context.Context, in *NowArgs) (string, error) {
loc := time.Local
if in.Timezone != "" {
l, err := time.LoadLocation(in.Timezone)
if err != nil {
return "", fmt.Errorf("未知时区 %q: %w", in.Timezone, err)
}
loc = l
}
return time.Now().In(loc).Format("2006-01-02 15:04:05 MST"), nil
})
if err != nil {
panic(err)
}
return t
}

// Calc 做四则运算。
// 注意：工具本体只返回「规范结果」，不做任何策略判断。
func Calc() tool.InvokableTool {
info := &schema.ToolInfo{
Name: "calc",
Desc: "四则运算计算器",
ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
"a":  {Type: schema.Number, Desc: "左操作数", Required: true},
"op": {Type: schema.String, Desc: "运算符：+ - * /", Required: true, Enum: []string{"+", "-", "*", "/"}},
"b":  {Type: schema.Number, Desc: "右操作数", Required: true},
}),
}
return utils.NewTool(info, func(_ context.Context, in *CalcArgs) (string, error) {
switch in.Op {
case "+":
return trimFloat(in.A + in.B), nil
case "-":
return trimFloat(in.A - in.B), nil
case "*":
return trimFloat(in.A * in.B), nil
case "/":
if in.B == 0 {
return "", fmt.Errorf("除数为 0")
}
return trimFloat(in.A / in.B), nil
default:
return "", fmt.Errorf("不支持的运算符 %q", in.Op)
}
})
}

// ReadFile 读取工作区内的文件（带路径越界防护）。
func ReadFile(root string) tool.InvokableTool {
t, err := utils.InferTool("read_file", "读取工作区内的一个文本文件",
func(_ context.Context, in *ReadFileArgs) (string, error) {
if strings.Contains(in.Path, "..") {
return "", fmt.Errorf("拒绝访问：路径不能包含 ..")
}
full := filepath.Join(root, in.Path)
data, err := os.ReadFile(full)
if err != nil {
return "", fmt.Errorf("读取失败: %w", err)
}
if len(data) > 32<<10 {
data = data[:32<<10]
}
return string(data), nil
})
if err != nil {
panic(err)
}
return t
}

func trimFloat(f float64) string {
return strconv.FormatFloat(f, 'f', -1, 64)
}

// ---------------------------------------------------------------------------
// 桥接：把任意 Eino 工具包进 toolpipe 流水线
// ---------------------------------------------------------------------------

type guardedTool struct {
inner tool.InvokableTool
pipe  *toolpipe.Pipeline
}

// Info 透传内层工具的描述。
func (g *guardedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
return g.inner.Info(ctx)
}

// InvokableRun 先过流水线，再执行内层工具。
//
// 注意：被拒绝时返回的是「正常文本」而不是 error ——
// 这样模型能读到拒绝理由并改道，而不是让整个 turn 报错。
func (g *guardedTool) InvokableRun(ctx context.Context, args string, opts ...tool.Option) (string, error) {
res := g.pipe.Run(ctx, toolpipe.Call{Name: toolName(ctx, g.inner), Args: args})
if res.IsError {
return "ERROR: " + res.Content, nil
}
return res.Content, nil
}

// nameCarrier 让调用方能带上网关名（简化为 context 传值）。
type nameKey struct{}

func toolName(ctx context.Context, inner tool.InvokableTool) string {
if v, ok := ctx.Value(nameKey{}).(string); ok {
return v
}
if info, err := inner.Info(ctx); err == nil && info != nil {
return info.Name
}
return "unknown"
}

// Guarded 用一个配置函数把工具包进流水线。
func Guarded(inner tool.InvokableTool, configure func(*toolpipe.Pipeline)) tool.InvokableTool {
p := toolpipe.New(func(ctx context.Context, call toolpipe.Call) (toolpipe.Result, error) {
out, err := inner.InvokableRun(ctx, call.Args)
if err != nil {
return toolpipe.Result{}, err
}
return toolpipe.Result{Content: out}, nil
})
configure(p)
return &guardedTool{inner: inner, pipe: p}
}
