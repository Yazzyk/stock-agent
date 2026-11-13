package contract

import "context"

// FuyaoConfig 是官方同花顺金融数据 API 的可写配置。
// APIKey 为空表示「保持现有 Key 不变」；ClearKey=true 表示删除已保存的 Key。
type FuyaoConfig struct {
	APIKey   string `json:"apiKey,omitempty"`
	BaseURL  string `json:"baseUrl,omitempty"`
	Enabled  bool   `json:"enabled"`
	ClearKey bool   `json:"clearKey,omitempty"`
}

// FuyaoStatus 是官方数据源的配置状态（**不含明文 Key**，只给掩码与来源）。
type FuyaoStatus struct {
	Enabled   bool   `json:"enabled"`
	HasKey    bool   `json:"hasKey"`
	KeyMasked string `json:"keyMasked,omitempty"`
	KeyOrigin string `json:"keyOrigin,omitempty"`
	BaseURL   string `json:"baseUrl"`
	Health    string `json:"health,omitempty"`
}

// FuyaoAdmin 是数据层实现的可选管理能力：设置中心据此配置官方 API。
// 用可选接口而不是扩大 DataService，避免所有 stub 连锁改动。
type FuyaoAdmin interface {
	FuyaoStatus(ctx context.Context) FuyaoStatus
	SaveFuyao(ctx context.Context, cfg FuyaoConfig) (FuyaoStatus, error)
	TestFuyao(ctx context.Context) error
}
