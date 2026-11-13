package contract

import (
	"context"
	"errors"
	"time"
)

// 本文件定义同花顺官方数据 API（fuyao.aicubes.cn）的通用查询契约。
//
// 设计取舍：
//   - 官方 124 个端点的返回结构差异极大（有的 data.item、有的 data.items、有的 data 直接是数组），
//     强行为每个端点写一套 DTO 会让数据层变成几百个几乎相同的结构体。
//     因此通用层用 []map[string]any 承载原始字段（json 标签与官方逐字一致），
//     常用域再提供带类型的便捷方法（见 FuyaoQuoteRow / FuyaoLimitRow / FuyaoHotRow …）。
//   - 单位口径不在这里猜：成交量官方是「股」、金额是「元」、涨跌幅是百分数原值；
//     数据层转换后会在字段注释与 docs/FUYAO-API.md 里写明，前端不得二次换手/换元。

// ErrFuyaoPermission 表示该能力需要同花顺 AI 客户端（官方 code=2003/2004），
// 外部 API Key 无法访问。调用方应展示说明并降级，而不是当成网络故障反复重试。
var ErrFuyaoPermission = errors.New("contract: 官方数据能力需要更高权限")

// ErrFuyaoUnsupported 表示目录里没有该端点，或该端点不适用于当前标的类型。
var ErrFuyaoUnsupported = errors.New("contract: 官方数据能力不存在或不适用")

// FuyaoParam 是一个查询参数的目录描述（用于工具 schema 与参数校验）。
type FuyaoParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
	Desc     string `json:"desc,omitempty"`
}

// FuyaoEndpoint 是官方 API 单个端点的元信息。
// Permission: public=外部 Key 可用；client=同花顺 AI 客户端专用（外部 Key 返回 2004）。
type FuyaoEndpoint struct {
	ID         string       `json:"id"`
	Path       string       `json:"path"`
	Title      string       `json:"title"`
	Group      string       `json:"group"`
	Params     []FuyaoParam `json:"params,omitempty"`
	Limit      int          `json:"limit,omitempty"` // 单次代码/记录上限，0 表示无固定上限
	Permission string       `json:"permission,omitempty"`
	Notes      string       `json:"notes,omitempty"`
}

// FuyaoQuery 是通用查询入参。Params 的键必须出现在端点目录的 Params 里
// （大小写敏感），数据层会拒绝未知参数，避免把参数名写错当成「无数据」。
type FuyaoQuery struct {
	Endpoint string            `json:"endpoint"`
	Params   map[string]string `json:"params,omitempty"`
	// Limit 是**输出裁剪**条数（不是上游分页大小）；0 使用端点默认值。
	Limit int `json:"limit,omitempty"`
}

