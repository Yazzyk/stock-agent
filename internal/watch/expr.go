package watch

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// MetricSpec 描述一个可用指标（面板与 Agent 生成规则时参考）。
type MetricSpec struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Unit  string `json:"unit,omitempty"`
}

// MetricSpecs 是条件表达式允许引用的全部指标白名单。
// 规则只能读这些指标，不能调用函数、不能访问网络、不能执行代码——这是安全边界。
var MetricSpecs = map[string]MetricSpec{
	"quote.price":       {Name: "quote.price", Label: "现价", Unit: "元"},
	"quote.changePct":   {Name: "quote.changePct", Label: "涨跌幅", Unit: "%"},
	"quote.volumeRatio": {Name: "quote.volumeRatio", Label: "量比", Unit: ""},
	"quote.turnover":    {Name: "quote.turnover", Label: "换手率", Unit: "%"},
	"quote.amount":      {Name: "quote.amount", Label: "成交额", Unit: "元"},
	"flow.mainNet":      {Name: "flow.mainNet", Label: "主力净额", Unit: "元"},
	"flow.mainNetYi":    {Name: "flow.mainNetYi", Label: "主力净额", Unit: "亿元"},
	"flow.mainNetPct":   {Name: "flow.mainNetPct", Label: "主力净占比（净额/成交额）", Unit: "%"},
	"sector.changePct":  {Name: "sector.changePct", Label: "板块涨跌幅", Unit: "%"},
	"sector.mainNet":    {Name: "sector.mainNet", Label: "板块主力净额", Unit: "元"},
	"sector.mainNetYi":  {Name: "sector.mainNetYi", Label: "板块主力净额", Unit: "亿元"},
	"kline.rsi":         {Name: "kline.rsi", Label: "RSI(14)", Unit: ""},
	"kline.ma20dev":     {Name: "kline.ma20dev", Label: "距 MA20", Unit: "%"},
	"kline.volumeRatio": {Name: "kline.volumeRatio", Label: "量比(5日)", Unit: ""},
	"kline.macd":        {Name: "kline.macd", Label: "MACD 柱", Unit: ""},
	"kline.aboveMA20":   {Name: "kline.aboveMA20", Label: "站上 MA20", Unit: "1/0"},
	"kline.atrPct":      {Name: "kline.atrPct", Label: "ATR 占收盘", Unit: "%"},
	"news.hits":         {Name: "news.hits", Label: "命中新闻/公告数", Unit: "条"},
	"news.negative":     {Name: "news.negative", Label: "负面词命中", Unit: "1/0"},
	"time.trading":      {Name: "time.trading", Label: "是否交易时段", Unit: "1/0"},
	"time.hour":         {Name: "time.hour", Label: "小时", Unit: ""},
	"time.minute":       {Name: "time.minute", Label: "分钟", Unit: ""},
}

// MetricList 返回按名称排序的指标清单。
func MetricList() []MetricSpec {
	out := make([]MetricSpec, 0, len(MetricSpecs))
	for _, v := range MetricSpecs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------- 词法 ----------

type tokKind int

const (
	tkEOF tokKind = iota
	tkNum
	tkIdent
	tkOp
	tkLParen
	tkRParen
)

type token struct {
	kind tokKind
	text string
	num  float64
	pos  int
}

func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := rune(src[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case c >= '0' && c <= '9' || c == '.':
			start := i
			for i < len(src) && (src[i] >= '0' && src[i] <= '9' || src[i] == '.') {
				i++
			}
			v, err := strconv.ParseFloat(src[start:i], 64)
			if err != nil {
				return nil, fmt.Errorf("数字格式错误: %q", src[start:i])
			}
			out = append(out, token{kind: tkNum, text: src[start:i], num: v, pos: start})
		case unicode.IsLetter(c) || c == '_':
			start := i
			for i < len(src) {
				r := rune(src[i])
				if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' {
					i++
					continue
				}
				break
			}
			word := src[start:i]
			switch strings.ToLower(word) {
			case "and", "or", "not", "true", "false":
				out = append(out, token{kind: tkIdent, text: strings.ToLower(word), pos: start})
			default:
				out = append(out, token{kind: tkIdent, text: word, pos: start})
			}
		case c == '(':
			out = append(out, token{kind: tkLParen, text: "(", pos: i})
			i++
		case c == ')':
			out = append(out, token{kind: tkRParen, text: ")", pos: i})
			i++
		case c == '&' || c == '|' || c == '!' || c == '=' || c == '<' || c == '>':
			start := i
			if i+1 < len(src) && (src[i+1] == '&' || src[i+1] == '|' || src[i+1] == '=') {
				i++
			}
			i++
			out = append(out, token{kind: tkOp, text: src[start:i], pos: start})
		default:
			return nil, fmt.Errorf("非法字符 %q（位置 %d）", string(c), i)
		}
	}
	out = append(out, token{kind: tkEOF})
	return out, nil
}

// ---------- 语法树 ----------

type node interface {
	eval(ctx map[string]float64) (float64, error)
	deps(out map[string]struct{})
}

type numNode struct{ v float64 }

func (n numNode) eval(map[string]float64) (float64, error) { return n.v, nil }
func (n numNode) deps(map[string]struct{})                 {}

type identNode struct{ name string }

