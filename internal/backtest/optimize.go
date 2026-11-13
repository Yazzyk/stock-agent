package backtest

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// maxOptimizeCandidates 是参数优化返回给前端的候选上限（按夏普排序后的前 N 个）。
const maxOptimizeCandidates = 10

// minOptimizeBars 是参数优化所需的最少 K 线数；样本不足只返回说明，不 panic。
const minOptimizeBars = 60

// OptimizeCandidate 是参数网格里的一个候选：全样本指标 + 邻域稳健性。
type OptimizeCandidate struct {
	Params      ParamSet `json:"params"`
	TotalReturn float64  `json:"totalReturn"`
	CAGR        float64  `json:"cagr"`
	MaxDrawdown float64  `json:"maxDrawdown"`
	Sharpe      float64  `json:"sharpe"`
	TradeCount  int      `json:"tradeCount"`
	Stability   float64  `json:"stability"`
}

// OptimizeResult 是参数优化结果：候选表 + 推荐参数 + 推荐参数的样本外验证。
type OptimizeResult struct {
	Strategy    string              `json:"strategy"`
	Code        string              `json:"code"`
	Start       string              `json:"start"`
	End         string              `json:"end"`
	Candidates  []OptimizeCandidate `json:"candidates"`
	Best        *OptimizeCandidate  `json:"best"`
	WalkForward *WalkForwardResult  `json:"walkforward,omitempty"`
	Report      string              `json:"report"`
	Note        string              `json:"note,omitempty"`
}

// Optimize 在全样本上评估参数网格，按邻域稳健性挑推荐参数，再用 walk-forward 做样本外验证。
// 这是研究工具：推荐参数只是「值得继续验证的假设」，不是可直接上线的参数。
func Optimize(code string, bars []contract.Bar, strategyID string, grid []ParamSet, folds int, opt Options) OptimizeResult {
	res := OptimizeResult{Strategy: strategyID, Code: code, Candidates: []OptimizeCandidate{}}
	if len(bars) > 0 {
		res.Start, res.End = bars[0].Date, bars[len(bars)-1].Date
	}
	if len(grid) == 0 {
		res.Note = "参数网格为空，无法做参数优化"
		res.Report = res.renderReport()
		return res
	}
	if len(bars) < minOptimizeBars {
		res.Note = fmt.Sprintf("K 线不足（%d 根），参数优化至少需要 %d 根；请扩大回看区间或换标的", len(bars), minOptimizeBars)
		res.Report = res.renderReport()
		return res
	}

	all := make([]OptimizeCandidate, 0, len(grid))
	for _, params := range grid {
		r := Run(code, bars, Build(strategyID, params), opt)
		all = append(all, OptimizeCandidate{
			Params:      params,
			TotalReturn: finiteOrZero(r.TotalReturn),
			CAGR:        finiteOrZero(r.CAGR),
			MaxDrawdown: finiteOrZero(r.MaxDrawdown),
			Sharpe:      finiteOrZero(r.Sharpe),
			TradeCount:  r.TradeCount,
		})
	}
	domains := paramDomains(all)
	for i := range all {
		all[i].Stability = round2(neighborhoodStability(all[i], all, domains))
	}

	// 先按夏普（其次稳健性）排序，只保留前 N 个候选，避免把整张网格塞给前端。
	sort.Slice(all, func(i, j int) bool {
		if all[i].Sharpe != all[j].Sharpe {
			return all[i].Sharpe > all[j].Sharpe
		}
		return all[i].Stability > all[j].Stability
	})
	if len(all) > maxOptimizeCandidates {
		all = all[:maxOptimizeCandidates]
	}
	res.Candidates = all
	best := pickBestCandidate(all)
	res.Best = &best

	// 推荐参数仍要过一遍样本外：walk-forward 每折在样本内选参、紧邻样本外原样验证。
	wf := WalkForward(code, bars, strategyID, grid, folds, opt)
	res.WalkForward = &wf
	res.Report = res.renderReport()
	return res
}

