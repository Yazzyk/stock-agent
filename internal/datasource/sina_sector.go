package datasource

// 新浪板块/概念的「目录」与「全市场快照」。
//
// 目录：Market_Center.getHQNodes 返回一棵节点树，首个元素是标题字符串，其后是子节点数组；
// 叶子节点是 [名称, 附加说明, category] 三元组数组。本应用只认两组：
//   - 行业：节点树里名为「新浪行业」的那一支（category 形如 new_dzxx）；
//   - 概念：节点树里名为「概念板块」的那一支（category 形如 gn_hwqc）。
//
// 为什么不用申万/证监会那几支：它们的 category 形如 sw1_770000 / hangye_ZA01，
// 新浪的资金流接口（ssl_bkzj_bk / ssl_bkzj_zjlrqs）只给 new_xxxx / gn_xxxx 这两套算资金流，
// 混用会出现「列表里能选、点进去没数据」。所以这里只暴露有资金流数据的这两组。
//
// 全市场快照：Market_Center.getHQNodeData?node=hs_a 分页返回（实测 56 页 x 100 条），
// 字段 symbol/code/name/trade/settlement/changepercent/amount。
// 涨跌家数就是在这份快照上统计，是本层唯一能拿到「全市场」口径的来源。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 新浪目录与快照的抓取参数。
const (
	// sinaCatalogIndustryNode / sinaCatalogConceptNode 是节点树里两支叶子的标题。
	sinaCatalogIndustryNode = "新浪行业"
	sinaCatalogConceptNode  = "概念板块"
	// sinaSnapshotPageSize 是全市场快照的每页条数（新浪上限 100）。
	sinaSnapshotPageSize = 100
	// sinaSnapshotMaxPages 是分页上限：实测 hs_a 共 56 页，留 80 页余量防止死循环。
	sinaSnapshotMaxPages = 80
	// sinaSnapshotWorkers 是并发抓页数：实测 8 路并发整轮约 1.3 秒。
	sinaSnapshotWorkers = 8
)

// sectorEntry 是板块目录里的一条。
type sectorEntry struct {
	Code string
	Name string
}

// sinaSectorCatalog 拉取并缓存新浪板块目录，按 Kind 分组返回。
// 缓存 5 分钟：目录是静态清单，只有新股上市才会变。
func (s *Service) sinaSectorCatalog(ctx context.Context) (map[Kind][]sectorEntry, error) {
	const cacheKey = "sina:sector:catalog"
	if cached, ok := s.cache.get(cacheKey); ok {
		if entries, isEntries := cached.(map[Kind][]sectorEntry); isEntries {
			return entries, nil
		}
	}
	body, err := s.client.get(ctx, request{
		source:  sourceSina,
		url:     sinaHQNodesURL(),
		referer: sinaFlowReferer,
	})
	if err != nil {
		return nil, err
	}
	entries, err := parseSinaHQNodes(body)
	if err != nil {
		return nil, err
	}
	s.cache.put(cacheKey, entries, ttlSinaFlow*5)
	// category -> 中文名也一起缓存起来：多日资金流只回 category，图例需要名称。
	for _, group := range entries {
		for _, entry := range group {
			s.cache.put(sinaNameKey(entry.Code), entry.Name, ttlSinaFlow*5)
		}
	}
	return entries, nil
}

// sinaHQNodesURL 是板块目录地址。
func sinaHQNodesURL() string {
	return "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHQNodes"
}