func (n identNode) eval(ctx map[string]float64) (float64, error) {
	if v, ok := ctx[n.name]; ok {
		return v, nil
	}
	if n.name == "true" {
		return 1, nil
	}
	if n.name == "false" {
		return 0, nil
	}
	// 指标缺失时按 0 处理：数据没取到不应该让整条规则崩掉。
	return 0, nil
}
func (n identNode) deps(out map[string]struct{}) {
	if n.name != "true" && n.name != "false" {
		out[n.name] = struct{}{}
	}
}

type cmpNode struct {
	op   string
	l, r node
}

func (n cmpNode) eval(ctx map[string]float64) (float64, error) {
	l, err := n.l.eval(ctx)
	if err != nil {
		return 0, err
	}
	r, err := n.r.eval(ctx)
	if err != nil {
		return 0, err
	}
	var ok bool
	switch n.op {
	case ">":
		ok = l > r
	case ">=":
		ok = l >= r
	case "<":
		ok = l < r
	case "<=":
		ok = l <= r
	case "==", "=":
		ok = l == r
	case "!=", "<>":
		ok = l != r
	default:
		return 0, fmt.Errorf("不支持的比较运算符 %q", n.op)
	}
	if ok {
		return 1, nil
	}
	return 0, nil
}

func (n cmpNode) deps(out map[string]struct{}) {
	n.l.deps(out)
	n.r.deps(out)
}

type logicNode struct {
	op   string // and | or
	l, r node
}

func (n logicNode) eval(ctx map[string]float64) (float64, error) {
	l, err := n.l.eval(ctx)
	if err != nil {
		return 0, err
	}
	if n.op == "and" && l == 0 {
		return 0, nil
	}
	if n.op == "or" && l != 0 {
		return 1, nil
	}
	r, err := n.r.eval(ctx)
	if err != nil {
		return 0, err
	}
	if n.op == "and" {
		if r != 0 {
			return 1, nil
		}
		return 0, nil
	}
	if r != 0 {
		return 1, nil
	}
	return 0, nil
}

func (n logicNode) deps(out map[string]struct{}) {
	n.l.deps(out)
	n.r.deps(out)
}

type notNode struct{ x node }

func (n notNode) eval(ctx map[string]float64) (float64, error) {
	v, err := n.x.eval(ctx)
	if err != nil {
		return 0, err
	}
	if v == 0 {
		return 1, nil
	}
	return 0, nil
}
func (n notNode) deps(out map[string]struct{}) { n.x.deps(out) }

// ---------- 解析 ----------

type parser struct {
	toks []token
	i    int
}

// Parse 解析条件表达式，返回可复用的语法树。
func Parse(src string) (node, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return numNode{v: 1}, nil
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tkEOF {
		return nil, fmt.Errorf("表达式在 %q 之后有多余内容（位置 %d）", p.peek().text, p.peek().pos)
	}
	return n, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tkOp && t.text == "||" || t.kind == tkIdent && t.text == "or" {
			p.next()
			right, err := p.parseAnd()
			if err != nil {
				return nil, err
			}
			left = logicNode{op: "or", l: left, r: right}
			continue
		}
		return left, nil
	}
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tkOp && t.text == "&&" || t.kind == tkIdent && t.text == "and" {
			p.next()
			right, err := p.parseNot()
			if err != nil {
				return nil, err
			}
			left = logicNode{op: "and", l: left, r: right}
			continue
		}
		return left, nil
	}
}

func (p *parser) parseNot() (node, error) {
	t := p.peek()
	if t.kind == tkOp && t.text == "!" || t.kind == tkIdent && t.text == "not" {
		p.next()
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return notNode{x: x}, nil
	}
	return p.parseCmp()
}

func (p *parser) parseCmp() (node, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tkOp {
		switch t.text {
		case ">", ">=", "<", "<=", "==", "=", "!=", "<>":
			p.next()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			return cmpNode{op: t.text, l: left, r: right}, nil
		}
	}
	return left, nil
}

func (p *parser) parsePrimary() (node, error) {
	t := p.next()
	switch t.kind {
	case tkNum:
		return numNode{v: t.num}, nil
	case tkIdent:
		return identNode{name: t.text}, nil
	case tkLParen:
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tkRParen {
			return nil, fmt.Errorf("缺少右括号")
		}
		p.next()
		return inner, nil
	case tkEOF:
		return nil, fmt.Errorf("表达式意外结束")
	default:
		return nil, fmt.Errorf("意外的记号 %q（位置 %d）", t.text, t.pos)
	}
}

// Eval 求值：返回 1 表示命中，0 表示未命中。
func Eval(n node, ctx map[string]float64) (bool, error) {
	v, err := n.eval(ctx)
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// referencedMetrics 提取表达式里引用到的指标名（校验白名单用）。
func referencedMetrics(src string) []string {
	n, err := Parse(src)
	if err != nil {
		return nil
	}
	set := map[string]struct{}{}
	n.deps(set)
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var identRe = regexp.MustCompile(`[a-zA-Z_][a-zA-Z0-9_.]*`)

// metricsInText 从文本中提取所有形如 a.b 的指标名（用于规则生成/校验的提示）。
func metricsInText(s string) []string {
	seen := map[string]struct{}{}
	for _, m := range identRe.FindAllString(s, -1) {
		if strings.Contains(m, ".") {
			seen[m] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
