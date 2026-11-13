package datasource

// 同花顺官方金融数据 API（fuyao.aicubes.cn）客户端。
//
// 定位：**官方 API 优先 + 现有链路兜底**。
//   - 快照：官方 /api/a-share/prices/snapshot 批量取数（一次最多 50 个代码）；
//   - K 线：官方 /api/a-share/prices/historical（日/周/月，支持 none/forward/backward）；
//   - 涨停池：官方 /api/a-share/special-data/limit-up-pool（含连板数、封单额、涨停原因）；
//   - 失败（无 Key / 限流 / 上游错误）时静默降级到原有同花顺网页、腾讯、新浪链路。
//
// 口径约定：
//   - 官方快照 volume 单位是「股」，对外统一换算成「手」（除以 100），与其它源一致；
//   - 官方快照不给名称，名称由腾讯/新浪快照或本地缓存补；
//   - 强约束与错误码见 https://fuyao.aicubes.cn/docs/api-reference/overview/
//     （code=0 成功；2001 未认证；2003 无权限；4001 限流；其余按可重试/不可重试处理）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

const (
	// sourceFuyao 是官方 API 的来源名（Health 与 Source 字段会展示）。
	sourceFuyao = "fuyao"
	// fuyaoDefaultBase 是官方 API 的 Base URL。
	fuyaoDefaultBase = "https://fuyao.aicubes.cn"
	// fuyaoKeyFile 是 API Key 的落盘文件名（0600，放在数据目录下，与 ths_cookie.txt 同口径）。
	fuyaoKeyFile = "fuyao.key"
	// fuyaoSettingsKey 是 settings 桶里保存非敏感配置（baseUrl / enabled）的键。
	fuyaoSettingsKey = "fuyao"
	// fuyaoKeyEnv 是 API Key 的环境变量覆盖（优先级最高，便于临时排障）。
	fuyaoKeyEnv = "GUANLAN_FUYAO_API_KEY"
	// fuyaoBatchMax 是单次快照请求的代码上限（官方未写死，留出保守余量）。
	fuyaoBatchMax = 50
	// fuyaoPoolPageSize 是涨停池单页条数（官方上限 200）。
	fuyaoPoolPageSize = 200
	// fuyaoConfigTTL 是配置缓存时间：改 Key 后最多 30 秒生效。
	fuyaoConfigTTL = 30 * time.Second
	// ttlFuyaoBars 是官方 K 线的缓存时间（比网页源略长，日线一天只变一次）。
	ttlFuyaoBars = 5 * time.Minute
	// fuyaoRetryDelay 是官方 code=4001（限流）后的唯一一次退避重试间隔。
	fuyaoRetryDelay = 300 * time.Millisecond
)

// fuyaoAPIError 是官方业务错误码的强类型载体。
// 有了它，上层可以用 errors.Is(err, contract.ErrFuyaoPermission) 判断「权限不够」
// 而不是靠字符串匹配文案（文案会变，错误码不会）。
type fuyaoAPIError struct {
	Code    int
	Path    string
	Message string
}

func (e *fuyaoAPIError) Error() string {
	return fmt.Sprintf("datasource: 官方 API %s 返回 code=%d: %s", e.Path, e.Code, fuyaoMessage(e.Code, e.Message))
}

// Is 让 2003/2004 都归一到 contract.ErrFuyaoPermission，5002/5003 归一到可重试语义。
func (e *fuyaoAPIError) Is(target error) bool {
	switch target {
	case contract.ErrFuyaoPermission:
		return e.Code == 2003 || e.Code == 2004
	}
	return false
}

// fuyaoSettings 是落盘的官方 API 非敏感配置。
type fuyaoSettings struct {
	BaseURL string `json:"baseUrl,omitempty"`
	// Disabled=true 时即使有 Key 也不走官方 API（用户可手动关闭）。
	Disabled bool `json:"disabled,omitempty"`
}

// fuyaoConfig 是生效中的官方 API 配置。
type fuyaoConfig struct {
	APIKey  string
	BaseURL string
	Enabled bool
	// Origin 说明 Key 从哪来（env / file），用于设置页展示。
	Origin string
}

// fuyaoEnvelope 是官方统一响应信封。
type fuyaoEnvelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
}

// fuyaoState 是进程内的配置缓存（避免每次取数都读盘）。
type fuyaoState struct {
	mu   sync.Mutex
	cfg  fuyaoConfig
	at   time.Time
	last string
}