// FuyaoData 是通用查询结果：Items 为裁剪后的记录，Raw 保留对象型 data
// （例如资金流快照、基金持仓汇总），Tool 输出时二者只取其一。
type FuyaoData struct {
	Endpoint  string            `json:"endpoint"`
	Path      string            `json:"path"`
	Title     string            `json:"title"`
	Group     string            `json:"group"`
	Timestamp int64             `json:"timestamp,omitempty"` // 数据就绪时间（毫秒，Asia/Shanghai）
	Count     int               `json:"count"`
	Items     []map[string]any  `json:"items,omitempty"`
	Raw       map[string]any    `json:"raw,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	// Permission 非空表示端点被权限拦截（client-only），Items 为空且 Note 给出解释。
	Permission string `json:"permission,omitempty"`
	Note       string `json:"note,omitempty"`
}

// FuyaoQuoteRow 是官方 A 股/指数行情快照的一行（字段与官方逐字一致，便于核对）。
// 单位：volume=股（数据层对外 Quote 会换算成手）；turnover=元；price_change_ratio_pct 是百分数原值。
type FuyaoQuoteRow struct {
	Thscode   string  `json:"thscode"`
	Ticker    string  `json:"ticker"`
	Name      string  `json:"name,omitempty"`
	LastPrice float64 `json:"last_price"`
	Change    float64 `json:"price_change"`
	ChangePct float64 `json:"price_change_ratio_pct"`
	Open      float64 `json:"open_price"`
	High      float64 `json:"high_price"`
	Low       float64 `json:"low_price"`
	PrevClose float64 `json:"prev_price"`
	Volume    float64 `json:"volume"`   // 股
	Turnover  float64 `json:"turnover"` // 元
	Timestamp int64   `json:"timestamp,omitempty"`
	Source    string  `json:"source,omitempty"`
}

// FuyaoLimitRow 是涨停/跌停/炸板池的一行。
// 单位：last_price=元；price_change_ratio_pct 百分数；seal_money/max_seal_money=元；
// open_times 为开板次数（官方可能为 null）。
type FuyaoLimitRow struct {
	Thscode     string  `json:"thscode"`
	Ticker      string  `json:"ticker"`
	Name        string  `json:"name"`
	IsST        bool    `json:"is_st,omitempty"`
	IsNew       bool    `json:"is_new,omitempty"`
	LastPrice   float64 `json:"last_price"`
	ChangePct   float64 `json:"price_change_ratio_pct"`
	LimitUpTime string  `json:"limit_up_time,omitempty"`
	FirstTime   string  `json:"first_limit_time,omitempty"`
	LastTime    string  `json:"last_limit_time,omitempty"`
	OpenTimes   *int    `json:"open_times,omitempty"`
	Reason      string  `json:"limit_up_reason,omitempty"`
	BoardsText  string  `json:"continue_day_text,omitempty"`
	Boards      int     `json:"continue_day_cnt,omitempty"`
	SealMoney   float64 `json:"seal_money,omitempty"`
	MaxSeal     float64 `json:"max_seal_money,omitempty"`
	TurnoverPct float64 `json:"turnover_ratio_pct,omitempty"`
}

// FuyaoHotRow 是热榜（飙升榜 / 热股榜 / 历史热股榜）的一行。
// heat 是上游原始字符串（可能是 102199.2 或整数串），不要当金额。
type FuyaoHotRow struct {
	Thscode    string         `json:"thscode"`
	Ticker     string         `json:"ticker"`
	Name       string         `json:"name"`
	Rank       int            `json:"rank"`
	Heat       string         `json:"heat,omitempty"`
	RankChange *int           `json:"rank_change,omitempty"`
	RankTrend  string         `json:"rank_trend,omitempty"`
	Date       string         `json:"date,omitempty"`
	DateMS     int64          `json:"date_ms,omitempty"`
	Extra      map[string]any `json:"-"`
}

// FuyaoFlowSide 是资金流单侧（流入/流出/净额），单位：元。
type FuyaoFlowSide struct {
	Inflow  float64 `json:"inflow_amount"`
	Outflow float64 `json:"outflow_amount"`
	Net     float64 `json:"net_amount,omitempty"`
}

// FuyaoCapitalFlowRow 是资金流实时快照的规范化结果（单位：元）。
type FuyaoCapitalFlowRow struct {
	Thscode   string        `json:"thscode"`
	Timestamp int64         `json:"timestamp,omitempty"`
	Super     FuyaoFlowSide `json:"super_large"`
	Large     FuyaoFlowSide `json:"large"`
	Medium    FuyaoFlowSide `json:"medium"`
	Small     FuyaoFlowSide `json:"small"`
	// MainNet = 超大单净额 + 大单净额（A 股「主力」常用口径），单位元。
	MainNet float64 `json:"main_net"`
	Source  string  `json:"source"`
}

// FuyaoAuctionRow 是集合竞价快照的一行。
// auction_pct/auction_turnover_pct/auction_yesterday_ratio_pct 均为百分数原值；
// auction_volume=股；auction_amount/float_market_cap=元。
type FuyaoAuctionRow struct {
	Thscode          string  `json:"thscode"`
	Ticker           string  `json:"ticker"`
	Name             string  `json:"name"`
	AuctionPrice     float64 `json:"auction_price"`
	AuctionPct       float64 `json:"auction_pct"`
	AuctionVolume    float64 `json:"auction_volume"`
	AuctionAmount    float64 `json:"auction_amount"`
	AuctionUnmatched float64 `json:"auction_unmatched"`
	TurnoverPct      float64 `json:"auction_turnover_pct"`
	YesterdayRatio   float64 `json:"auction_yesterday_ratio_pct"`
	VolumeRatio      float64 `json:"auction_volume_ratio"`
	PrevClose        float64 `json:"pre_close_price"`
	Open             float64 `json:"open_price"`
	LastPrice        float64 `json:"last_price"`
	FloatMarketCap   float64 `json:"float_market_cap"`
}

// FuyaoTickerRef 是标的检索/列表的结果（asset_type 见官方枚举）。
type FuyaoTickerRef struct {
	Thscode   string `json:"thscode"`
	Ticker    string `json:"ticker"`
	Name      string `json:"name"`
	Exchange  string `json:"exchange,omitempty"`
	AssetType string `json:"asset_type,omitempty"`
}

// FuyaoCalendarDay 是交易日历的一天。
type FuyaoCalendarDay struct {
	DateMS int64  `json:"date_ms"`
	Date   string `json:"date"` // yyyyMMdd
}

// FuyaoBarRow 是官方历史 K 线的一根。
// 单位：volume=股（批量工具输出保留官方口径，前端画图需要手时自行 /100）；turnover=元。
type FuyaoBarRow struct {
	DateMS int64   `json:"date_ms"`
	Date   string  `json:"date"` // Asia/Shanghai 的 YYYY-MM-DD
	Open   float64 `json:"open_price"`
	High   float64 `json:"high_price"`
	Low    float64 `json:"low_price"`
	Close  float64 `json:"close_price"`
	Volume float64 `json:"volume"`
	Turn   float64 `json:"turnover"`
}

// FuyaoBarsSeries 是一只标的的历史 K 线序列。
type FuyaoBarsSeries struct {
	Thscode   string        `json:"thscode"`
	Interval  string        `json:"interval,omitempty"`
	Adjust    string        `json:"adjust,omitempty"`
	Timestamp int64         `json:"timestamp,omitempty"`
	Bars      []FuyaoBarRow `json:"bars"`
}

// FuyaoNewsRow 是快讯/资讯/公告的通用行（字段随端点变化，保留原始 map 以免丢字段）。
type FuyaoNewsRow struct {
	ID          string         `json:"id,omitempty"`
	Title       string         `json:"title,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Content     string         `json:"content,omitempty"`
	Source      string         `json:"source,omitempty"`
	PublishTime int64          `json:"publish_time,omitempty"`
	Thscode     string         `json:"thscode,omitempty"`
	URL         string         `json:"url,omitempty"`
	Extra       map[string]any `json:"-"`
}

