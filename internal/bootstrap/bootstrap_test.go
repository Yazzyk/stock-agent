package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/yazzyk/stock-agent/internal/kernel"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestKernel(t *testing.T) (*kernel.Kernel, Config) {
	t.Helper()
	cfg := Config{DataDir: t.TempDir(), LogLevel: "error", Task: "selftest"}
	k, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("bootstrap.New 失败: %v", err)
	}
	if err := k.Start(context.Background()); err != nil {
		t.Fatalf("kernel 启动失败: %v", err)
	}
	return k, cfg
}

// 端到端：真实 sqlite + 真实安全网关 + 已注册插件能一起启动。
func TestKernelBootsWithRealInfrastructure(t *testing.T) {
	k, cfg := newTestKernel(t)
	defer func() { _ = k.Stop(context.Background()) }()

	if _, err := os.Stat(filepath.Join(cfg.DataDir, "guanlan.db")); err != nil {
		t.Fatalf("本地数据库未创建: %v", err)
	}
	st := k.Status()
	if st.Plugins == 0 || st.Active != st.Plugins {
		t.Fatalf("插件未全部激活: %+v / %+v", st, k.Plugins())
	}
	if len(st.Services) == 0 {
		t.Fatal("服务未注册")
	}
	// 真实 Guard 生效
	if k.Guard().AllowHost("evil.example.com") {
		t.Fatal("非白名单域名不应放行")
	}
	if !k.Guard().AllowHost("d.10jqka.com.cn") {
		t.Fatal("数据源域名应放行")
	}
	if err := k.Guard().CheckPath("/tmp/outside-guanlan.txt"); err == nil {
		t.Fatal("数据目录外路径应拒绝")
	}
	if err := k.Guard().CheckPath(filepath.Join(cfg.DataDir, "runs", "x.md")); err != nil {
		t.Fatalf("数据目录内路径应放行: %v", err)
	}
}

// 模型配置必须跨进程持久化（真实 sqlite，而非内存）。
func TestModelSettingsPersistAcrossRestart(t *testing.T) {
	k, cfg := newTestKernel(t)
	out, err := k.Invoke(context.Background(), "modelsettings", "list", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	var list struct {
		Providers []struct {
			ID     string `json:"id"`
			HasKey bool   `json:"hasKey"`
		} `json:"providers"`
		DefaultID string `json:"defaultId"`
	}
	if err := json.Unmarshal(out, &list); err != nil || len(list.Providers) == 0 {
		t.Fatalf("list 返回异常: %s", out)
	}
	id := list.Providers[0].ID
	save, _ := json.Marshal(map[string]any{"id": id, "provider": "deepseek", "model": "deepseek-chat", "apiKey": "sk-persist-test"})
	if _, err := k.Invoke(context.Background(), "modelsettings", "save", save); err != nil {
		t.Fatalf("save 失败: %v", err)
	}
	if err := k.Stop(context.Background()); err != nil {
		t.Fatalf("停止失败: %v", err)
	}

	// 用同一个 DataDir 重新启动，模拟重启应用。
	k2, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("二次启动失败: %v", err)
	}
	if err := k2.Start(context.Background()); err != nil {
		t.Fatalf("二次 kernel 启动失败: %v", err)
	}
	defer func() { _ = k2.Stop(context.Background()) }()
	out2, err := k2.Invoke(context.Background(), "modelsettings", "list", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("重启后 list 失败: %v", err)
	}
	if err := json.Unmarshal(out2, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Providers) == 0 || !list.Providers[0].HasKey {
		t.Fatalf("重启后配置丢失: %s", out2)
	}
	if json.Valid(out2) && len(out2) > 0 {
		// 密文不得回显
		if contains(out2, "sk-persist-test") {
			t.Fatal("返回里出现了明文 key")
		}
	}
}

// 审计必须真实落盘。
func TestAuditPersistsEvents(t *testing.T) {
	k, cfg := newTestKernel(t)
	if _, err := k.Invoke(context.Background(), "modelsettings", "list", json.RawMessage("{}")); err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	events, err := k.Audit().List(20)
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("应至少有一条激活/方法调用审计")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "guanlan.db")); err != nil {
		t.Fatalf("审计未落库: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Plugin == "modelsettings" && e.Kind == "method" && e.Action == "list" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺少 modelsettings.list 审计: %+v", events)
	}
}

func contains(b []byte, sub string) bool {
	return len(sub) > 0 && len(b) >= len(sub) && string(b) != "" && indexOf(string(b), sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
