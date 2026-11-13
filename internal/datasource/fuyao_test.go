package datasource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yazzyk/stock-agent/pkg/contract"
)

// newFuyaoTestService 构造一个指向 httptest 服务的测试数据服务（Key 走环境变量）。
func newFuyaoTestService(t *testing.T, handler http.HandlerFunc) (*Service, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv(fuyaoKeyEnv, "sk-test")
	svc, err := New(Config{
		Guard:   newFakeGuard(),
		Store:   newMemStore(),
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc.(*Service), srv
}

// setFuyaoBase 把测试服务的官方 API 地址指到 httptest。
func setFuyaoBase(t *testing.T, s *Service, base string) {
	t.Helper()
	s.cfg.Store.Put("settings", fuyaoSettingsKey, []byte(`{"baseUrl":"`+base+`"}`))
	s.invalidateFuyaoConfig()
}

// TestFuyaoQuotesMapsUnits 验证官方快照的字段映射与单位换算（股 → 手）。
func TestFuyaoQuotesMapsUnits(t *testing.T) {
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-api-key"); got != "sk-test" {
			t.Errorf("必须带 X-api-key，实际 %q", got)
		}
		if r.URL.Path != "/api/a-share/prices/snapshot" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("thscodes"); got != "600519.SH,000001.SZ" {
			t.Errorf("thscodes 应逗号拼接，实际 %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "message": "success",
			"data": map[string]any{
				"timestamp": 1747584000000,
				"item": []map[string]any{{
					"thscode": "600519.SH", "ticker": "600519",
					"volume": 3142572, "turnover": 5401389334.87,
					"last_price": 1602.6, "price_change": 21.8, "price_change_ratio_pct": 1.74,
					"open_price": 1611.6, "high_price": 1626.6, "low_price": 1601.7, "prev_price": 1580.8,
				}},
			},
		})
	})
	setFuyaoBase(t, s, srv.URL)

	codes := []Code{
		{Raw: "600519", Symbol: "600519", Market: contract.MarketSH, Kind: KindStock},
		{Raw: "000001", Symbol: "000001", Market: contract.MarketSZ, Kind: KindStock},
	}
	got := s.fuyaoQuotes(context.Background(), codes)
	sh, ok := got["sh600519"]
	if !ok {
		t.Fatalf("必须取到 600519 的快照，实际 %+v", got)
	}
	if sh.Volume != 31425.72 {
		t.Fatalf("成交量必须从股换算成手（/100），实际 %v", sh.Volume)
	}
	if sh.Price != 1602.6 || sh.ChangePct != 1.74 || sh.Source != sourceFuyao {
		t.Fatalf("价格字段映射不对: %+v", sh)
	}
	if sh.Time == "" {
		t.Fatal("时间必须来自 data.timestamp")
	}
}

// TestFuyaoBarsSupportsAdjustAndPeriod 验证 K 线的周期/复权参数映射与日期格式。
func TestFuyaoBarsSupportsAdjustAndPeriod(t *testing.T) {
	var seenInterval, seenAdjust string
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		seenInterval = r.URL.Query().Get("interval")
		seenAdjust = r.URL.Query().Get("adjust")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": []map[string]any{{
					"date_ms":    1716134400000,
					"open_price": 10, "high_price": 11, "low_price": 9, "close_price": 10.5,
					"volume": 100000, "turnover": 1050000,
				}},
			},
		})
	})
	setFuyaoBase(t, s, srv.URL)

	code := Code{Raw: "600519", Symbol: "600519", Market: contract.MarketSH, Kind: KindStock}
	bars, err := s.fuyaoBars(context.Background(), code, contract.PeriodWeek, 30, contract.AdjustQfq)
	if err != nil {
		t.Fatalf("官方 K 线失败: %v", err)
	}
	if seenInterval != "1w" || seenAdjust != "forward" {
		t.Fatalf("周期/复权映射不对: interval=%s adjust=%s", seenInterval, seenAdjust)
	}
	if len(bars) != 1 || bars[0].Date != "2024-05-20" {
		t.Fatalf("K 线映射不对: %+v", bars)
	}
	if bars[0].Volume != 1000 {
		t.Fatalf("成交量必须换算成手，实际 %v", bars[0].Volume)
	}
	// 分钟线官方不支持：必须明确返回 ErrPeriodUnsupported，让上层走原链路。
	if _, err := s.fuyaoBars(context.Background(), code, contract.PeriodMin30, 30, contract.AdjustNone); err == nil {
		t.Fatal("分钟线必须返回不支持的明确错误")
	}
}

