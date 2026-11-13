package bootstrap

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

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

// retiredHosts 是从数据层移除后不再放行的主机后缀。
// 老版本把 eastmoney.com 写进了用户策略；这里在读取时剔除，
// 否则升级后白名单还留着东财，等于给"以后又接回去"留了后门。
var retiredHosts = []string{"eastmoney.com"}

// stripRetiredHosts 从白名单里剔除已下线数据源的主机（大小写、通配符、子域都覆盖）。
func stripRetiredHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		key := strings.ToLower(strings.TrimSpace(h))
		key = strings.TrimPrefix(key, "*.")
		retired := false
		for _, bad := range retiredHosts {
			if key == bad || strings.HasSuffix(key, "."+bad) {
				retired = true
				break
			}
		}
		if !retired {
			out = append(out, h)
		}
	}
	return out
}

// policyFromStore 读取用户自定义策略；缺省用内置白名单。
func policyFromStore(cfg Config, st contract.Store) contract.Policy {
	policy := defaultPolicy(cfg)
	raw, err := st.Get("settings", "policy")
	if err != nil || len(raw) == 0 {
		return policy
	}
	var p contract.Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return policy
	}
	p.NetworkAllowlist = stripRetiredHosts(p.NetworkAllowlist)
	if len(p.NetworkAllowlist) == 0 {
		return policy
	}
	// 老策略里没有官方数据源主机：自动补上（官方 API 是内置数据能力，缺它会让设置页
	// 明明配了 Key 却一直降级，表现为"官方源不可用"的假故障）。
	if !networkAllows(p.NetworkAllowlist, "aicubes.cn") {
		p.NetworkAllowlist = append(p.NetworkAllowlist, "aicubes.cn")
	}
	// AllowMethodControl 是新增开关：老策略里没有这个键时不能静默变成 false，
	// 否则升级后主控立刻失去对观澜的控制权。只有用户显式写过才认。
	if !jsonHasKey(raw, "allowMethodControl") {
		p.AllowMethodControl = policy.AllowMethodControl
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

// networkAllows 判断白名单里是否已放行某个主机后缀（含 *. 前缀写法）。
func networkAllows(hosts []string, host string) bool {
	for _, h := range hosts {
		key := strings.ToLower(strings.TrimSpace(h))
		key = strings.TrimPrefix(key, "*.")
		if key == host || strings.HasSuffix(host, "."+key) {
			return true
		}
	}
	return false
}

// jsonHasKey 判断一段 JSON 对象里是否显式出现过某个键（用于区分"未配置"与"配置成零值"）。
func jsonHasKey(raw []byte, key string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	_, ok := probe[key]
	return ok
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
