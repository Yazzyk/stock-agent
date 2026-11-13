// Package fuyao 把同花顺官方数据 API（fuyao.aicubes.cn）暴露成 Agent 工具与前端方法。
//
// 定位：数据层（internal/datasource）负责协议、限流、缓存与降级；本插件只做
// 「工具契约 + 输出裁剪 + 中文口径说明」，不重复实现 HTTP。
//
// 约定：
//   - 所有工具 Risk=network（会出网）；
//   - 返回值都带 endpoint / title / count / items，并在 note 里写清单位与时间戳；
//   - 客户端专用能力（官方 code=2004）返回 permission=client 与说明，不抛异常。
package fuyao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	"github.com/yazzyk/stock-agent/plugins/all"
)

const pluginID = "fuyao"

// init 把插件工厂登记到 all 注册表（plugins/builtin 负责 blank import 触发）。
func init() { all.Register(New) }

// New 是插件工厂（签名与其它内置插件一致）。
func New() plugin.Plugin { return &Plugin{} }

// Plugin 是同花顺官方数据源插件。
type Plugin struct {
	plugin.Base
	ctx *plugin.Context
}

var _ plugin.Plugin = (*Plugin)(nil)

// Manifest 描述插件身份与贡献点。
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "数据源 · 同花顺",
		Version:     "1.0.0",
		Description: "同花顺官方金融数据 API：行情/K线/估值/涨跌停/热榜/龙虎榜/竞价/财务/指数/基金/期货期权/资讯",
		Tags:        []string{"market", "datasource", "fuyao"},
		Provides: []string{
			"method:catalog", "method:query", "method:quote", "method:kline", "method:valuation",
			"method:limitPool", "method:limitStats", "method:hotRank", "method:special",
			"method:auction", "method:capitalFlow", "method:financials", "method:tickerSearch",
			"method:calendar", "method:index", "method:fund", "method:futures", "method:options",
			"method:news", "method:ipo", "method:marketDump",
			"tool:fuyao.catalog", "tool:fuyao.api", "tool:fuyao.quote", "tool:fuyao.kline",
			"tool:fuyao.valuation", "tool:fuyao.limitPool", "tool:fuyao.hotRank",
			"tool:fuyao.capitalFlow", "tool:fuyao.auction", "tool:fuyao.special",
			"tool:fuyao.financials", "tool:fuyao.tickerSearch", "tool:fuyao.calendar",
			"tool:fuyao.index", "tool:fuyao.fund", "tool:fuyao.futures", "tool:fuyao.options",
			"tool:fuyao.news", "tool:fuyao.ipo", "tool:fuyao.marketDump",
			"ui:fuyao",
		},
	}
}

// Activate 注册前端方法、Agent 工具。
func (p *Plugin) Activate(ctx *plugin.Context) error {
	p.ctx = ctx
	methods := []plugin.Method{
		{Name: "catalog", Description: "返回官方端点目录（离线，不发网络请求）", Fn: p.methodCatalog},
		{Name: "query", Description: "通用查询：endpoint + params，按目录校验参数", Fn: p.methodQuery},
		{Name: "quote", Description: "批量行情快照（官方，>50 自动分批）", Fn: p.methodQuote},
		{Name: "kline", Description: "批量历史 K 线（股票/指数/基金，10/批）", Fn: p.methodKline},
		{Name: "valuation", Description: "估值快照（PE/PB/PS/PCF）", Fn: p.methodValuation},
		{Name: "limitPool", Description: "涨停/跌停/炸板池", Fn: p.methodLimitPool},
		{Name: "limitStats", Description: "涨停强度与晋级统计", Fn: p.methodLimitStats},
		{Name: "hotRank", Description: "同花顺热榜（飙升/热股/历史）", Fn: p.methodHotRank},
		{Name: "special", Description: "特色数据（异动/龙虎榜/筹码/新增概念等）", Fn: p.methodSpecial},
		{Name: "auction", Description: "集合竞价快照/风向标/机会", Fn: p.methodAuction},
		{Name: "capitalFlow", Description: "主力资金实时/历史/高频（客户端专用会降级说明）", Fn: p.methodCapitalFlow},
		{Name: "financials", Description: "三大报表/财务指标", Fn: p.methodFinancials},
		{Name: "tickerSearch", Description: "标的检索", Fn: p.methodTickerSearch},
		{Name: "calendar", Description: "交易日历", Fn: p.methodCalendar},
		{Name: "index", Description: "指数目录/成分股/反查/快照/K线", Fn: p.methodIndex},
		{Name: "fund", Description: "基金资料/行情/持仓/业绩/经理", Fn: p.methodFund},
		{Name: "futures", Description: "期货品种/合约/持仓/基差/时间轴", Fn: p.methodFutures},
		{Name: "options", Description: "期权品种/合约/行情/时间轴", Fn: p.methodOptions},
		{Name: "news", Description: "快讯/公告/资讯/专题/个股新闻", Fn: p.methodNews},
		{Name: "ipo", Description: "A 股/港股打新日历与详情", Fn: p.methodIPO},
		{Name: "marketDump", Description: "全市场 Parquet 下载链接", Fn: p.methodMarketDump},
	}
	for _, m := range methods {
		if err := ctx.Methods.Add(m); err != nil {
			return err
		}
	}
	for _, tool := range p.tools() {
		if err := ctx.Tools.Add(tool); err != nil {
			return err
		}
	}
	// 面板槽位：PanelHost 按 component 从 src/panels/*.vue 里找同名组件，
	// 所以 component 必须与文件名一致（frontend/src/panels/fuyao.vue）。
	return ctx.UI.Add(plugin.UISlot{
		ID:        "fuyao",
		Title:     "数据源",
		Icon:      "database",
		Group:     "行情",
		Component: "fuyao",
		Order:     16,
	})
}