// fuyaoConfigNow 读取当前生效的配置（带 30 秒缓存）。
func (s *Service) fuyaoConfigNow() fuyaoConfig {
	s.fy.mu.Lock()
	defer s.fy.mu.Unlock()
	if !s.fy.at.IsZero() && s.now().Sub(s.fy.at) < fuyaoConfigTTL {
		return s.fy.cfg
	}
	cfg := fuyaoConfig{BaseURL: fuyaoDefaultBase, Enabled: true}
	if key := strings.TrimSpace(os.Getenv(fuyaoKeyEnv)); key != "" {
		cfg.APIKey = key
		cfg.Origin = "env"
	}
	// 文件里的 Key 优先级低于环境变量（环境变量用于临时排障）。
	if cfg.APIKey == "" {
		if key := s.readFuyaoKey(); key != "" {
			cfg.APIKey = key
			cfg.Origin = "file"
		}
	}
	var settings fuyaoSettings
	if s.cfg.Store != nil {
		if raw, err := s.cfg.Store.Get("settings", fuyaoSettingsKey); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &settings)
		}
	}
	if base := strings.TrimSpace(settings.BaseURL); base != "" {
		cfg.BaseURL = strings.TrimRight(base, "/")
	}
	if settings.Disabled {
		cfg.Enabled = false
	}
	cfg.Enabled = cfg.Enabled && cfg.APIKey != ""
	s.fy.cfg = cfg
	s.fy.at = s.now()
	return cfg
}

// invalidateFuyaoConfig 清空配置缓存（保存 Key / 开关后立即生效）。
func (s *Service) invalidateFuyaoConfig() {
	s.fy.mu.Lock()
	s.fy.at = time.Time{}
	s.fy.mu.Unlock()
}

// fuyaoKeyPath 返回 API Key 的落盘路径。
func (s *Service) fuyaoKeyPath() string {
	if strings.TrimSpace(s.dir) == "" {
		return ""
	}
	return filepath.Join(s.dir, fuyaoKeyFile)
}

// readFuyaoKey 读落盘 Key（0600 文件，去掉首尾空白与行尾）。
func (s *Service) readFuyaoKey() string {
	path := s.fuyaoKeyPath()
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// writeFuyaoKey 写落盘 Key（0600；空串表示删除）。
func (s *Service) writeFuyaoKey(key string) error {
	path := s.fuyaoKeyPath()
	if path == "" {
		return fmt.Errorf("datasource: 数据目录未配置，无法保存官方 API Key")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("datasource: 删除官方 API Key 失败: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("datasource: 创建数据目录失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return fmt.Errorf("datasource: 写入官方 API Key 失败: %w", err)
	}
	return os.Chmod(path, 0o600)
}

// fuyaoAvailable 判断官方 API 是否可用（启用且有 Key）。
func (s *Service) fuyaoAvailable() bool {
	cfg := s.fuyaoConfigNow()
	return cfg.Enabled && cfg.APIKey != ""
}

// fuyaoGet 发一次官方 API 请求并解包信封；业务错误码统一转成中文错误。
// retryable 只影响 client 内部重试：限流（4001/429）与 5xx 值得退避重试。
func (s *Service) fuyaoGet(ctx context.Context, path string, query url.Values, out any) error {
	cfg := s.fuyaoConfigNow()
	if !cfg.Enabled || cfg.APIKey == "" {
		return fmt.Errorf("datasource: 官方 API 未启用（未配置 API Key）")
	}
	endpoint := cfg.BaseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	body, err := s.client.get(ctx, request{
		source:  sourceFuyao,
		url:     endpoint,
		headers: map[string]string{"X-api-key": cfg.APIKey, "Accept": "application/json"},
	})
	if err != nil {
		return err
	}
	// 官方限流以 HTTP 200 + code=4001 表达（也可能直接 429，client 已重试）。
	// 这里只再退避重试一次：成功就正常返回，仍失败才交给降级链。
	for attempt := 0; ; attempt++ {
		var env fuyaoEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return fmt.Errorf("datasource: 官方 API 响应不是合法 JSON: %w", err)
		}
		if env.Code == 0 {
			if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
				return ErrNoData
			}
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("datasource: 官方 API 数据格式异常: %w", err)
			}
			return nil
		}
		if env.Code == 4001 && attempt == 0 {
			s.log.Debug("官方 API 限流，退避后重试一次", "path", path)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(fuyaoRetryDelay):
			}
			body, err = s.client.get(ctx, request{
				source:  sourceFuyao,
				url:     endpoint,
				headers: map[string]string{"X-api-key": cfg.APIKey, "Accept": "application/json"},
			})
			if err != nil {
				return err
			}
			continue
		}
		return &fuyaoAPIError{Code: env.Code, Path: path, Message: env.Message}
	}
}