// TestFuyaoLimitPoolMapsBoards 验证涨停池的连板数/封单额映射。
func TestFuyaoLimitPoolMapsBoards(t *testing.T) {
	s, srv := newFuyaoTestService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/a-share/special-data/limit-up-pool" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"timestamp":  1747584000000,
				"pagination": map[string]any{"total": 2, "pages": 1, "size": 200, "page": 1},
				"item": []map[string]any{
					{"ticker": "603175", "name": "超颖电子", "last_price": 99.54, "price_change_ratio_pct": 10.0,
						"limit_up_time": "09:38", "limit_up_reason": "AI算力PCB", "continue_day_cnt": 3, "seal_money": 11088756},
					{"ticker": "605255", "name": "天普股份", "last_price": 67.11, "price_change_ratio_pct": 9.99,
						"limit_up_time": "09:41", "limit_up_reason": "实控人变更", "continue_day_cnt": 1, "seal_money": 7194192},
				},
			},
		})
	})
	setFuyaoBase(t, s, srv.URL)

	data, err := s.fetchLimitPoolFuyao(context.Background())
	if err != nil {
		t.Fatalf("官方涨停池失败: %v", err)
	}
	if len(data.Stocks) != 2 || data.LimitUp != 2 {
		t.Fatalf("涨停池条数不对: %+v", data)
	}
	if data.Stocks[0].Boards != 3 || data.Stocks[0].AmountKind != "封单额" {
		t.Fatalf("连板数/封单额映射不对: %+v", data.Stocks[0])
	}
	if data.Source != sourceFuyao || data.Date != "2025-05-19" {
		t.Fatalf("来源/日期不对: %+v", data)
	}
}

// TestFuyaoSaveAndStatus 验证 Key 落盘 0600、掩码展示与状态读取。
func TestFuyaoSaveAndStatus(t *testing.T) {
	svc, err := New(Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := svc.(*Service)
	t.Setenv(fuyaoKeyEnv, "")
	s.invalidateFuyaoConfig()

	status, err := s.SaveFuyao(context.Background(), contract.FuyaoConfig{
		APIKey: "sk-fuyao-1234567890abcdef", BaseURL: "https://fuyao.aicubes.cn/", Enabled: true,
	})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if !status.HasKey || status.KeyOrigin != "file" {
		t.Fatalf("保存后应读到 file Key，实际 %+v", status)
	}
	if status.KeyMasked == "sk-fuyao-1234567890abcdef" || status.KeyMasked == "" {
		t.Fatalf("必须返回掩码而不是明文，实际 %q", status.KeyMasked)
	}
	if status.BaseURL != "https://fuyao.aicubes.cn" {
		t.Fatalf("BaseURL 必须去掉尾部斜杠，实际 %q", status.BaseURL)
	}
	// 清空 Key 后应回到未配置状态（现有链路兜底）。
	status, err = s.SaveFuyao(context.Background(), contract.FuyaoConfig{Enabled: true, ClearKey: true})
	if err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	if status.HasKey {
		t.Fatalf("清除后不应还有 Key，实际 %+v", status)
	}
}

// TestFuyaoLive 是真实官方 API 的连通性验证（默认跳过；设置
// GUANLAN_FUYAO_LIVE=1 与 GUANLAN_FUYAO_API_KEY 后才跑，避免 CI 依赖外网/密钥）。
func TestFuyaoLive(t *testing.T) {
	if os.Getenv("GUANLAN_FUYAO_LIVE") != "1" {
		t.Skip("未开启真实连通测试（GUANLAN_FUYAO_LIVE=1）")
	}
	if strings.TrimSpace(os.Getenv(fuyaoKeyEnv)) == "" {
		t.Skip("缺少 " + fuyaoKeyEnv)
	}
	svc, err := New(Config{Guard: newFakeGuard(), Store: newMemStore(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := svc.(*Service)
	ctx := context.Background()
	quotes := s.fuyaoQuotes(ctx, []Code{{Raw: "600519", Symbol: "600519", Market: contract.MarketSH, Kind: KindStock}})
	if q := quotes["sh600519"]; q.Price <= 0 {
		t.Fatalf("官方快照没有取到价格: %+v", quotes)
	}
	if _, err := s.fuyaoBars(ctx, Code{Raw: "600519", Symbol: "600519", Market: contract.MarketSH, Kind: KindStock}, contract.PeriodDay, 10, contract.AdjustQfq); err != nil {
		t.Fatalf("官方 K 线失败: %v", err)
	}
	pool, err := s.fetchLimitPoolFuyao(ctx)
	if err != nil {
		t.Fatalf("官方涨停池失败: %v", err)
	}
	if len(pool.Stocks) == 0 {
		t.Fatal("官方涨停池为空")
	}
}