// tools 返回全部工具定义（集中在函数里便于核对数量与 schema）。
func (p *Plugin) tools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name:    "fuyao.catalog",
			Title:   "同花顺官方数据目录",
			Summary: "不确定该用哪个官方接口时先查目录：返回 124 个端点的 ID、路径、必填参数、上限与权限（客户端专用会标注）。",
			Tags:    []string{"fuyao", "meta"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"group":{"type":"string","description":"按分组过滤，如 行情/资金/特色数据/财务/基金/期货"},
				"endpoint":{"type":"string","description":"查单个端点详情（ID 或路径）"}
			},"additionalProperties":false}`),
			Run: p.toolCatalog,
		},
		{
			Name:    "fuyao.api",
			Title:   "同花顺官方通用查询",
			Summary: "查询目录里的任意官方端点（长尾能力兜底）：endpoint 用目录 ID，params 的键必须与目录一致；输出自动裁剪并附口径。",
			Tags:    []string{"fuyao", "datasource"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"endpoint":{"type":"string","minLength":1,"description":"目录 ID，如 a-share.prices.snapshot / news.flash.list；先用 fuyao.catalog 查"},
				"params":{"type":"object","additionalProperties":{"type":"string"},"description":"端点参数，键名必须与目录 Params 一致（大小写敏感）"},
				"limit":{"type":"integer","minimum":1,"maximum":200,"description":"输出条数上限，默认 30"}
			},"required":["endpoint"],"additionalProperties":false}`),
			Run: p.toolAPI,
		},
		{
			Name:    "fuyao.quote",
			Title:   "官方行情快照",
			Summary: "批量取 A 股最新价/涨跌幅/量额（官方一次 50 个）：判断当日强弱、给自选列表补行情时优先用它，比逐只查快。",
			Tags:    []string{"fuyao", "market", "quote"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"codes":{"type":"array","items":{"type":"string","minLength":1},"minItems":1,"maxItems":50,"description":"股票代码，如 600519 / sh600519 / 600519.SH"},
				"limit":{"type":"integer","minimum":1,"maximum":50}
			},"required":["codes"],"additionalProperties":false}`),
			Run: p.toolQuote,
		},
		{
			Name:    "fuyao.kline",
			Title:   "官方历史K线",
			Summary: "批量取日/周/月 K 线（A 股 10 个/批；指数与基金当前仅日线），支持前/后复权。看历史走势、比较多只标的时用。",
			Tags:    []string{"fuyao", "market", "kline"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"codes":{"type":"array","items":{"type":"string","minLength":1},"minItems":1,"maxItems":10},
				"interval":{"type":"string","enum":["1d","1w","1mo"],"default":"1d"},
				"adjust":{"type":"string","enum":["forward","none","backward"],"default":"forward","description":"forward=前复权、backward=后复权"},
				"asset":{"type":"string","enum":["stock","index","fund"],"default":"stock"},
				"start":{"type":"string","description":"起始日期 YYYY-MM-DD，可省略"},
				"end":{"type":"string","description":"结束日期 YYYY-MM-DD，可省略（默认今天）"},
				"limit":{"type":"integer","minimum":1,"maximum":2000,"default":120}
			},"required":["codes"],"additionalProperties":false}`),
			Run: p.toolKline,
		},
		{
			Name:    "fuyao.valuation",
			Title:   "官方估值快照",
			Summary: "批量查 PE(TTM/MRQ)、PB(MRQ)、PS(TTM)、PCF(TTM)：做估值对比、判断贵不贵时用。",
			Tags:    []string{"fuyao", "valuation", "fundamental"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"codes":{"type":"array","items":{"type":"string","minLength":1},"minItems":1,"maxItems":100}
			},"required":["codes"],"additionalProperties":false}`),
			Run: p.toolValuation,
		},
		{
			Name:    "fuyao.limitPool",
			Title:   "官方涨跌停池",
			Summary: "查涨停/跌停/炸板股票池（含连板数、封单额、涨停原因）：复盘情绪、找主线时用。",
			Tags:    []string{"fuyao", "special", "limit"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"pool":{"type":"string","enum":["up","down","break"],"default":"up"},
				"limit":{"type":"integer","minimum":1,"maximum":200,"default":50}
			},"additionalProperties":false}`),
			Run: p.toolLimitPool,
		},
		{
			Name:    "fuyao.hotRank",
			Title:   "同花顺热榜",
			Summary: "查飙升榜/热股榜/历史热股榜：判断市场关注度与情绪焦点时用（heat 是热度字符串，不是金额）。",
			Tags:    []string{"fuyao", "special", "hot"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"kind":{"type":"string","enum":["skyrocket","hot","history"],"default":"hot"},
				"period":{"type":"string","enum":["day","hour"],"default":"day"},
				"date":{"type":"string","description":"kind=history 时的自然日 YYYY-MM-DD，可省略（默认昨日）"},
				"limit":{"type":"integer","minimum":1,"maximum":100,"default":30}
			},"additionalProperties":false}`),
			Run: p.toolHotRank,
		},
		{
			Name:    "fuyao.capitalFlow",
			Title:   "官方主力资金/高频",
			Summary: "查单只个股资金流快照或历史、个股/指数高频分时。注意：外部 Key 调用会返回『客户端专用』说明，须转用新浪资金流兜底。",
			Tags:    []string{"fuyao", "flow"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","enum":["snapshot","history","highfreq","intraday"],"default":"snapshot"},
				"code":{"type":"string","minLength":1,"description":"股票代码，如 600519"},
				"interval":{"type":"string","enum":["1d","1m"],"default":"1d"},
				"date":{"type":"string","description":"intraday 用 YYYY-MM-DD，可省略"},
				"start":{"type":"string","description":"YYYY-MM-DD，可省略"},
				"end":{"type":"string","description":"YYYY-MM-DD，可省略"}
			},"required":["code"],"additionalProperties":false}`),
			Run: p.toolCapitalFlow,
		},
		{
			Name:    "fuyao.auction",
			Title:   "官方集合竞价",
			Summary: "查集合竞价快照（live/final）、短线风向标基准、竞价机会：开盘前判断高开/抢筹时用。",
			Tags:    []string{"fuyao", "auction"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","enum":["snapshot","benchmark","opportunities"],"default":"snapshot"},
				"codes":{"type":"array","items":{"type":"string","minLength":1},"maxItems":100},
				"stage":{"type":"string","enum":["live","final"],"default":"final"},
				"date":{"type":"string","description":"YYYY-MM-DD，可省略"}
			},"additionalProperties":false}`),
			Run: p.toolAuction,
		},
		{
			Name:    "fuyao.special",
			Title:   "官方特色数据",
			Summary: "查个股异动原因、异动监控、龙虎榜（含机构/营业部）、筹码形态、新增概念、成交额预测：复盘与题材归因时用。",
			Tags:    []string{"fuyao", "special"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","enum":["anomaly","anomaly-stock","abnormal","dragon-tiger","dragon-tiger-institution","department-record","department-history","chip-shape","new-concepts","turnover"],"default":"anomaly"},
				"codes":{"type":"array","items":{"type":"string","minLength":1},"maxItems":50},
				"code":{"type":"string","description":"单只代码（机构/营业部视图可用）"},
				"date":{"type":"string","description":"YYYY-MM-DD"},
				"tags":{"type":"array","items":{"type":"string"},"maxItems":10,"description":"异动标签码"},
				"shape":{"type":"string","enum":["double_peak","high_position_dense","low_position_dense","low_position_locked"]},
				"departmentId":{"type":"string"},
				"limit":{"type":"integer","minimum":1,"maximum":200,"default":20}
			},"additionalProperties":false}`),
			Run: p.toolSpecial,
		},
		{
			Name:    "fuyao.financials",
			Title:   "官方财务报表/指标",
			Summary: "查利润表/资产负债表/现金流量表（单只或 ≤10 只批量）与五类财务指标：分析基本面、同比环比时用。",
			Tags:    []string{"fuyao", "financial"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"kind":{"type":"string","enum":["income","balance","cash","indicators"],"default":"income"},
				"code":{"type":"string","description":"单只代码（indicators 必填）"},
				"codes":{"type":"array","items":{"type":"string","minLength":1},"maxItems":10,"description":"批量报表用"},
				"period":{"type":"string","enum":["annual","quarterly"],"default":"annual"},
				"report":{"type":"string","description":"indicators 必填，格式 yyyy-1..4，如 2025-3"},
				"limit":{"type":"integer","minimum":1,"maximum":20,"default":4}
			},"additionalProperties":false}`),
			Run: p.toolFinancials,
		},
		{
			Name:    "fuyao.tickerSearch",
			Title:   "官方标的检索",
			Summary: "按代码/名称/拼音检索股票、ETF、指数、期货、期权：用户只说名字、不确定代码时用。",
			Tags:    []string{"fuyao", "meta"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"keyword":{"type":"string","minLength":1},
				"assetType":{"type":"string","description":"规范化资产类型，如 a_share/etf/index/futures/options，可省略"},
				"limit":{"type":"integer","minimum":1,"maximum":50,"default":10}
			},"required":["keyword"],"additionalProperties":false}`),
			Run: p.toolTickerSearch,
		},
		{
			Name:    "fuyao.calendar",
			Title:   "官方交易日历",
			Summary: "查 A 股交易日（yyyyMMdd）：判断某天是否开市、算持有天数时用。",
			Tags:    []string{"fuyao", "meta"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"start":{"type":"string","description":"起始日期 yyyyMMdd，可省略"},
				"end":{"type":"string","description":"结束日期 yyyyMMdd，可省略"}
			},"additionalProperties":false}`),
			Run: p.toolCalendar,
		},
		{
			Name:    "fuyao.index",
			Title:   "官方指数/板块",
			Summary: "查同花顺概念/行业/地域目录、成分股、个股所属板块反查、指数快照/日线：拆解题材与板块联动时用。",
			Tags:    []string{"fuyao", "index"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","enum":["catalog","constituents","memberships","snapshot","kline"],"default":"catalog"},
				"tag":{"type":"string","enum":["cn_concept","industry","region","tszs"],"default":"cn_concept"},
				"indexCode":{"type":"string","description":"指数 thscode，如 885431.TI / 000300.SH"},
				"codes":{"type":"array","items":{"type":"string","minLength":1},"maxItems":100},
				"limit":{"type":"integer","minimum":1,"maximum":1000,"default":60}
			},"additionalProperties":false}`),
			Run: p.toolIndex,
		},
		{
			Name:    "fuyao.fund",
			Title:   "官方基金数据",
			Summary: "查基金资料/行情/净值/收益/持仓/持有人/经理/公司/分红/诊断/额度/募集/ETF 申赎：基金研究时用。",
			Tags:    []string{"fuyao", "fund"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","minLength":1,"description":"视图名，如 profile/snapshot/nav/holdings/manager/quota-summary；先用 fuyao.catalog 查端点或看文档"},
				"thscode":{"type":"string","description":"基金完整代码，如 510300.SH / 025480.OF"},
				"managerId":{"type":"string"},
				"companyId":{"type":"string"},
				"params":{"type":"object","additionalProperties":{"type":"string"},"description":"其它端点参数（如 range/nav_type/subscribe/tab）"},
				"limit":{"type":"integer","minimum":1,"maximum":200,"default":30}
			},"required":["view"],"additionalProperties":false}`),
			Run: p.toolFund,
		},
		{
			Name:    "fuyao.futures",
			Title:   "官方期货数据",
			Summary: "查期货品种/合约/主连/持仓/仓单/基差/交易时间轴：期货基础研究时用（部分行情上游暂不可用）。",
			Tags:    []string{"fuyao", "futures"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","minLength":1,"description":"视图名，如 varieties/contract/main/positions-variety/basis/session"},
				"params":{"type":"object","additionalProperties":{"type":"string"},"description":"例如 thscode/variety/date/start_date/end_date"},
				"limit":{"type":"integer","minimum":1,"maximum":200,"default":30}
			},"required":["view"],"additionalProperties":false}`),
			Run: p.toolFutures,
		},
		{
			Name:    "fuyao.options",
			Title:   "官方期权数据",
			Summary: "查期权品种/合约/分时/K线/交易时间轴：期权链与合约研究时用（K线上游可能暂不可用）。",
			Tags:    []string{"fuyao", "options"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","minLength":1,"description":"视图名：varieties/contracts/contract/kline/intraday/session"},
				"params":{"type":"object","additionalProperties":{"type":"string"},"description":"例如 thscode/limit/offset"},
				"limit":{"type":"integer","minimum":1,"maximum":200,"default":30}
			},"required":["view"],"additionalProperties":false}`),
			Run: p.toolOptions,
		},
		{
			Name:    "fuyao.news",
			Title:   "官方资讯",
			Summary: "查快讯/公告详情/资讯详情/热门专题/个股分时新闻/异动解读：找消息面催化时用。",
			Tags:    []string{"fuyao", "news"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","enum":["flash","flash-refresh","notice","article","topic","stock-timeline","anomaly","events"],"default":"flash"},
				"tagId":{"type":"integer"},
				"seq":{"type":"string"},
				"code":{"type":"string"},
				"date":{"type":"string"},
				"params":{"type":"object","additionalProperties":{"type":"string"},"description":"其它端点参数，如 begin_date/end_date/offset/tab_id/client_type/encoded_seq"},
				"limit":{"type":"integer","minimum":1,"maximum":100,"default":20}
			},"additionalProperties":false}`),
			Run: p.toolNews,
		},
		{
			Name:    "fuyao.ipo",
			Title:   "官方打新日历",
			Summary: "查 A 股/港股打新日历与发行详情：看新股申购节奏时用。",
			Tags:    []string{"fuyao", "ipo"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"view":{"type":"string","enum":["a-ipo","hk-application","hk-calendar","hk-detail"],"default":"a-ipo"},
				"market":{"type":"string","enum":["sh_sz","bj"],"default":"sh_sz"},
				"status":{"type":"string","description":"a-ipo: upcoming/subscribable/awaiting_listing；hk-calendar: subscribable/awaiting_listing"},
				"issueCode":{"type":"string"},
				"limit":{"type":"integer","minimum":1,"maximum":100,"default":30}
			},"additionalProperties":false}`),
			Run: p.toolIPO,
		},
		{
			Name:    "fuyao.marketDump",
			Title:   "全市场数据下载链接",
			Summary: "取全市场 Parquet 预签名下载链接（10 年日 K / 最近 10 交易日 / 复权因子）：做离线回测、建本地库时用。链接有时效。",
			Tags:    []string{"fuyao", "dump"},
			Risk:    plugin.RiskNetwork,
			Schema: json.RawMessage(`{"type":"object","properties":{
				"kind":{"type":"string","enum":["10y","10d","adjust"],"default":"10d"}
			},"additionalProperties":false}`),
			Run: p.toolMarketDump,
		},
	}
}

// ---------- 数据层入口 ----------

// data 返回实现了官方数据能力的数据服务；未接入时给出可操作错误。
func (p *Plugin) data() (contract.FuyaoService, error) {
	if p.ctx == nil || p.ctx.Data == nil {
		return nil, fmt.Errorf("数据层不可用")
	}
	svc, ok := p.ctx.Data.(contract.FuyaoService)
	if !ok {
		return nil, fmt.Errorf("当前数据层未实现官方数据能力（FuyaoService），请先在设置中心配置 API Key")
	}
	return svc, nil
}

// asOf 返回当前时间的 RFC3339（Asia/Shanghai），工具输出统一带时间戳。
func (p *Plugin) asOf() string {
	return p.ctx.Clock().In(cstZone()).Format(time.RFC3339)
}

// cstZone 是 A 股统一时区。
func cstZone() *time.Location { return time.FixedZone("CST", 8*3600) }

// dataReply 把通用查询结果包成工具输出：保留口径与权限说明。
func (p *Plugin) dataReply(data contract.FuyaoData) map[string]any {
	reply := map[string]any{
		"endpoint": data.Endpoint,
		"title":    data.Title,
		"group":    data.Group,
		"count":    data.Count,
		"asOf":     p.asOf(),
	}
	if len(data.Items) > 0 {
		reply["items"] = data.Items
	}
	if data.Raw != nil {
		reply["data"] = data.Raw
	}
	if data.Timestamp > 0 {
		reply["dataTimestamp"] = time.UnixMilli(data.Timestamp).In(cstZone()).Format(time.RFC3339)
	}
	if data.Truncated {
		reply["truncated"] = true
	}
	if data.Permission != "" {
		reply["permission"] = data.Permission
	}
	if data.Note != "" {
		reply["note"] = data.Note
	}
	if len(data.Params) > 0 {
		reply["params"] = data.Params
	}
	return reply
}

// permissionReply 是客户端专用能力的统一降级输出（不抛异常，让 LLM 能解释）。
func (p *Plugin) permissionReply(title, note string) map[string]any {
	return map[string]any{
		"permission": "client",
		"title":      title,
		"count":      0,
		"items":      []any{},
		"note":       note,
		"asOf":       p.asOf(),
	}
}

// decodeArgs 解析工具参数（空 payload 视为 {}）。
func decodeArgs(raw json.RawMessage, dst any) error {
	payload := strings.TrimSpace(string(raw))
	if payload == "" {
		payload = "{}"
	}
	if err := json.Unmarshal([]byte(payload), dst); err != nil {
		return fmt.Errorf("参数格式错误: %w", err)
	}
	return nil
}

// run 统一处理工具错误：权限不足转成降级说明，其它错误带上中文前缀。
func (p *Plugin) run(err error, reply any, fallbackTitle string) (any, error) {
	if err == nil {
		return reply, nil
	}
	if errors.Is(err, contract.ErrFuyaoPermission) {
		return p.permissionReply(fallbackTitle, "该能力为同花顺 AI 客户端专用（官方 code=2004），外部 API Key 无权访问；请改用腾讯/新浪口径或提示用户。"), nil
	}
	return nil, err
}

// ---------- 目录 / 通用 ----------

type catalogArgs struct {
	Group    string `json:"group"`
	Endpoint string `json:"endpoint"`
}

// methodCatalog 返回端点目录（可按分组或 ID 过滤）。
func (p *Plugin) methodCatalog(_ context.Context, payload json.RawMessage) (any, error) {
	var args catalogArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	all := svc.FuyaoCatalog(context.Background())
	group := strings.TrimSpace(args.Group)
	endpoint := strings.TrimSpace(args.Endpoint)
	items := make([]contract.FuyaoEndpoint, 0, len(all))
	groups := map[string]bool{}
	for _, ep := range all {
		groups[ep.Group] = true
		if group != "" && !strings.Contains(ep.Group, group) {
			continue
		}
		if endpoint != "" && ep.ID != endpoint && ep.Path != endpoint {
			continue
		}
		items = append(items, ep)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	return map[string]any{"total": len(all), "count": len(items), "groups": names, "endpoints": items, "asOf": p.asOf()}, nil
}

// toolCatalog 是 fuyao.catalog 工具入口：只读目录，不发网络请求。
func (p *Plugin) toolCatalog(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodCatalog(ctx, payload)
}

type apiArgs struct {
	Endpoint string            `json:"endpoint"`
	Params   map[string]string `json:"params"`
	Limit    int               `json:"limit"`
}

// methodQuery 执行通用查询（所有官方端点都可走这里）。
func (p *Plugin) methodQuery(ctx context.Context, payload json.RawMessage) (any, error) {
	var args apiArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Endpoint) == "" {
		return nil, fmt.Errorf("endpoint 不能为空（先用 fuyao.catalog 查）")
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	data, err := svc.FuyaoQuery(ctx, contract.FuyaoQuery{Endpoint: args.Endpoint, Params: args.Params, Limit: args.Limit})
	if err != nil {
		return p.run(err, nil, args.Endpoint)
	}
	return p.dataReply(data), nil
}

// toolAPI 是 fuyao.api 工具入口：通用端点查询（长尾能力兜底）。
func (p *Plugin) toolAPI(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodQuery(ctx, payload)
}

// ---------- 行情 ----------

type quoteArgs struct {
	Codes []string `json:"codes"`
	Limit int      `json:"limit"`
}

// methodQuote 批量取官方快照（>50 自动分批；输出为官方单位，见 note）。
func (p *Plugin) methodQuote(ctx context.Context, payload json.RawMessage) (any, error) {
	var args quoteArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	if len(args.Codes) == 0 {
		return nil, fmt.Errorf("codes 不能为空")
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	rows, err := svc.FuyaoQuoteRows(ctx, args.Codes)
	if err != nil && len(rows) == 0 {
		return nil, err
	}
	limit := args.Limit
	if limit <= 0 || limit > len(rows) {
		limit = len(rows)
	}
	return map[string]any{
		"count": len(rows),
		"items": rows[:limit],
		"asOf":  p.asOf(),
		"note":  "官方快照：volume 单位股、turnover 单位元、price_change_ratio_pct 为百分数原值；未返回的代码由腾讯/新浪链路兜底。",
	}, nil
}

// toolQuote 是 fuyao.quote 工具入口：批量官方快照。
func (p *Plugin) toolQuote(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodQuote(ctx, payload)
}

type klineArgs struct {
	Codes    []string `json:"codes"`
	Interval string   `json:"interval"`
	Adjust   string   `json:"adjust"`
	Asset    string   `json:"asset"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Limit    int      `json:"limit"`
}

