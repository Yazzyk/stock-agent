package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/yazzyk/stock-agent/internal/llm"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

// realNewLLMService 把模型服务接到 kernel：同一个实例既作为 Options.LLM，
// 又以 service "llm" 暴露给插件（modelsettings 用），保证配置只有一份真源。
func realNewLLMService(cfg Config, store contract.Store, guard contract.Guard, logger *slog.Logger) (contract.LLMService, error) {
	svc, err := llm.New(llm.Config{
		Store:   store,
		Guard:   guard,
		Logger:  logger,
		DataDir: cfg.DataDir,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: 初始化模型服务失败: %w", err)
	}
	logger.Info("模型服务就绪", "dataDir", cfg.DataDir)
	return svc, nil
}
