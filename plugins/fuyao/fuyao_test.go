package fuyao

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yazzyk/stock-agent/internal/datasource"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
)

// memGuard 是测试用安全策略：放行全部主机与路径。
type memGuard struct{}

func (memGuard) AllowHost(string) bool   { return true }
func (memGuard) CheckPath(string) error  { return nil }
func (memGuard) Redact(s string) string  { return s }
func (memGuard) Policy() contract.Policy { return contract.Policy{NetworkAllowlist: []string{"*"}} }

// memStore 是内存版 Store（与 sqlite 语义一致：按 key 升序 List）。
type memStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

// newMemStore 构造空存储。
func newMemStore() *memStore { return &memStore{data: map[string]map[string][]byte{}} }

func (s *memStore) Get(bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.data[bucket][key]; ok {
		return value, nil
	}
	return nil, contract.ErrNotFound
}

func (s *memStore) Put(bucket, key string, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = append([]byte(nil), val...)
	return nil
}

func (s *memStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}

func (s *memStore) List(bucket, prefix string) ([]contract.KVItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]contract.KVItem, 0, len(s.data[bucket]))
	for key, value := range s.data[bucket] {
		if strings.HasPrefix(key, prefix) {
			out = append(out, contract.KVItem{Key: key, Value: value})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *memStore) Close() error { return nil }

// newTestPlugin 构造指向 httptest 的插件上下文（Key 走环境变量）。
func newTestPlugin(t *testing.T, handler http.HandlerFunc) (*Plugin, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("GUANLAN_FUYAO_API_KEY", "sk-test")
	store := newMemStore()
	_ = store.Put("settings", "fuyao", []byte(`{"baseUrl":"`+srv.URL+`"}`))
	data, err := datasource.New(datasource.Config{Guard: memGuard{}, Store: store, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p := &Plugin{}
	ctx := &plugin.Context{
		ID:       pluginID,
		Manifest: p.Manifest(),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:    time.Now,
		Tools:    plugin.NewToolRegistry(pluginID),
		UI:       plugin.NewUIRegistry(pluginID),
		Methods:  plugin.NewMethodRegistry(pluginID),
		Services: plugin.NewServiceRegistry(),
		Events:   plugin.NewBus(),
		Store:    store,
		Data:     data,
	}
	if err := p.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	return p, srv
}

// TestManifestAndTools 验证 Manifest、工具数量与 schema 硬约束。
func TestManifestAndTools(t *testing.T) {
	p, _ := newTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {})
	if p.Manifest().ID != "fuyao" || p.Manifest().Name == "" {
		t.Fatalf("Manifest 不完整: %+v", p.Manifest())
	}
	tools := p.ctx.Tools.List()
	if len(tools) != 20 {
		t.Fatalf("fuyao 应注册 20 个工具，实际 %d", len(tools))
	}
	for _, tool := range tools {
		if !strings.HasPrefix(tool.Name, "fuyao.") {
			t.Fatalf("工具名必须以 fuyao. 开头: %s", tool.Name)
		}
		if tool.Risk != plugin.RiskNetwork {
			t.Fatalf("%s 风险等级应为 network，实际 %s", tool.Name, tool.Risk)
		}
		if strings.TrimSpace(tool.Summary) == "" {
			t.Fatalf("%s 缺少中文 Summary", tool.Name)
		}
		if !json.Valid(tool.Schema) {
			t.Fatalf("%s schema 不是合法 JSON", tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema 必须 additionalProperties:false", tool.Name)
		}
	}
	// 前端方法必须覆盖 21 个视图入口（catalog/query + 19 个强类型域方法）。
	if got := len(p.ctx.Methods.List()); got != 21 {
		t.Fatalf("fuyao 应注册 21 个前端方法，实际 %d", got)
	}
	// UI slot：PanelHost 按 component 找 src/panels/<component>.vue，必须与文件名一致。
	if !containsString(p.Manifest().Provides, "ui:fuyao") {
		t.Fatalf("Manifest().Provides 缺少 ui:fuyao: %v", p.Manifest().Provides)
	}
	slots := p.ctx.UI.List()
	if len(slots) != 1 {
		t.Fatalf("fuyao 应注册 1 个 UI slot，实际 %d", len(slots))
	}
	slot := slots[0]
	if slot.ID != "fuyao" || slot.Component != "fuyao" || slot.Group != "行情" || slot.Order != 16 || slot.Icon != "database" {
		t.Fatalf("UI slot 不符合约定: %+v", slot)
	}
}

// containsString 判断字符串切片是否包含目标值（测试辅助）。
func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

// TestToolCatalog 验证目录工具返回 125 个端点。
func TestToolCatalog(t *testing.T) {
	p, _ := newTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("目录工具不应发网络请求: %s", r.URL.Path)
	})
	reply, err := p.toolCatalog(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	view := reply.(map[string]any)
	if view["total"].(int) != 125 {
		t.Fatalf("目录总数应为 125，实际 %v", view["total"])
	}
}

// TestToolQuoteAndPermission 验证行情工具输出与 2004 降级。
func TestToolQuoteAndPermission(t *testing.T) {
	p, _ := newTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/a-share/prices/snapshot":
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"timestamp":1794552945000,"item":[{"thscode":"600519.SH","ticker":"600519","volume":3950084,"turnover":5627798300,"last_price":1421.0,"price_change":21.8,"price_change_ratio_pct":1.56,"open_price":1405.0,"high_price":1430.0,"low_price":1400.1,"prev_price":1399.2}]}}`))
		case "/api/a-share/capital-flow/snapshot":
			_, _ = w.Write([]byte(`{"code":2004,"message":"该数据为同花顺AI客户端专用","data":null}`))
		default:
			t.Errorf("未预期路径: %s", r.URL.Path)
		}
	})
	reply, err := p.toolQuote(context.Background(), json.RawMessage(`{"codes":["600519"]}`))
	if err != nil {
		t.Fatal(err)
	}
	view := reply.(map[string]any)
	if view["count"].(int) != 1 || !strings.Contains(view["note"].(string), "volume 单位股") {
		t.Fatalf("行情工具输出不对: %+v", view)
	}
	// 资金流是客户端专用：必须返回 permission=client 而不是错误。
	cfg, err := p.toolCapitalFlow(context.Background(), json.RawMessage(`{"code":"600519","view":"snapshot"}`))
	if err != nil {
		t.Fatalf("2004 应降级而不是报错: %v", err)
	}
	if cfg.(map[string]any)["permission"] != "client" {
		t.Fatalf("2004 应返回 permission=client，实际 %+v", cfg)
	}
}

// TestMethodQueryValidatesParams 验证通用方法对未知参数给出可操作错误。
func TestMethodQueryValidatesParams(t *testing.T) {
	p, _ := newTestPlugin(t, func(w http.ResponseWriter, r *http.Request) {})
	_, err := p.methodQuery(context.Background(), json.RawMessage(`{"endpoint":"a-share.prices.snapshot","params":{"thscode":"600519.SH"}}`))
	if err == nil || !strings.Contains(err.Error(), "不支持参数") {
		t.Fatalf("未知参数必须报错，实际 %v", err)
	}
}

// TestMain 避免测试受本机真实 Key 影响（环境变量由各测试显式设置）。
func TestMain(m *testing.M) {
	_ = os.Unsetenv("GUANLAN_FUYAO_API_KEY")
	os.Exit(m.Run())
}