// methodKline 批量取历史 K 线，输出按代码分组的最近 limit 根。
func (p *Plugin) methodKline(ctx context.Context, payload json.RawMessage) (any, error) {
	var args klineArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	if len(args.Codes) == 0 {
		return nil, fmt.Errorf("codes 不能为空")
	}
	start, err := parseDate(args.Start)
	if err != nil {
		return nil, fmt.Errorf("start 格式错误（需要 YYYY-MM-DD）: %w", err)
	}
	end, err := parseDate(args.End)
	if err != nil {
		return nil, fmt.Errorf("end 格式错误（需要 YYYY-MM-DD）: %w", err)
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	series, err := svc.FuyaoKline(ctx, args.Codes, args.Interval, start, end, args.Adjust, args.Asset, args.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"count":  len(series),
		"series": series,
		"asOf":   p.asOf(),
		"note":   "官方 K 线：volume 单位股、turnover 单位元；adjust=forward 为前复权。",
	}, nil
}

// toolKline 是 fuyao.kline 工具入口：批量历史 K 线。
func (p *Plugin) toolKline(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodKline(ctx, payload)
}

// parseDate 解析 YYYY-MM-DD（空串返回零值）。
func parseDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", raw, cstZone())
}

type valuationArgs struct {
	Codes []string `json:"codes"`
}