// fuyaoMessage 把官方错误码翻成可操作的中文说明。
func fuyaoMessage(code int, message string) string {
	detail := strings.TrimSpace(message)
	switch code {
	case 1001:
		return "缺少必填参数（" + detail + "）"
	case 1002:
		return "参数格式错误（" + detail + "）"
	case 1003:
		return "参数越界（" + detail + "）"
	case 1004:
		return "参数冲突（" + detail + "）"
	case 2001:
		return "API Key 缺失或无效，请在设置中心重新填写"
	case 2003:
		return "当前 API Key 无权访问该数据能力"
	case 2004:
		return "该数据为同花顺 AI 客户端专用，外部 API Key 无法访问"
	case 3001:
		return "标的不存在"
	case 3002:
		return "数据尚未就绪"
	case 3004:
		return "该标的类型不支持此数据能力"
	case 4001:
		return "触发官方限流，请稍后再试"
	case 5001:
		return "官方服务内部错误"
	case 5002:
		return "官方上游超时"
	case 5003:
		return "官方上游数据源不可用"
	}
	if detail == "" {
		detail = "未知错误"
	}
	return detail
}

// ---------- 历史 K 线 ----------

// fuyaoBarItem 是官方历史 K 线的一根。
type fuyaoBarItem struct {
	DateMS int64   `json:"date_ms"`
	Open   float64 `json:"open_price"`
	High   float64 `json:"high_price"`
	Low    float64 `json:"low_price"`
	Close  float64 `json:"close_price"`
	Volume float64 `json:"volume"`
	Turn   float64 `json:"turnover"`
}

// fuyaoBarData 是官方历史 K 线的数据体。
type fuyaoBarData struct {
	Timestamp int64          `json:"timestamp"`
	Item      []fuyaoBarItem `json:"item"`
}

// fuyaoInterval 把契约周期映射成官方 interval；不支持的周期返回空串（走原有链路）。
func fuyaoInterval(p contract.Period) (interval string, windowDays int) {
	switch p {
	case contract.PeriodDay:
		return "1d", 2
	case contract.PeriodWeek:
		return "1w", 8
	case contract.PeriodMonth:
		return "1mo", 32
	}
	return "", 0
}

// fuyaoAdjust 把契约复权方式映射成官方 adjust。
func fuyaoAdjust(adj contract.Adjust) string {
	switch adj {
	case contract.AdjustQfq:
		return "forward"
	case contract.AdjustHfq:
		return "backward"
	default:
		return "none"
	}
}

// fuyaoBars 用官方历史 K 线取日/周/月序列（含复权）；失败返回错误由调用方降级。
func (s *Service) fuyaoBars(ctx context.Context, code Code, p contract.Period, limit int, adj contract.Adjust) ([]contract.Bar, error) {
	if !s.fuyaoAvailable() {
		return nil, fmt.Errorf("datasource: 官方 API 未启用")
	}
	interval, windowDays := fuyaoInterval(p)
	if interval == "" {
		return nil, fmt.Errorf("%w: 官方 API 只覆盖日/周/月", ErrPeriodUnsupported)
	}
	ths := code.THSCode()
	if ths == "" {
		return nil, fmt.Errorf("%w: %s 无法映射成官方 thscode", ErrNoData, code.Raw)
	}
	cacheKey := "fuyao:bars:" + code.ID() + ":" + string(p) + ":" + string(adj) + ":" + strconv.Itoa(limit)
	if cached, ok := s.cache.get(cacheKey); ok {
		if bars, isBars := cached.([]contract.Bar); isBars && len(bars) >= limit {
			return lastBars(bars, limit), nil
		}
	}
	now := s.now()
	end := now.UnixMilli()
	start := now.AddDate(0, 0, -limit*windowDays-10).UnixMilli()
	query := url.Values{}
	query.Set("thscode", ths)
	query.Set("interval", interval)
	query.Set("start", strconv.FormatInt(start, 10))
	query.Set("end", strconv.FormatInt(end, 10))
	query.Set("adjust", fuyaoAdjust(adj))
	var data fuyaoBarData
	if err := s.fuyaoGet(ctx, "/api/a-share/prices/historical", query, &data); err != nil {
		return nil, err
	}
	bars := make([]contract.Bar, 0, len(data.Item))
	for _, item := range data.Item {
		if item.DateMS <= 0 || item.Close <= 0 {
			continue
		}
		bars = append(bars, contract.Bar{
			Date: time.UnixMilli(item.DateMS).In(cstZone()).Format("2006-01-02"),
			Open: item.Open, High: item.High, Low: item.Low, Close: item.Close,
			// 官方 volume 单位是股：对外统一换算成手。
			Volume: item.Volume / 100,
			Amount: item.Turn,
		})
	}
	if len(bars) == 0 {
		return nil, fmt.Errorf("%w: 官方 API 没有返回 %s 的 %s K 线", ErrNoData, ths, p)
	}
	s.cache.put(cacheKey, bars, ttlFuyaoBars)
	return lastBars(bars, limit), nil
}