// paramDomains 收集每个参数在网格里出现过的取值（升序），用于判断两组参数是否相邻。
func paramDomains(all []OptimizeCandidate) map[string][]float64 {
	seen := make(map[string]map[float64]struct{})
	for _, c := range all {
		for k, v := range c.Params {
			f, ok := paramFloat(v)
			if !ok {
				continue
			}
			if seen[k] == nil {
				seen[k] = make(map[float64]struct{})
			}
			seen[k][f] = struct{}{}
		}
	}
	out := make(map[string][]float64, len(seen))
	for k, set := range seen {
		vals := make([]float64, 0, len(set))
		for v := range set {
			vals = append(vals, v)
		}
		sort.Float64s(vals)
		out[k] = vals
	}
	return out
}

// neighborhoodStability 计算候选在参数邻域上的稳健性：
// 取网格中每个参数最多变化一格（Chebyshev 距离为 1）的邻居，计算它们夏普的算术平均，
// 用 邻居均值/本点夏普 归一到 0~1；本点夏普不为正或没有邻居时返回 0。
func neighborhoodStability(c OptimizeCandidate, all []OptimizeCandidate, domains map[string][]float64) float64 {
	if c.Sharpe <= 0 {
		return 0
	}
	sum, n := 0.0, 0
	for _, other := range all {
		if !isNeighborParams(c.Params, other.Params, domains) {
			continue
		}
		sum += other.Sharpe
		n++
	}
	if n == 0 {
		return 0
	}
	return clamp01((sum / float64(n)) / c.Sharpe)
}

// isNeighborParams 判断两组参数是否是网格中的相邻点：
// 每个参数在各自取值序列里最多移动一格，且至少有一个参数不同（支持 RSI 的 lower/upper 联动）。
func isNeighborParams(a, b ParamSet, domains map[string][]float64) bool {
	keys := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	diff := false
	for k := range keys {
		av, aok := paramFloat(a[k])
		bv, bok := paramFloat(b[k])
		if !aok || !bok {
			if fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
				return false
			}
			continue
		}
		if av == bv {
			continue
		}
		diff = true
		ai, bi := indexOfValue(domains[k], av), indexOfValue(domains[k], bv)
		if ai < 0 || bi < 0 || ai-bi > 1 || bi-ai > 1 {
			return false
		}
	}
	return diff
}

// paramFloat 把参数值转成 float64；不支持的类型返回 false。
func paramFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	default:
		return 0, false
	}
}

// indexOfValue 返回数值在升序序列里的下标，找不到返回 -1。
func indexOfValue(vals []float64, v float64) int {
	for i, x := range vals {
		if x == v {
			return i
		}
	}
	return -1
}

// pickBestCandidate 在候选里挑推荐参数：取稳健性中位数，只在稳健性不低于中位数的点里
// 挑夏普最高的，避免「单点夏普最高但参数一动就崩」的过拟合参数。
func pickBestCandidate(cands []OptimizeCandidate) OptimizeCandidate {
	if len(cands) == 0 {
		return OptimizeCandidate{}
	}
	stabilities := make([]float64, len(cands))
	for i, c := range cands {
		stabilities[i] = c.Stability
	}
	sort.Float64s(stabilities)
	median := stabilities[len(stabilities)/2]

	best := cands[0]
	bestSharpe := math.Inf(-1)
	for _, c := range cands {
		if c.Stability+1e-9 < median {
			continue
		}
		if c.Sharpe > bestSharpe || (c.Sharpe == bestSharpe && c.Stability > best.Stability) {
			best, bestSharpe = c, c.Sharpe
		}
	}
	return best
}