// parseSinaHQNodes 解析节点树，只取「新浪行业」与「概念板块」两支。
//
// 树的结构没有 schema：每个节点是数组，第 0 项是标题，其后可能是字符串（其它元信息）
// 或子节点数组。叶子是 [[名称, 说明, category], ...]，所以这里按「元素是长度 3 的纯字符串数组」
// 判定叶子，并对上层分支做递归。任何一层格式异常都只跳过该支，不整体失败。
func parseSinaHQNodes(body []byte) (map[Kind][]sectorEntry, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("datasource: 新浪板块目录格式异常: %w", err)
	}
	out := map[Kind][]sectorEntry{}
	walkSinaNodes(root, "", out)
	if len(out[KindSector]) == 0 && len(out[KindConcept]) == 0 {
		return nil, fmt.Errorf("%w: 新浪板块目录里没有行业/概念分组", ErrEmptyResponse)
	}
	for kind := range out {
		sort.SliceStable(out[kind], func(i, j int) bool { return out[kind][i].Code < out[kind][j].Code })
	}
	return out, nil
}

// walkSinaNodes 递归遍历节点树；title 是当前节点的标题。
func walkSinaNodes(node any, title string, out map[Kind][]sectorEntry) {
	list, ok := node.([]any)
	if !ok {
		return
	}
	if entries, isLeaf := sinaLeafEntries(list); isLeaf {
		switch title {
		case sinaCatalogIndustryNode:
			out[KindSector] = append(out[KindSector], entries...)
		case sinaCatalogConceptNode:
			out[KindConcept] = append(out[KindConcept], entries...)
		}
		return
	}
	// 非叶子：第 0 项「可能」是标题，其余可能是子节点数组或元信息字符串。
	// 只有第 0 项确实是字符串时才把它当标题并跳过；否则它是子节点，必须继续递归
	// （实测 A股 下面第一支「新浪行业」就在下标 0，无条件跳过会整支丢失）。
	nextTitle := title
	skip := -1
	if len(list) > 0 {
		if text, isText := list[0].(string); isText {
			nextTitle = strings.TrimSpace(text)
			skip = 0
		}
	}
	for i, child := range list {
		if i == skip {
			continue
		}
		walkSinaNodes(child, nextTitle, out)
	}
}

// sinaLeafEntries 判断一组元素是否是 [名称, 说明, category] 三元组数组，并转成条目。
func sinaLeafEntries(list []any) ([]sectorEntry, bool) {
	if len(list) == 0 {
		return nil, false
	}
	entries := make([]sectorEntry, 0, len(list))
	for _, item := range list {
		triple, ok := item.([]any)
		if !ok || len(triple) < 3 {
			return nil, false
		}
		name, nameOK := triple[0].(string)
		code, codeOK := triple[2].(string)
		if !nameOK || !codeOK {
			return nil, false
		}
		name = strings.TrimSpace(name)
		code = strings.TrimSpace(code)
		if name == "" || code == "" {
			return nil, false
		}
		entries = append(entries, sectorEntry{Code: code, Name: name})
	}
	return entries, true
}

// ---------- 全市场快照 ----------

// marketSnapshotRow 是全市场快照的一行（只保留统计需要的字段）。
type marketSnapshotRow struct {
	Code      string
	Name      string
	Price     float64
	PrevClose float64
	ChangePct float64
	Amount    float64
}

// marketSnapshot 是一次全市场扫描的结果。
type marketSnapshot struct {
	Rows []marketSnapshotRow
	// FailedPages 是重试后仍然失败的分页，用于让调用方判断口径是否完整。
	FailedPages []int
}

// sinaHQNodeDataURL 是全市场快照的单页地址（node=hs_a 是沪深 A 股）。
func sinaHQNodeDataURL(page int) string {
	query := url.Values{}
	query.Set("page", strconv.Itoa(page))
	query.Set("num", strconv.Itoa(sinaSnapshotPageSize))
	query.Set("sort", "symbol")
	query.Set("asc", "1")
	query.Set("node", "hs_a")
	return "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHQNodeData?" + query.Encode()
}