// ---------- 涨停池 ----------

// fuyaoLimitStock 是官方涨停池的一行。
type fuyaoLimitStock struct {
	Thscode     string  `json:"thscode"`
	Ticker      string  `json:"ticker"`
	Name        string  `json:"name"`
	LastPrice   float64 `json:"last_price"`
	ChangePct   float64 `json:"price_change_ratio_pct"`
	LimitUpTime string  `json:"limit_up_time"`
	Reason      string  `json:"limit_up_reason"`
	BoardsText  string  `json:"continue_day_text"`
	Boards      int     `json:"continue_day_cnt"`
	SealMoney   float64 `json:"seal_money"`
	MaxSeal     float64 `json:"max_seal_money"`
	OpenTimes   *int    `json:"open_times"`
}

// fuyaoLimitData 是官方涨停池的数据体。
type fuyaoLimitData struct {
	Timestamp  int64             `json:"timestamp"`
	Item       []fuyaoLimitStock `json:"item"`
	Pagination struct {
		Total int `json:"total"`
		Pages int `json:"pages"`
		Size  int `json:"size"`
		Page  int `json:"page"`
	} `json:"pagination"`
}

// fetchLimitPoolFuyao 用官方涨停池取数并归一化成内部结构。
func (s *Service) fetchLimitPoolFuyao(ctx context.Context) (marketLimitPoolData, error) {
	if !s.fuyaoAvailable() {
		return marketLimitPoolData{}, fmt.Errorf("datasource: 官方 API 未启用")
	}
	data := marketLimitPoolData{Source: sourceFuyao}
	seen := map[string]bool{}
	// 官方分页：size 上限 200，最多翻 5 页（1000 只涨停足以覆盖任何真实盘面）。
	for page := 1; page <= 5; page++ {
		query := url.Values{}
		query.Set("page", strconv.Itoa(page))
		query.Set("size", strconv.Itoa(fuyaoPoolPageSize))
		query.Set("sort_field", "continue_day_cnt")
		query.Set("sort_dir", "desc")
		var body fuyaoLimitData
		if err := s.fuyaoGet(ctx, "/api/a-share/special-data/limit-up-pool", query, &body); err != nil {
			if page == 1 {
				return marketLimitPoolData{}, err
			}
			break
		}
		if body.Timestamp > 0 {
			data.Date = time.UnixMilli(body.Timestamp).In(cstZone()).Format("2006-01-02")
		}
		for _, row := range body.Item {
			code := strings.TrimSpace(row.Ticker)
			name := strings.TrimSpace(row.Name)
			if code == "" || name == "" || seen[code] {
				continue
			}
			seen[code] = true
			boards := row.Boards
			if boards < 1 {
				boards = 1
			}
			data.Stocks = append(data.Stocks, LimitUpStock{
				Code:      code,
				Name:      name,
				Price:     row.LastPrice,
				ChangePct: row.ChangePct,
				Boards:    boards,
				FirstTime: strings.TrimSpace(row.LimitUpTime),
				Reason:    strings.TrimSpace(row.Reason),
				// 官方给的是封单额（元），与网页同花顺 order_amount 同口径。
				Amount:     row.SealMoney,
				AmountKind: "封单额",
			})
		}
		if body.Pagination.Pages <= page || len(body.Item) == 0 {
			break
		}
	}
	if len(data.Stocks) == 0 {
		return marketLimitPoolData{}, fmt.Errorf("%w: 官方涨停池为空", ErrNoData)
	}
	sortLadder(data.Stocks)
	data.LimitUp = len(data.Stocks)
	if data.Date == "" {
		data.Date = s.now().In(cstZone()).Format("2006-01-02")
	}
	return data, nil
}

