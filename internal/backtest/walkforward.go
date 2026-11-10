package backtest

import (
	"fmt"
	"math"
	"strings"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// Fold 是一折 walk-forward：前段调参（样本内），后段验证（样本外）。
type Fold struct {
	Index      int      `json:"index"`
	TrainStart string   `json:"trainStart"`
	TrainEnd   string   `json:"trainEnd"`
	TestStart  string   `json:"testStart"`
	TestEnd    string   `json:"testEnd"`
	Best       ParamSet `json:"best"`
	TrainSharpe float64 `json:"trainSharpe"`
	TrainReturn float64 `json:"trainReturn"`
	TestSharpe  float64 `json:"testSharpe"`
	TestReturn  float64 `json:"testReturn"`
}

// WalkForwardResult 汇总样本内外表现，用于判断是否过拟合。
type WalkForwardResult struct {
	Code       string  `json:"code"`
	Strategy   string  `json:"strategy"`
	Folds      []Fold  `json:"folds"`
	ISReturn   float64 `json:"isReturn"`
	OOSReturn  float64 `json:"oosReturn"`
	OverfitGap float64 `json:"overfitGap"`
	Verdict    string  `json:"verdict"`
	Note       string  `json:"note,omitempty"`
}

// WalkForward 用滚动窗口做参数选择与样本外验证。
// 参数网格在每折的样本内段上按夏普选出最优，再在紧邻的样本外段上原样运行，
// 不做任何"回头看"的参数调整。
func WalkForward(code string, bars []contract.Bar, strategyID string, grid []ParamSet, folds int, opt Options) WalkForwardResult {
	res := WalkForwardResult{Code: code, Strategy: strategyID}
	if folds < 2 {
		folds = 2
	}
	if len(grid) == 0 {
		res.Note = "参数网格为空，无法做 walk-forward"
		return res
	}
	if len(bars) < folds*20 {
		res.Note = fmt.Sprintf("K 线不足（%d 根），至少需要 %d 根", len(bars), folds*20)
		return res
	}
	size := len(bars) / (folds + 1)
	if size < 10 {
		res.Note = "每个窗口不足 10 根，放弃 walk-forward"
		return res
	}

	compoundedOOS := 1.0
	compoundedIS := 1.0
	for k := 0; k < folds; k++ {
		train := bars[k*size : (k+1)*size]
		test := bars[(k+1)*size : (k+2)*size]
		bestIdx, bestSharpe, bestReturn := -1, math.Inf(-1), 0.0
		for i, params := range grid {
			r := Run(code, train, Build(strategyID, params), opt)
			score := r.Sharpe
			if math.IsNaN(score) {
				score = math.Inf(-1)
			}
			// 同分时用收益做二级排序，保证可复现。
			if score > bestSharpe || (score == bestSharpe && r.TotalReturn > bestReturn) {
				bestIdx, bestSharpe, bestReturn = i, score, r.TotalReturn
			}
		}
		if bestIdx < 0 {
			continue
		}
		best := grid[bestIdx]
		testRes := Run(code, test, Build(strategyID, best), opt)
		res.Folds = append(res.Folds, Fold{
			Index: k,
			TrainStart: train[0].Date, TrainEnd: train[len(train)-1].Date,
			TestStart: test[0].Date, TestEnd: test[len(test)-1].Date,
			Best: best, TrainSharpe: round2(bestSharpe), TrainReturn: round4(bestReturn),
			TestSharpe: testRes.Sharpe, TestReturn: round4(testRes.TotalReturn),
		})
		compoundedOOS *= 1 + testRes.TotalReturn
		compoundedIS *= 1 + bestReturn
	}
	if len(res.Folds) == 0 {
		res.Note = "没有可用的折"
		return res
	}
	res.OOSReturn = round4(compoundedOOS - 1)
	res.ISReturn = round4(compoundedIS - 1)
	res.OverfitGap = round4(res.ISReturn - res.OOSReturn)
	switch {
	case res.OverfitGap > 0.30:
		res.Verdict = "过拟合风险高"
	case res.OverfitGap > 0.10:
		res.Verdict = "过拟合风险中等"
	default:
		res.Verdict = "样本内外接近"
	}
	return res
}

// Report 输出可读的 walk-forward 结论（含"不要因为单次回测好就上线"的显式提醒）。
func (w WalkForwardResult) Report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Walk-forward 报告 · %s（%s）\n\n", w.Strategy, w.Code)
	fmt.Fprintf(&b, "- 样本内复利：%.2f%%，样本外复利：%.2f%%，差距：%.2f%%\n", w.ISReturn*100, w.OOSReturn*100, w.OverfitGap*100)
	fmt.Fprintf(&b, "- 结论：%s\n", w.Verdict)
	if w.Note != "" {
		fmt.Fprintf(&b, "- 说明：%s\n", w.Note)
	}
	if len(w.Folds) > 0 {
		b.WriteString("\n| 折 | 样本内 | 样本外 | 最优参数 | 样本内夏普 | 样本外夏普 |\n|---|---|---|---|---|---|\n")
		for _, f := range w.Folds {
			fmt.Fprintf(&b, "| %d | %s~%s | %s~%s | %s | %.2f | %.2f |\n",
				f.Index+1, f.TrainStart, f.TrainEnd, f.TestStart, f.TestEnd, paramsString(f.Best), f.TrainSharpe, f.TestSharpe)
		}
	}
	b.WriteString("\n> 样本外为负或样本内外差距过大时，结论应写「策略不成立」，而不是调整参数再跑一遍。\n")
	return b.String()
}

func paramsString(p ParamSet) string {
	if len(p) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, p[k]))
	}
	return strings.Join(parts, ",")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// DefaultGrid 返回策略的默认参数网格（用于 walk-forward 与参数敏感性）。
func DefaultGrid(strategyID string) []ParamSet {
	switch strategyID {
	case "ma":
		var out []ParamSet
		for _, fast := range []int{3, 5, 10} {
			for _, slow := range []int{10, 20, 30, 60} {
				if slow > fast {
					out = append(out, ParamSet{"fast": fast, "slow": slow})
				}
			}
		}
		return out
	case "rsi":
		var out []ParamSet
		for _, p := range []int{6, 14, 24} {
			for _, lo := range []float64{20, 30, 40} {
				out = append(out, ParamSet{"period": p, "lower": lo, "upper": 100 - lo})
			}
		}
		return out
	case "momentum":
		var out []ParamSet
		for _, look := range []int{5, 10, 20, 60} {
			for _, th := range []float64{0.0, 0.03, 0.05, 0.10} {
				out = append(out, ParamSet{"lookback": look, "threshold": th})
			}
		}
		return out
	default:
		return []ParamSet{{}}
	}
}
