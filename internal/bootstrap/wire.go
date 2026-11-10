package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/yazzyk/stock-agent/internal/datasource"
	"github.com/yazzyk/stock-agent/internal/security"
	"github.com/yazzyk/stock-agent/internal/store"
	"github.com/yazzyk/stock-agent/pkg/contract"
)

// 真实实现接线：sqlite 存储 + 安全网关 + 审计。
// 数据源与 harness 的接线在同一文件的后续里程碑追加（见 wireData/wireAgent）。

func realOpenStore(cfg Config, logger *slog.Logger) (contract.Store, error) {
	path := filepath.Join(cfg.DataDir, "guanlan.db")
	db, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: 打开本地数据库失败: %w", err)
	}
	logger.Info("本地数据库就绪", "path", path)
	return db, nil
}

// policyFromStore 读取用户自定义策略；缺省用内置白名单。
func policyFromStore(cfg Config, st contract.Store) contract.Policy {
	policy := defaultPolicy(cfg)
	raw, err := st.Get("settings", "policy")
	if err != nil || len(raw) == 0 {
		return policy
	}
	var p contract.Policy
	if err := json.Unmarshal(raw, &p); err != nil || len(p.NetworkAllowlist) == 0 {
		return policy
	}
	if p.MaxReqPerMin <= 0 {
		p.MaxReqPerMin = policy.MaxReqPerMin
	}
	if p.MaxToolCalls <= 0 {
		p.MaxToolCalls = policy.MaxToolCalls
	}
	if p.MaxTokensPerRun <= 0 {
		p.MaxTokensPerRun = policy.MaxTokensPerRun
	}
	if len(p.PathRoots) == 0 {
		p.PathRoots = policy.PathRoots
	}
	return p
}

func realNewGuard(cfg Config, st contract.Store, logger *slog.Logger) contract.Guard {
	policy := policyFromStore(cfg, st)
	roots := policy.PathRoots
	if len(roots) == 0 {
		roots = []string{cfg.DataDir}
	}
	g := security.NewGuard(policy, roots)
	logger.Info("安全网关就绪", "allowlist", len(policy.NetworkAllowlist), "roots", roots, "maxReqPerMin", policy.MaxReqPerMin)
	return g
}

func realNewAudit(st contract.Store, logger *slog.Logger) contract.Audit {
	logger.Info("审计日志就绪", "bucket", "audit")
	return security.NewAudit(st)
}

// realNewDataService 接入真实 A 股数据源（同花顺/东财/新浪/腾讯），所有出网请求都过 Guard。
func realNewDataService(cfg Config, st contract.Store, g contract.Guard, logger *slog.Logger) contract.DataService {
	svc, err := datasource.New(datasource.Config{
		Guard:   g,
		Store:   st,
		Logger:  logger,
		DataDir: cfg.DataDir,
	})
	if err != nil {
		logger.Error("数据源初始化失败，降级为占位实现", "error", err)
		return stubNewDataService(cfg, st, g, logger)
	}
	logger.Info("A 股数据源就绪", "sources", "同花顺/东财/新浪/腾讯")
	return svc
}

// 让 stub 的默认策略保持单一来源（stub 与真实实现共用）。
func init() {
	openStoreFn = realOpenStore
	newGuardFn = realNewGuard
	newAuditFn = realNewAudit
	newDataServiceFn = realNewDataService
}