// cstZone 是 A 股统一时区（Asia/Shanghai，UTC+8，无夏令时）。
func cstZone() *time.Location { return time.FixedZone("CST", 8*3600) }

// fuyaoHealth 生成官方 API 的健康文案（设置页与 Health 共用）。
func (s *Service) fuyaoHealth() string {
	cfg := s.fuyaoConfigNow()
	switch {
	case !cfg.Enabled && cfg.APIKey == "":
		return "未配置（走现有链路）"
	case !cfg.Enabled:
		return "已关闭（走现有链路）"
	default:
		return "已启用（" + cfg.Origin + "）"
	}
}

// ---------- 设置中心（可选管理能力） ----------

// maskKey 把 API Key 变成可展示的掩码：保留前 8 位与后 4 位，中间省略。
// 绝不返回完整 Key（前端只写不读，与模型设置一致）。
func maskKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 14 {
		return strings.Repeat("•", len(key))
	}
	return key[:8] + "…" + key[len(key)-4:]
}

// FuyaoStatus 返回官方 API 的配置状态（不含明文 Key）。
func (s *Service) FuyaoStatus(context.Context) contract.FuyaoStatus {
	cfg := s.fuyaoConfigNow()
	return contract.FuyaoStatus{
		Enabled:   cfg.Enabled,
		HasKey:    cfg.APIKey != "",
		KeyMasked: maskKey(cfg.APIKey),
		KeyOrigin: cfg.Origin,
		BaseURL:   cfg.BaseURL,
		Health:    s.fuyaoHealth(),
	}
}

// SaveFuyao 保存官方 API 配置：Key 单独写 0600 文件，其余（baseUrl/开关）写 settings。
func (s *Service) SaveFuyao(_ context.Context, cfg contract.FuyaoConfig) (contract.FuyaoStatus, error) {
	if cfg.ClearKey {
		if err := s.writeFuyaoKey(""); err != nil {
			return contract.FuyaoStatus{}, err
		}
	} else if key := strings.TrimSpace(cfg.APIKey); key != "" {
		// 不允许把掩码写回去（前端展示用的 •…• 不是真 Key）。
		if strings.Contains(key, "•") || strings.Contains(key, "…") {
			return contract.FuyaoStatus{}, fmt.Errorf("datasource: 不能保存掩码形式的 Key，请重新粘贴完整 API Key")
		}
		if err := s.writeFuyaoKey(key); err != nil {
			return contract.FuyaoStatus{}, err
		}
	}
	settings := fuyaoSettings{
		BaseURL:  strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		Disabled: !cfg.Enabled,
	}
	if s.cfg.Store != nil {
		raw, err := json.Marshal(settings)
		if err != nil {
			return contract.FuyaoStatus{}, fmt.Errorf("datasource: 官方 API 配置序列化失败: %w", err)
		}
		if err := s.cfg.Store.Put("settings", fuyaoSettingsKey, raw); err != nil {
			return contract.FuyaoStatus{}, fmt.Errorf("datasource: 保存官方 API 配置失败: %w", err)
		}
	}
	s.invalidateFuyaoConfig()
	return s.FuyaoStatus(context.Background()), nil
}

// TestFuyao 用一次真实快照请求验证 Key 是否可用（不要用 GET /ping 之类不存在的探针）。
func (s *Service) TestFuyao(ctx context.Context) error {
	if !s.fuyaoAvailable() {
		return fmt.Errorf("官方 API 未启用：请先填写 API Key 并开启")
	}
	data := fuyaoSnapshotData{}
	query := url.Values{}
	query.Set("thscodes", "600519.SH")
	if err := s.fuyaoGet(ctx, "/api/a-share/prices/snapshot", query, &data); err != nil {
		return err
	}
	if len(data.Item) == 0 {
		return fmt.Errorf("官方 API 连通但返回空快照，请稍后重试")
	}
	return nil
}
