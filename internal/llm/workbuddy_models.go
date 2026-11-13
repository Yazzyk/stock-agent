package llm

// WorkBuddy 模型目录：GET /v2/enterprises/personal/models（带 Bearer token）。
// 目录里的模型 ID 是对话接口唯一合法取值，因此本文件同时负责请求前的本地校验。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const workBuddyModelsTTL = 5 * time.Minute

// workBuddyModelRaw 是模型目录的原始条目（只声明用到的字段）。
type workBuddyModelRaw struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	MaxInputTokens  int             `json:"maxInputTokens"`
	MaxOutputTokens int             `json:"maxOutputTokens"`
	Credits         json.RawMessage `json:"credits"`
	SupportsImages  *bool           `json:"supportsImages"`
	DescriptionZh   string          `json:"descriptionZh"`
	DescriptionEn   string          `json:"descriptionEn"`
}

// WorkBuddyModel 是给界面展示的模型条目（不含任何凭证信息）。
type WorkBuddyModel struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContextWindow   int    `json:"contextWindow"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	CreditHint      string `json:"creditHint,omitempty"`
	SupportsImages  bool   `json:"supportsImages,omitempty"`
	Description     string `json:"description,omitempty"`
	IsImageModel    bool   `json:"isImageModel,omitempty"`
}

// parseWorkBuddyCredits 解析积分提示。上游既可能是数字，也可能是 "x0.29 credits" 这类字符串。
func parseWorkBuddyCredits(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return formatWorkBuddyCredit(number)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "x"))
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return text
	}
	value, convErr := strconv.ParseFloat(fields[0], 64)
	if convErr != nil {
		return text
	}
	return formatWorkBuddyCredit(value)
}

// formatWorkBuddyCredit 把积分倍率格式化成界面用的短文本。
func formatWorkBuddyCredit(value float64) string {
	if value <= 0 {
		return "免费"
	}
	return fmt.Sprintf("x%.2f", value)
}

// parseWorkBuddyModels 解析模型目录响应（拆出来便于用固定样本做单测）。
func parseWorkBuddyModels(body []byte) ([]WorkBuddyModel, error) {
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Models []workBuddyModelRaw `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("解析 WorkBuddy 模型目录失败: %w", err)
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("拉取 WorkBuddy 模型目录被拒绝：code %d", envelope.Code)
	}
	models := make([]WorkBuddyModel, 0, len(envelope.Data.Models))
	for _, raw := range envelope.Data.Models {
		id := strings.TrimSpace(raw.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			name = id
		}
		description := strings.TrimSpace(raw.DescriptionZh)
		if description == "" {
			description = strings.TrimSpace(raw.DescriptionEn)
		}
		models = append(models, WorkBuddyModel{
			ID:              id,
			Name:            name,
			ContextWindow:   raw.MaxInputTokens,
			MaxOutputTokens: raw.MaxOutputTokens,
			CreditHint:      parseWorkBuddyCredits(raw.Credits),
			SupportsImages:  raw.SupportsImages != nil && *raw.SupportsImages,
			Description:     description,
			// 图像模型没有 token 窗口，不能用于对话（目录里 ctx/out 均为 null）。
			IsImageModel: raw.MaxInputTokens == 0 && strings.Contains(strings.ToLower(id), "image"),
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("WorkBuddy 模型目录为空：当前账号可能没有可用模型")
	}
	return models, nil
}

// fetchModels 用指定凭证拉取模型目录。
func (w *workBuddy) fetchModels(ctx context.Context, cred *workBuddyCredential) ([]WorkBuddyModel, error) {
	base := w.baseOf(cred.Domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, workBuddyAPIEndpoint(base, workBuddyModelsPath), nil)
	if err != nil {
		return nil, err
	}
	headers := workBuddyRequestHeaders(cred, base)
	applyWorkBuddyHeaders(req, headers)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := workBuddyHTTPClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 WorkBuddy 模型目录失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("WorkBuddy 登录态已失效（HTTP %d），请重新登录或导入", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("拉取 WorkBuddy 模型目录失败：HTTP %d", resp.StatusCode)
	}
	return parseWorkBuddyModels(body)
}

// modelsCacheFreshLocked 判断模型目录缓存是否仍然有效（要求调用方已持锁）。
func (w *workBuddy) modelsCacheFreshLocked(fingerprint string) bool {
	return len(w.modelsCache) > 0 && w.modelsTokenHash == fingerprint && time.Since(w.modelsAt) < workBuddyModelsTTL
}

// resetModelsCache 清空模型目录缓存。
func (w *workBuddy) resetModelsCache() {
	w.modelsMu.Lock()
	w.modelsAt = time.Time{}
	w.modelsTokenHash = ""
	w.modelsCache = nil
	w.modelsMu.Unlock()
}

// Models 返回当前登录账号的模型目录（含图像模型），带 5 分钟缓存。
func (w *workBuddy) Models(ctx context.Context) ([]WorkBuddyModel, error) {
	cred, err := w.credential(ctx)
	if err != nil {
		return nil, err
	}
	fingerprint := workBuddyShortHash(cred.AccessToken)
	w.modelsMu.Lock()
	if w.modelsCacheFreshLocked(fingerprint) {
		out := append([]WorkBuddyModel(nil), w.modelsCache...)
		w.modelsMu.Unlock()
		return out, nil
	}
	w.modelsMu.Unlock()

	models, fetchErr := w.fetchModels(ctx, cred)
	if fetchErr != nil {
		return nil, fetchErr
	}
	w.modelsMu.Lock()
	w.modelsAt = time.Now()
	w.modelsTokenHash = fingerprint
	w.modelsCache = append([]WorkBuddyModel(nil), models...)
	w.modelsMu.Unlock()
	return models, nil
}

// ensureModelAllowed 在发对话请求前确认模型 ID 来自当前账号的模型目录。
// 目录暂时取不到时不拦住请求（网关会给出更准确的拒绝原因），只记一条告警。
func (w *workBuddy) ensureModelAllowed(ctx context.Context, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("WorkBuddy 模型名为空：请从模型目录里选择一个")
	}
	models, err := w.Models(ctx)
	if err != nil {
		w.log.Warn("WorkBuddy 模型目录暂不可用，跳过本地模型校验", "model", model, "error", err.Error())
		return nil
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		if m.IsImageModel {
			continue
		}
		if m.ID == model {
			return nil
		}
		names = append(names, m.ID)
	}
	hint := strings.Join(names, ", ")
	if hint == "" {
		hint = "（目录里没有可对话模型）"
	}
	return fmt.Errorf("WorkBuddy 模型 %q 不在当前账号的模型目录里，可用：%s", model, hint)
}
