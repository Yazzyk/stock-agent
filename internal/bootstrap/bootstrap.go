// Package bootstrap 负责组装：配置、日志、本地存储、安全、数据源、模型服务、插件注册。
// 这里是唯一允许"知道所有具体实现"的地方，其它包都不应该 import 具体数据源。
package bootstrap

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yazzyk/stock-agent/internal/kernel"
	"github.com/yazzyk/stock-agent/pkg/contract"
	"github.com/yazzyk/stock-agent/pkg/plugin"
	pluginsall "github.com/yazzyk/stock-agent/plugins/all"

	// 装配全部内置插件：bootstrap 是唯一的组装点，测试与桌面端跑同一套插件。
	_ "github.com/yazzyk/stock-agent/plugins/builtin"
)

// Config 是进程级配置（命令行 / 环境变量）。
type Config struct {
	Headless bool
	Task     string
	Args     []string
	DataDir  string
	LogLevel string
	LogFile  string
}

// LoadConfig 解析命令行：程序名之后的所有参数都会收集进 Args，供 headless 任务使用。
func LoadConfig() Config {
	home, _ := os.UserHomeDir()
	defDir := filepath.Join(home, ".guanlan")
	if v := os.Getenv("GUANLAN_DATA_DIR"); v != "" {
		defDir = v
	}
	cfg := Config{}
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&cfg.Headless, "headless", false, "无界面模式：在终端跑一次任务")
	fs.StringVar(&cfg.Task, "task", "selftest", "headless 任务：selftest|plugins|tools|quote|bars|watchlist|premarket|postmarket|monitor|quant|chat")
	fs.StringVar(&cfg.DataDir, "data-dir", defDir, "本地数据目录")
	fs.StringVar(&cfg.LogLevel, "log-level", envOr("GUANLAN_LOG_LEVEL", "info"), "日志级别 debug|info|warn|error")
	fs.StringVar(&cfg.LogFile, "log-file", "", "日志文件（缺省输出到 stderr）")
	_ = fs.Parse(os.Args[1:])
	cfg.Args = fs.Args()
	return cfg
}

// NewLogger 构建进程级 slog logger：默认 stderr，可落文件。
func NewLogger(cfg Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	var w io.Writer = os.Stderr
	if cfg.LogFile != "" {
		if f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			w = io.MultiWriter(os.Stderr, f)
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// New 组装 kernel：本地存储 + 安全策略 + 数据源 + 模型服务 + 全部插件。
func New(cfg Config, logger *slog.Logger) (*kernel.Kernel, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("bootstrap: 创建数据目录失败: %w", err)
	}
	store, err := openStore(cfg, logger)
	if err != nil {
		return nil, err
	}

	guard := newGuard(cfg, store, logger)
	audit := newAudit(store, logger)
	data := newDataService(cfg, store, guard, logger)
	llmSvc, err := newLLMService(cfg, store, guard, logger)
	if err != nil {
		return nil, err
	}

	k, err := kernel.New(kernel.Options{
		Logger:  logger,
		Store:   store,
		Data:    data,
		LLM:     llmSvc,
		Guard:   guard,
		Audit:   audit,
		Clock:   time.Now,
		DataDir: cfg.DataDir,
	})
	if err != nil {
		return nil, err
	}
	// 跨插件服务：模型服务以 "llm" 暴露，插件用 ctx.Services.Get("llm") 取用。
	if err := k.Services().Register("llm", llmSvc); err != nil {
		return nil, fmt.Errorf("bootstrap: 注册 llm 服务失败: %w", err)
	}
	// 数据服务同时作为 "datasource" 暴露：设置中心据此配置官方金融数据 API。
	if err := k.Services().Register("datasource", data); err != nil {
		return nil, fmt.Errorf("bootstrap: 注册 datasource 服务失败: %w", err)
	}

	// 插件启停：先按 settings/plugins.disabled 标记（必须在 Start 之前），
	// 再把总览服务注册给设置中心插件。
	disabled := loadDisabledPlugins(store, logger)
	if err := k.SetDisabled(disabled); err != nil {
		return nil, fmt.Errorf("bootstrap: 应用插件停用配置失败: %w", err)
	}
	if len(disabled) > 0 {
		logger.Info("已停用插件（重启生效）", "disabled", disabled)
	}
	if err := k.Services().Register(inventoryServiceName, newKernelInventory(k, store, logger, disabled)); err != nil {
		return nil, fmt.Errorf("bootstrap: 注册设置中心服务失败: %w", err)
	}

	for _, p := range pluginsall.All() {
		if err := k.Register(p); err != nil {
			return nil, fmt.Errorf("bootstrap: 注册插件失败: %w", err)
		}
	}
	logger.Info("bootstrap 完成", "dataDir", cfg.DataDir, "plugins", len(k.Plugins()))
	return k, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// 这些是"接入点"，具体实现在 internal/store、internal/security、internal/datasource、internal/llm。
// 拆分交付期间由 stubs.go 提供过渡实现，保证主程序随时可编译可运行。
// 五个接线点：由 bootstrap 独占，其它包不感知具体实现。
var (
	openStoreFn      func(Config, *slog.Logger) (contract.Store, error)                                      = stubOpenStore
	newGuardFn       func(Config, contract.Store, *slog.Logger) contract.Guard                               = stubNewGuard
	newAuditFn       func(contract.Store, *slog.Logger) contract.Audit                                       = stubNewAudit
	newDataServiceFn func(Config, contract.Store, contract.Guard, *slog.Logger) contract.DataService         = stubNewDataService
	newLLMServiceFn  func(Config, contract.Store, contract.Guard, *slog.Logger) (contract.LLMService, error) = realNewLLMService
)

func openStore(cfg Config, logger *slog.Logger) (contract.Store, error) {
	return openStoreFn(cfg, logger)
}

func newGuard(cfg Config, store contract.Store, logger *slog.Logger) contract.Guard {
	return newGuardFn(cfg, store, logger)
}

func newAudit(store contract.Store, logger *slog.Logger) contract.Audit {
	return newAuditFn(store, logger)
}

func newDataService(cfg Config, store contract.Store, g contract.Guard, logger *slog.Logger) contract.DataService {
	return newDataServiceFn(cfg, store, g, logger)
}

func newLLMService(cfg Config, store contract.Store, g contract.Guard, logger *slog.Logger) (contract.LLMService, error) {
	return newLLMServiceFn(cfg, store, g, logger)
}

var _ = plugin.RiskRead