// FuyaoService 是数据层实现的可选官方数据能力。
// 用可选接口而不是扩大 DataService，避免所有 stub 连锁改动（与 FuyaoAdmin 同一约定）。
//
// 端点长尾统一走 FuyaoQuery；以下强类型方法是「前端/工具最常用视图」的稳定契约，
// 参数校验、权限降级（2004）与单位换算都在数据层完成。
type FuyaoService interface {
	// FuyaoCatalog 返回官方端点目录（离线常量，不发网络请求）。
	FuyaoCatalog(ctx context.Context) []FuyaoEndpoint
	// FuyaoQuery 执行一次通用查询；endpoint 取目录 ID，params 按目录校验。
	FuyaoQuery(ctx context.Context, query FuyaoQuery) (FuyaoData, error)

	// FuyaoQuoteRows 批量取官方行情快照（>50 只自动分批，带短 TTL 缓存）。
	FuyaoQuoteRows(ctx context.Context, codes []string) ([]FuyaoQuoteRow, error)
	// FuyaoValuationRows 批量取官方估值快照（PE/PB/PS/PCF）。
	FuyaoValuationRows(ctx context.Context, codes []string) ([]map[string]any, error)
	// FuyaoKline 批量历史 K 线（asset=stock/index/fund，最多 10 个/批）。
	FuyaoKline(ctx context.Context, codes []string, interval string, start, end time.Time, adjust, asset string, limit int) ([]FuyaoBarsSeries, error)
	// FuyaoIndexKline 指数历史日线（内部转调 FuyaoKline）。
	FuyaoIndexKline(ctx context.Context, codes []string, limit int) ([]FuyaoBarsSeries, error)

	// FuyaoLimitRows 取官方涨停/跌停/炸板池（pool=up/down/break）。
	FuyaoLimitRows(ctx context.Context, pool string, limit int) ([]FuyaoLimitRow, error)
	// FuyaoLimitStatistics 涨停强度与晋级统计。
	FuyaoLimitStatistics(ctx context.Context, date string) (map[string]any, error)
	// FuyaoHotRows 取官方热榜（kind=skyrocket/hot/history；period=day/hour）。
	FuyaoHotRows(ctx context.Context, kind, period string, limit int) ([]FuyaoHotRow, error)
	// FuyaoAnomalyList 个股异动原因列表。
	FuyaoAnomalyList(ctx context.Context, tagCodes []string, limit int) ([]map[string]any, error)
	// FuyaoAnomalyByStock 按股票批量查异动原因。
	FuyaoAnomalyByStock(ctx context.Context, codes []string) ([]map[string]any, error)
	// FuyaoAbnormalMonitor 严重异动与重点监控记录。
	FuyaoAbnormalMonitor(ctx context.Context, state string, limit int) ([]map[string]any, error)
	// FuyaoDragonTiger 龙虎榜榜单（含机构/游资）。
	FuyaoDragonTiger(ctx context.Context, boardType, date string) (map[string]any, error)
	// FuyaoDragonTigerInstitution 个股龙虎榜机构交易详情。
	FuyaoDragonTigerInstitution(ctx context.Context, code, date string, rangeDays int) ([]map[string]any, error)
	// FuyaoDepartmentClassicRecord 营业部经典战绩。
	FuyaoDepartmentClassicRecord(ctx context.Context, departmentID string) (map[string]any, error)
	// FuyaoDepartmentHistory 营业部历史上榜记录。
	FuyaoDepartmentHistory(ctx context.Context, departmentID string, limit int) ([]map[string]any, error)
	// FuyaoChipShape 筹码形态股票筛选（客户端专用）。
	FuyaoChipShape(ctx context.Context, shape, date string, limit int) ([]map[string]any, error)
	// FuyaoNewConcepts 同花顺新增概念（客户端专用）。
	FuyaoNewConcepts(ctx context.Context, limit int) ([]map[string]any, error)
	// FuyaoTurnoverPrediction 市场成交额预测（客户端专用）。
	FuyaoTurnoverPrediction(ctx context.Context) (map[string]any, error)

	// FuyaoCapitalFlow 取单只个股资金流快照（可能返回 ErrFuyaoPermission）。
	FuyaoCapitalFlow(ctx context.Context, code string) (FuyaoCapitalFlowRow, error)
	// FuyaoCapitalFlowHistory 资金流历史（1m/1d）。
	FuyaoCapitalFlowHistory(ctx context.Context, code, interval string, start, end time.Time) ([]map[string]any, error)
	// FuyaoHighFrequency 高频历史（个股/指数）。
	FuyaoHighFrequency(ctx context.Context, code, interval string, start, end time.Time) ([]map[string]any, error)
	// FuyaoHighFrequencyIntraday 单日高频分时。
	FuyaoHighFrequencyIntraday(ctx context.Context, code, date string) ([]map[string]any, error)

	// FuyaoAuction 取集合竞价快照（stage=live/final）。
	FuyaoAuction(ctx context.Context, codes []string, stage string) ([]FuyaoAuctionRow, error)
	// FuyaoAuctionBenchmark 短线风向标竞价基准。
	FuyaoAuctionBenchmark(ctx context.Context, date string) ([]map[string]any, error)
	// FuyaoAuctionOpportunities 集合竞价机会筛选。
	FuyaoAuctionOpportunities(ctx context.Context, date string, warnTypes []string, limit int) ([]map[string]any, error)

	// FuyaoFinancials 单只 A 股三大报表之一。
	FuyaoFinancials(ctx context.Context, kind, code, period string, limit int) ([]map[string]any, error)
	// FuyaoFinancialsBatch 批量三大报表（≤10/批）。
	FuyaoFinancialsBatch(ctx context.Context, kind string, codes []string, period string, limit int) ([]map[string]any, error)
	// FuyaoFinancialIndicators 五类财务指标（report=yyyy-1..4）。
	FuyaoFinancialIndicators(ctx context.Context, code, report string) (map[string]any, error)

	// FuyaoSearchTickers 标的检索。
	FuyaoSearchTickers(ctx context.Context, keyword, assetType string, limit int) ([]FuyaoTickerRef, error)
	// FuyaoCalendar A 股交易日历。
	FuyaoCalendar(ctx context.Context, startDate, endDate string) ([]FuyaoCalendarDay, error)

	// FuyaoIndexCatalog 同花顺指数目录。
	FuyaoIndexCatalog(ctx context.Context, tag string) ([]map[string]any, error)
	// FuyaoIndexConstituents 指数成分股。
	FuyaoIndexConstituents(ctx context.Context, indexCode string) ([]map[string]any, error)
	// FuyaoIndexMemberships 个股所属板块反查。
	FuyaoIndexMemberships(ctx context.Context, codes []string, typ string) ([]map[string]any, error)
	// FuyaoIndexSnapshot 指数行情快照。
	FuyaoIndexSnapshot(ctx context.Context, codes []string) ([]FuyaoQuoteRow, error)

	// FuyaoFundView 基金视图查询（view 白名单见数据层）。
	FuyaoFundView(ctx context.Context, view string, params map[string]string, limit int) (FuyaoData, error)
	// FuyaoFuturesView 期货视图查询。
	FuyaoFuturesView(ctx context.Context, view string, params map[string]string, limit int) (FuyaoData, error)
	// FuyaoOptionsView 期权视图查询。
	FuyaoOptionsView(ctx context.Context, view string, params map[string]string, limit int) (FuyaoData, error)
	// FuyaoNewsView 资讯视图查询。
	FuyaoNewsView(ctx context.Context, view string, params map[string]string, limit int) (FuyaoData, error)
	// FuyaoOtherView 打新/ETF 申赎/全市场导出等视图。
	FuyaoOtherView(ctx context.Context, view string, params map[string]string, limit int) (FuyaoData, error)
	// FuyaoMarketDumpURL 全市场 Parquet 下载链接。
	FuyaoMarketDumpURL(ctx context.Context, kind string) (map[string]any, error)
}