// finiteOrZero 把 NaN/Inf 归零，避免 JSON 序列化失败、前端显示 NaN。
func finiteOrZero(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// renderReport 生成中文 Markdown 报告（表格由前端渲染器转成真正的表格）。
func (o OptimizeResult) renderReport() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 参数优化报告 · %s（%s）\n\n", o.Strategy, o.Code)
	if o.Start != "" && o.End != "" {
		fmt.Fprintf(&b, "- 区间：%s ~ %s（全样本）\n", o.Start, o.End)
	}
	if o.Note != "" {
		fmt.Fprintf(&b, "- 说明：%s\n", o.Note)
	}
	b.WriteString("- 候选：按全样本夏普排序取前 10；稳健性 = 参数邻域（每个参数最多变化一格）的夏普均值 / 本点夏普，归一到 0~1。\n")
	b.WriteString("- 选择规则：只有稳健性不低于候选中位数的点才参与「推荐参数」评选，再在其中取夏普最高；不直接取单点夏普冠军。\n\n")

	if o.Best != nil && len(o.Candidates) > 0 {
		best := o.Best
		b.WriteString("## 推荐参数\n\n")
		fmt.Fprintf(&b, "- 参数：%s\n", paramsString(best.Params))
		fmt.Fprintf(&b, "- 样本内：总收益 %.2f%%，年化 %.2f%%，最大回撤 %.2f%%，夏普 %.2f，交易 %d 笔\n",
			best.TotalReturn*100, best.CAGR*100, best.MaxDrawdown*100, best.Sharpe, best.TradeCount)
		fmt.Fprintf(&b, "- 邻域稳健性：%.2f（0~1，越高表示参数轻微变动后表现越不容易崩）\n\n", best.Stability)
	}

	if len(o.Candidates) > 0 {
		b.WriteString("## 候选（按夏普排序）\n\n")
		b.WriteString("| 排名 | 参数 | 总收益 | 年化 | 最大回撤 | 夏普 | 交易数 | 稳健性 |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
		for i, c := range o.Candidates {
			mark := ""
			if o.Best != nil && paramsString(c.Params) == paramsString(o.Best.Params) {
				mark = " ★"
			}
			fmt.Fprintf(&b, "| %d%s | %s | %.2f%% | %.2f%% | %.2f%% | %.2f | %d | %.2f |\n",
				i+1, mark, paramsString(c.Params), c.TotalReturn*100, c.CAGR*100, c.MaxDrawdown*100, c.Sharpe, c.TradeCount, c.Stability)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Walk-forward 样本外验证\n\n")
	if o.WalkForward == nil {
		b.WriteString("（未执行 walk-forward）\n")
	} else if len(o.WalkForward.Folds) == 0 {
		fmt.Fprintf(&b, "（%s）\n", o.WalkForward.Note)
	} else {
		wf := o.WalkForward
		fmt.Fprintf(&b, "- 样本内复利：%.2f%%，样本外复利：%.2f%%，差距：%.2f%%\n", wf.ISReturn*100, wf.OOSReturn*100, wf.OverfitGap*100)
		fmt.Fprintf(&b, "- 结论：%s\n\n", wf.Verdict)
		b.WriteString("| 折 | 样本内 | 样本外 | 最优参数 | 样本内夏普 | 样本外夏普 |\n|---|---|---|---|---|---|\n")
		for _, f := range wf.Folds {
			fmt.Fprintf(&b, "| %d | %s~%s | %s~%s | %s | %.2f | %.2f |\n",
				f.Index+1, f.TrainStart, f.TrainEnd, f.TestStart, f.TestEnd, paramsString(f.Best), f.TrainSharpe, f.TestSharpe)
		}
	}

	b.WriteString("\n> 警告：不要用单次回测的最优参数直接上线。参数优化只回答「哪组参数在这段历史上更稳」，不回答「未来会不会有效」。至少要通过 walk-forward 样本外为正、邻域稳健性不低、并能说清经济含义，才值得进入下一轮验证。\n")
	return b.String()
}