// methodValuation 批量取估值快照。
func (p *Plugin) methodValuation(ctx context.Context, payload json.RawMessage) (any, error) {
	var args valuationArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	if len(args.Codes) == 0 {
		return nil, fmt.Errorf("codes 不能为空")
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	items, err := svc.FuyaoValuationRows(ctx, args.Codes)
	if err != nil {
		return p.run(err, nil, "估值快照")
	}
	return map[string]any{"count": len(items), "items": items, "asOf": p.asOf(),
		"note": "估值倍数：PE/PB/PS/PCF 为倍，不是百分数；数据为最新快照。"}, nil
}

// toolValuation 是 fuyao.valuation 工具入口：批量估值快照。
func (p *Plugin) toolValuation(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodValuation(ctx, payload)
}

// ---------- 特色数据 ----------

type limitPoolArgs struct {
	Pool  string `json:"pool"`
	Limit int    `json:"limit"`
}

// methodLimitPool 返回涨跌停/炸板池。
func (p *Plugin) methodLimitPool(ctx context.Context, payload json.RawMessage) (any, error) {
	var args limitPoolArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	rows, err := svc.FuyaoLimitRows(ctx, args.Pool, args.Limit)
	if err != nil {
		return p.run(err, nil, "涨跌停池")
	}
	return map[string]any{"pool": args.Pool, "count": len(rows), "items": rows, "asOf": p.asOf(),
		"note": "涨跌幅/封板率为百分数原值；seal_money/max_seal_money 单位元；open_times 为开板次数（可能 null）。"}, nil
}

// toolLimitPool 是 fuyao.limitPool 工具入口：涨停/跌停/炸板池。
func (p *Plugin) toolLimitPool(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodLimitPool(ctx, payload)
}

type limitStatsArgs struct {
	Date string `json:"date"`
}

// methodLimitStats 返回涨停强度与晋级统计。
func (p *Plugin) methodLimitStats(ctx context.Context, payload json.RawMessage) (any, error) {
	var args limitStatsArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	raw, err := svc.FuyaoLimitStatistics(ctx, args.Date)
	if err != nil {
		return p.run(err, nil, "涨停统计")
	}
	return map[string]any{"data": raw, "asOf": p.asOf()}, nil
}

type hotRankArgs struct {
	Kind   string `json:"kind"`
	Period string `json:"period"`
	Date   string `json:"date"`
	Limit  int    `json:"limit"`
}

// methodHotRank 返回热榜。
func (p *Plugin) methodHotRank(ctx context.Context, payload json.RawMessage) (any, error) {
	var args hotRankArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	// 历史热榜可指定自然日；未指定时数据层默认昨日。
	if strings.EqualFold(strings.TrimSpace(args.Kind), "history") && strings.TrimSpace(args.Date) != "" {
		data, err := svc.FuyaoQuery(ctx, contract.FuyaoQuery{
			Endpoint: "a-share.special-data.hot-stock-list-history",
			Params:   map[string]string{"date": strings.TrimSpace(args.Date)},
			Limit:    args.Limit,
		})
		if err != nil {
			return p.run(err, nil, "历史热榜")
		}
		return p.dataReply(data), nil
	}
	rows, err := svc.FuyaoHotRows(ctx, args.Kind, args.Period, args.Limit)
	if err != nil {
		return p.run(err, nil, "热榜")
	}
	return map[string]any{"kind": args.Kind, "period": args.Period, "count": len(rows), "items": rows, "asOf": p.asOf(),
		"note": "heat 是上游热度字符串（不是金额）；rank_change 正数表示排名上升。"}, nil
}

// toolHotRank 是 fuyao.hotRank 工具入口：同花顺热榜。
func (p *Plugin) toolHotRank(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodHotRank(ctx, payload)
}

type specialArgs struct {
	View         string   `json:"view"`
	Codes        []string `json:"codes"`
	Code         string   `json:"code"`
	Date         string   `json:"date"`
	Tags         []string `json:"tags"`
	Shape        string   `json:"shape"`
	DepartmentID string   `json:"departmentId"`
	Limit        int      `json:"limit"`
}

// methodSpecial 按 view 分发特色数据。
func (p *Plugin) methodSpecial(ctx context.Context, payload json.RawMessage) (any, error) {
	var args specialArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	switch strings.ToLower(strings.TrimSpace(args.View)) {
	case "", "anomaly":
		items, err := svc.FuyaoAnomalyList(ctx, args.Tags, limit)
		if err != nil {
			return p.run(err, nil, "个股异动")
		}
		return map[string]any{"view": "anomaly", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "anomaly-stock":
		items, err := svc.FuyaoAnomalyByStock(ctx, args.Codes)
		if err != nil {
			return p.run(err, nil, "个股异动")
		}
		return map[string]any{"view": "anomaly-stock", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "abnormal":
		items, err := svc.FuyaoAbnormalMonitor(ctx, "current", limit)
		if err != nil {
			return p.run(err, nil, "异动监控")
		}
		return map[string]any{"view": "abnormal", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "dragon-tiger":
		raw, err := svc.FuyaoDragonTiger(ctx, "", args.Date)
		if err != nil {
			return p.run(err, nil, "龙虎榜")
		}
		return map[string]any{"view": "dragon-tiger", "data": raw, "asOf": p.asOf()}, nil
	case "dragon-tiger-institution":
		items, err := svc.FuyaoDragonTigerInstitution(ctx, args.Code, args.Date, 0)
		if err != nil {
			return p.run(err, nil, "龙虎榜机构")
		}
		return map[string]any{"view": "dragon-tiger-institution", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "department-record":
		raw, err := svc.FuyaoDepartmentClassicRecord(ctx, args.DepartmentID)
		if err != nil {
			return p.run(err, nil, "营业部战绩")
		}
		return map[string]any{"view": "department-record", "data": raw, "asOf": p.asOf()}, nil
	case "department-history":
		items, err := svc.FuyaoDepartmentHistory(ctx, args.DepartmentID, limit)
		if err != nil {
			return p.run(err, nil, "营业部历史")
		}
		return map[string]any{"view": "department-history", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "chip-shape":
		items, err := svc.FuyaoChipShape(ctx, args.Shape, args.Date, limit)
		if err != nil {
			return p.run(err, nil, "筹码形态")
		}
		return map[string]any{"view": "chip-shape", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "new-concepts":
		items, err := svc.FuyaoNewConcepts(ctx, limit)
		if err != nil {
			return p.run(err, nil, "新增概念")
		}
		return map[string]any{"view": "new-concepts", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "turnover":
		raw, err := svc.FuyaoTurnoverPrediction(ctx)
		if err != nil {
			return p.run(err, nil, "成交额预测")
		}
		return map[string]any{"view": "turnover", "data": raw, "asOf": p.asOf()}, nil
	}
	return nil, fmt.Errorf("未知 view %q（可用 anomaly/anomaly-stock/abnormal/dragon-tiger/dragon-tiger-institution/department-record/department-history/chip-shape/new-concepts/turnover）", args.View)
}

// toolSpecial 是 fuyao.special 工具入口：异动/龙虎榜/筹码等特色数据。
func (p *Plugin) toolSpecial(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodSpecial(ctx, payload)
}

// ---------- 竞价 ----------

type auctionArgs struct {
	View  string   `json:"view"`
	Codes []string `json:"codes"`
	Stage string   `json:"stage"`
	Date  string   `json:"date"`
}

// methodAuction 按 view 返回竞价快照/风向标/机会。
func (p *Plugin) methodAuction(ctx context.Context, payload json.RawMessage) (any, error) {
	var args auctionArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(args.View)) {
	case "", "snapshot":
		if len(args.Codes) == 0 {
			return nil, fmt.Errorf("snapshot 视图需要 codes")
		}
		rows, err := svc.FuyaoAuction(ctx, args.Codes, args.Stage)
		if err != nil {
			return p.run(err, nil, "集合竞价")
		}
		return map[string]any{"view": "snapshot", "count": len(rows), "items": rows, "asOf": p.asOf(),
			"note": "auction_pct/auction_turnover_pct 为百分数原值；auction_volume 单位股；auction_amount 单位元。"}, nil
	case "benchmark":
		items, err := svc.FuyaoAuctionBenchmark(ctx, args.Date)
		if err != nil {
			return p.run(err, nil, "竞价风向标")
		}
		return map[string]any{"view": "benchmark", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "opportunities":
		items, err := svc.FuyaoAuctionOpportunities(ctx, args.Date, nil, 20)
		if err != nil {
			return p.run(err, nil, "竞价机会")
		}
		return map[string]any{"view": "opportunities", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	}
	return nil, fmt.Errorf("未知 view %q（可用 snapshot/benchmark/opportunities）", args.View)
}

// toolAuction 是 fuyao.auction 工具入口：集合竞价快照/风向标/机会。
func (p *Plugin) toolAuction(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodAuction(ctx, payload)
}

// ---------- 资金流 ----------

type capitalFlowArgs struct {
	View     string `json:"view"`
	Code     string `json:"code"`
	Interval string `json:"interval"`
	Date     string `json:"date"`
	Start    string `json:"start"`
	End      string `json:"end"`
}

// methodCapitalFlow 按 view 返回资金流快照/历史/高频。
func (p *Plugin) methodCapitalFlow(ctx context.Context, payload json.RawMessage) (any, error) {
	var args capitalFlowArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Code) == "" {
		return nil, fmt.Errorf("code 不能为空")
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(args.View)) {
	case "", "snapshot":
		row, err := svc.FuyaoCapitalFlow(ctx, args.Code)
		if err != nil {
			return p.run(err, nil, "主力资金快照")
		}
		return map[string]any{"view": "snapshot", "item": row, "asOf": p.asOf(),
			"note": "金额单位元；main_net=超大单净额+大单净额；外部 Key 通常无权限（2004）。"}, nil
	case "history":
		start, _ := parseDate(args.Start)
		end, _ := parseDate(args.End)
		items, err := svc.FuyaoCapitalFlowHistory(ctx, args.Code, args.Interval, start, end)
		if err != nil {
			return p.run(err, nil, "主力资金历史")
		}
		return map[string]any{"view": "history", "count": len(items), "items": items, "asOf": p.asOf(),
			"note": "金额单位元；历史接口原始只给流入/流出，net_amount 由数据层计算。"}, nil
	case "highfreq":
		start, _ := parseDate(args.Start)
		end, _ := parseDate(args.End)
		items, err := svc.FuyaoHighFrequency(ctx, args.Code, args.Interval, start, end)
		if err != nil {
			return p.run(err, nil, "高频动向")
		}
		return map[string]any{"view": "highfreq", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "intraday":
		items, err := svc.FuyaoHighFrequencyIntraday(ctx, args.Code, args.Date)
		if err != nil {
			return p.run(err, nil, "高频分时")
		}
		return map[string]any{"view": "intraday", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	}
	return nil, fmt.Errorf("未知 view %q（可用 snapshot/history/highfreq/intraday）", args.View)
}

// toolCapitalFlow 是 fuyao.capitalFlow 工具入口：主力资金/高频（含 2004 降级）。
func (p *Plugin) toolCapitalFlow(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodCapitalFlow(ctx, payload)
}

// ---------- 财务 ----------

type financialsArgs struct {
	Kind   string   `json:"kind"`
	Code   string   `json:"code"`
	Codes  []string `json:"codes"`
	Period string   `json:"period"`
	Report string   `json:"report"`
	Limit  int      `json:"limit"`
}

// methodFinancials 按 kind 返回报表或财务指标。
func (p *Plugin) methodFinancials(ctx context.Context, payload json.RawMessage) (any, error) {
	var args financialsArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(args.Kind)) {
	case "", "income", "balance", "cash":
		if len(args.Codes) > 0 {
			items, err := svc.FuyaoFinancialsBatch(ctx, args.Kind, args.Codes, args.Period, args.Limit)
			if err != nil {
				return p.run(err, nil, "批量报表")
			}
			return map[string]any{"kind": args.Kind, "count": len(items), "items": items, "asOf": p.asOf(),
				"note": "报表金额单位元；批量每组含 thscode 与 item[]，不要跨公司混读。"}, nil
		}
		if strings.TrimSpace(args.Code) == "" {
			return nil, fmt.Errorf("单只报表需要 code，批量报表需要 codes")
		}
		items, err := svc.FuyaoFinancials(ctx, args.Kind, args.Code, args.Period, args.Limit)
		if err != nil {
			return p.run(err, nil, "财务报表")
		}
		return map[string]any{"kind": args.Kind, "count": len(items), "items": items, "asOf": p.asOf(),
			"note": "报表金额单位元；period=annual 仅年报、quarterly 为季度末。"}, nil
	case "indicators":
		if strings.TrimSpace(args.Code) == "" || strings.TrimSpace(args.Report) == "" {
			return nil, fmt.Errorf("indicators 需要 code 与 report（格式 yyyy-1..4）")
		}
		raw, err := svc.FuyaoFinancialIndicators(ctx, args.Code, args.Report)
		if err != nil {
			return p.run(err, nil, "财务指标")
		}
		return map[string]any{"kind": "indicators", "data": raw, "asOf": p.asOf(),
			"note": "value 为上游原始数值字符串；百分比类指标按百分数表达（89.12=89.12%）。"}, nil
	}
	return nil, fmt.Errorf("未知 kind %q（可用 income/balance/cash/indicators）", args.Kind)
}

// toolFinancials 是 fuyao.financials 工具入口：三大报表与财务指标。
func (p *Plugin) toolFinancials(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodFinancials(ctx, payload)
}

// ---------- 基础 ----------

type tickerSearchArgs struct {
	Keyword   string `json:"keyword"`
	AssetType string `json:"assetType"`
	Limit     int    `json:"limit"`
}

// methodTickerSearch 标的检索。
func (p *Plugin) methodTickerSearch(ctx context.Context, payload json.RawMessage) (any, error) {
	var args tickerSearchArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	rows, err := svc.FuyaoSearchTickers(ctx, args.Keyword, args.AssetType, args.Limit)
	if err != nil {
		return p.run(err, nil, "标的检索")
	}
	return map[string]any{"count": len(rows), "items": rows, "asOf": p.asOf()}, nil
}

// toolTickerSearch 是 fuyao.tickerSearch 工具入口：标的检索。
func (p *Plugin) toolTickerSearch(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodTickerSearch(ctx, payload)
}

type calendarArgs struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// methodCalendar 交易日历。
func (p *Plugin) methodCalendar(ctx context.Context, payload json.RawMessage) (any, error) {
	var args calendarArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	days, err := svc.FuyaoCalendar(ctx, args.Start, args.End)
	if err != nil {
		return p.run(err, nil, "交易日历")
	}
	return map[string]any{"count": len(days), "items": days, "asOf": p.asOf(), "note": "date 为 yyyyMMdd；date_ms 为当日零点毫秒戳。"}, nil
}

// toolCalendar 是 fuyao.calendar 工具入口：交易日历。
func (p *Plugin) toolCalendar(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodCalendar(ctx, payload)
}

// ---------- 指数 / 基金 / 期货 / 期权 ----------

type indexArgs struct {
	View      string   `json:"view"`
	Tag       string   `json:"tag"`
	IndexCode string   `json:"indexCode"`
	Codes     []string `json:"codes"`
	Limit     int      `json:"limit"`
}

// methodIndex 指数目录/成分股/反查/快照/K线。
func (p *Plugin) methodIndex(ctx context.Context, payload json.RawMessage) (any, error) {
	var args indexArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(args.View)) {
	case "", "catalog":
		items, err := svc.FuyaoIndexCatalog(ctx, args.Tag)
		if err != nil {
			return p.run(err, nil, "指数目录")
		}
		return map[string]any{"view": "catalog", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "constituents":
		items, err := svc.FuyaoIndexConstituents(ctx, args.IndexCode)
		if err != nil {
			return p.run(err, nil, "指数成分股")
		}
		return map[string]any{"view": "constituents", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "memberships":
		items, err := svc.FuyaoIndexMemberships(ctx, args.Codes, "")
		if err != nil {
			return p.run(err, nil, "板块反查")
		}
		return map[string]any{"view": "memberships", "count": len(items), "items": items, "asOf": p.asOf()}, nil
	case "snapshot":
		rows, err := svc.FuyaoIndexSnapshot(ctx, args.Codes)
		if err != nil {
			return p.run(err, nil, "指数快照")
		}
		return map[string]any{"view": "snapshot", "count": len(rows), "items": rows, "asOf": p.asOf(),
			"note": "volume 单位股、turnover 单位元（指数成交量口径与个股不同）。"}, nil
	case "kline":
		series, err := svc.FuyaoIndexKline(ctx, args.Codes, args.Limit)
		if err != nil {
			return p.run(err, nil, "指数K线")
		}
		return map[string]any{"view": "kline", "count": len(series), "series": series, "asOf": p.asOf()}, nil
	}
	return nil, fmt.Errorf("未知 view %q（可用 catalog/constituents/memberships/snapshot/kline）", args.View)
}

// toolIndex 是 fuyao.index 工具入口：指数目录/成分股/反查/快照/K线。
func (p *Plugin) toolIndex(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodIndex(ctx, payload)
}

type fundArgs struct {
	View      string            `json:"view"`
	Thscode   string            `json:"thscode"`
	ManagerID string            `json:"managerId"`
	CompanyID string            `json:"companyId"`
	Params    map[string]string `json:"params"`
	Limit     int               `json:"limit"`
}

// methodFund 基金视图查询。
func (p *Plugin) methodFund(ctx context.Context, payload json.RawMessage) (any, error) {
	var args fundArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	for k, v := range args.Params {
		params[k] = v
	}
	if args.Thscode != "" {
		params["thscode"] = args.Thscode
	}
	if args.ManagerID != "" {
		params["manager_id"] = args.ManagerID
	}
	if args.CompanyID != "" {
		params["company_id"] = args.CompanyID
	}
	data, err := svc.FuyaoFundView(ctx, args.View, params, args.Limit)
	if err != nil {
		return p.run(err, nil, "基金数据")
	}
	return p.dataReply(data), nil
}

// toolFund 是 fuyao.fund 工具入口：基金视图查询。
func (p *Plugin) toolFund(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodFund(ctx, payload)
}

type viewArgs struct {
	View   string            `json:"view"`
	Params map[string]string `json:"params"`
	Limit  int               `json:"limit"`
}

// methodFutures 期货视图查询。
func (p *Plugin) methodFutures(ctx context.Context, payload json.RawMessage) (any, error) {
	var args viewArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	data, err := svc.FuyaoFuturesView(ctx, args.View, args.Params, args.Limit)
	if err != nil {
		return p.run(err, nil, "期货数据")
	}
	return p.dataReply(data), nil
}

// toolFutures 是 fuyao.futures 工具入口：期货视图查询。
func (p *Plugin) toolFutures(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodFutures(ctx, payload)
}

// methodOptions 期权视图查询。
func (p *Plugin) methodOptions(ctx context.Context, payload json.RawMessage) (any, error) {
	var args viewArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	data, err := svc.FuyaoOptionsView(ctx, args.View, args.Params, args.Limit)
	if err != nil {
		return p.run(err, nil, "期权数据")
	}
	return p.dataReply(data), nil
}

// toolOptions 是 fuyao.options 工具入口：期权视图查询。
func (p *Plugin) toolOptions(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodOptions(ctx, payload)
}

// ---------- 资讯 / 新股 ----------

type newsArgs struct {
	View   string            `json:"view"`
	TagID  int               `json:"tagId"`
	Seq    string            `json:"seq"`
	Code   string            `json:"code"`
	Date   string            `json:"date"`
	Params map[string]string `json:"params"`
	Limit  int               `json:"limit"`
}

// methodNews 资讯视图查询。
func (p *Plugin) methodNews(ctx context.Context, payload json.RawMessage) (any, error) {
	var args newsArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	for k, v := range args.Params {
		params[k] = v
	}
	if args.TagID > 0 {
		params["tag_id"] = fmt.Sprintf("%d", args.TagID)
	}
	if args.Seq != "" {
		params["seq"] = args.Seq
	}
	if args.Code != "" {
		params["thscode"] = args.Code
	}
	if args.Date != "" {
		if strings.TrimSpace(params["begin_date"]) == "" {
			params["begin_date"] = args.Date
		}
		if strings.TrimSpace(params["end_date"]) == "" {
			params["end_date"] = args.Date
		}
		params["date"] = args.Date
	}
	data, err := svc.FuyaoNewsView(ctx, args.View, params, args.Limit)
	if err != nil {
		return p.run(err, nil, "资讯")
	}
	return p.dataReply(data), nil
}

// toolNews 是 fuyao.news 工具入口：资讯视图查询。
func (p *Plugin) toolNews(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodNews(ctx, payload)
}

type ipoArgs struct {
	View      string `json:"view"`
	Market    string `json:"market"`
	Status    string `json:"status"`
	IssueCode string `json:"issueCode"`
	Limit     int    `json:"limit"`
}

// methodIPO 打新视图查询。
func (p *Plugin) methodIPO(ctx context.Context, payload json.RawMessage) (any, error) {
	var args ipoArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	view := strings.ToLower(strings.TrimSpace(args.View))
	if view == "" {
		view = "a-ipo"
	}
	params := map[string]string{}
	switch view {
	case "a-ipo":
		params["market"] = args.Market
		params["status"] = args.Status
	case "hk-application":
	case "hk-calendar":
		params["status"] = args.Status
	case "hk-detail":
		params["issue_code"] = args.IssueCode
	default:
		return nil, fmt.Errorf("未知 view %q（可用 a-ipo/hk-application/hk-calendar/hk-detail）", view)
	}
	data, err := svc.FuyaoOtherView(ctx, view, params, args.Limit)
	if err != nil {
		return p.run(err, nil, "打新数据")
	}
	return p.dataReply(data), nil
}

// toolIPO 是 fuyao.ipo 工具入口：A 股/港股打新日历与详情。
func (p *Plugin) toolIPO(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodIPO(ctx, payload)
}

type marketDumpArgs struct {
	Kind string `json:"kind"`
}

// methodMarketDump 取全市场 Parquet 下载链接。
func (p *Plugin) methodMarketDump(ctx context.Context, payload json.RawMessage) (any, error) {
	var args marketDumpArgs
	if err := decodeArgs(payload, &args); err != nil {
		return nil, err
	}
	svc, err := p.data()
	if err != nil {
		return nil, err
	}
	raw, err := svc.FuyaoMarketDumpURL(ctx, args.Kind)
	if err != nil {
		return p.run(err, nil, "全市场导出")
	}
	return map[string]any{"kind": args.Kind, "data": raw, "asOf": p.asOf(),
		"note": "presigned_url 有时效；下载域名 o.thsi.cn 不在应用网络白名单内，请交给浏览器完成。"}, nil
}

// toolMarketDump 是 fuyao.marketDump 工具入口：全市场 Parquet 下载链接。
func (p *Plugin) toolMarketDump(ctx context.Context, payload json.RawMessage) (any, error) {
	return p.methodMarketDump(ctx, payload)
}