// fetchMarketSnapshot 并发扫描新浪全市场快照。
//
// 策略：先抓第 1 页，满页才继续并发抓后续页（页数不固定：新股多时会上浮）。
// 单页失败重试 1 次（新浪偶发限流返回空数组），仍失败的页记进 FailedPages 而不是整轮失败 ——
// 涨跌家数少算几十只比整块不可用更有用，调用方据此决定是否降级。
func (s *Service) fetchMarketSnapshot(ctx context.Context) (marketSnapshot, error) {
	first, err := s.fetchSnapshotPage(ctx, 1)
	if err != nil {
		return marketSnapshot{}, err
	}
	if len(first) == 0 {
		return marketSnapshot{}, fmt.Errorf("%w: 新浪全市场快照第 1 页为空", ErrEmptyResponse)
	}
	out := marketSnapshot{Rows: append([]marketSnapshotRow(nil), first...)}
	if len(first) < sinaSnapshotPageSize {
		return out, nil
	}

	pages := make([][]marketSnapshotRow, sinaSnapshotMaxPages+1)
	var (
		mu    sync.Mutex
		fails []int
		wg    sync.WaitGroup
	)
	sem := make(chan struct{}, sinaSnapshotWorkers)
	for page := 2; page <= sinaSnapshotMaxPages; page++ {
		wg.Add(1)
		go func(page int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, err := s.fetchSnapshotPage(ctx, page)
			if err != nil || len(rows) == 0 {
				mu.Lock()
				fails = append(fails, page)
				mu.Unlock()
				return
			}
			pages[page] = rows
		}(page)
	}
	wg.Wait()

	// 只有「最后一页不满」才算真正到底：之后那些页必然为空，不能当成失败。
	lastFull := sinaSnapshotMaxPages
	for page := 2; page <= sinaSnapshotMaxPages; page++ {
		if len(pages[page]) > 0 && len(pages[page]) < sinaSnapshotPageSize {
			lastFull = page
			break
		}
	}
	for page := 2; page <= lastFull; page++ {
		if len(pages[page]) == 0 {
			continue
		}
		out.Rows = append(out.Rows, pages[page]...)
	}
	for _, page := range fails {
		if page <= lastFull {
			out.FailedPages = append(out.FailedPages, page)
		}
	}
	sort.Ints(out.FailedPages)
	return out, nil
}

// fetchSnapshotPage 抓一页并解析；失败重试 1 次。
func (s *Service) fetchSnapshotPage(ctx context.Context, page int) ([]marketSnapshotRow, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		body, err := s.client.get(ctx, request{
			source:  sourceSina,
			url:     sinaHQNodeDataURL(page),
			referer: sinaFlowReferer,
			gbk:     true,
		})
		if err != nil {
			lastErr = err
			continue
		}
		rows, err := parseSinaHQNodeData(body)
		if err != nil {
			lastErr = err
			continue
		}
		return rows, nil
	}
	return nil, lastErr
}

// parseSinaHQNodeData 解析全市场快照的一页。
// 停牌股的 trade 是 0、changepercent 是 0，用 settlement（昨收）非 0 判定为可用行。
func parseSinaHQNodeData(body []byte) ([]marketSnapshotRow, error) {
	payload := strings.TrimSpace(string(body))
	if payload == "" || payload == "null" {
		return nil, ErrEmptyResponse
	}
	// 字段形态不稳定：trade/settlement/changepercent/amount 在不同页里会混用
	// 数字与字符串（实测 "trade":"11.270" 与 pricechange:-0.03 同时出现），
	// 所以一律先收到 any 再用 anyNum 归一，避免一行脏字段导致整页失败。
	var raw []map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil, fmt.Errorf("datasource: 新浪全市场快照格式异常: %w", err)
	}
	rows := make([]marketSnapshotRow, 0, len(raw))
	for _, item := range raw {
		code := strings.TrimSpace(anyStr(item["code"]))
		if code == "" {
			continue
		}
		rows = append(rows, marketSnapshotRow{
			Code:      code,
			Name:      strings.TrimSpace(anyStr(item["name"])),
			Price:     anyNum(item["trade"]),
			PrevClose: anyNum(item["settlement"]),
			ChangePct: anyNum(item["changepercent"]),
			Amount:    anyNum(item["amount"]),
		})
	}
	return rows, nil
}
