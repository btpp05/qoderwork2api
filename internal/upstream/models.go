// models.go 动态模型获取：COSY 签名 GET /algo/api/v2/model/list?Encode=1，
// 拿 chat scene 的 key 列表，缓存后给 handler 用。
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"qoderwork2api/internal/cred"
)

// ModelsPath 模型列表端点。
const ModelsPath = "/algo/api/v2/model/list?Encode=1"

// ContextOption 上下文窗口可选档位。
type ContextOption struct {
	TokenCount int64 `json:"token_count"`
	IsDefault bool   `json:"is_default"`
}

// DynamicModel 上游 chat scene 单个模型。
type DynamicModel struct {
	Key            string  `json:"key"`
	DisplayName    string  `json:"display_name"`
	Enable         bool    `json:"enable"`
	IsReasoning    bool    `json:"is_reasoning"`
	IsVL           bool    `json:"is_vl"`
	MaxInputTokens int64   `json:"max_input_tokens"`
	PriceFactor    float64 `json:"price_factor"`
	ContextConfig  map[string]ContextOption `json:"context_config,omitempty"`
}

// MaxContextTokens 返回模型支持的最大上下文 token 数。
// 优先取 context_config 中最大的 token_count，没有则回退 max_input_tokens。
func (m DynamicModel) MaxContextTokens() int64 {
	var max int64
	for _, opt := range m.ContextConfig {
		if opt.TokenCount > max {
			max = opt.TokenCount
		}
	}
	if max > 0 {
		return max
	}
	return m.MaxInputTokens
}

// DefaultContextTokens 返回默认上下文窗口大小。
// 优先取 context_config 中 is_default=true 的 token_count，没有则回退 max_input_tokens。
func (m DynamicModel) DefaultContextTokens() int64 {
	for _, opt := range m.ContextConfig {
		if opt.IsDefault && opt.TokenCount > 0 {
			return opt.TokenCount
		}
	}
	if m.MaxInputTokens > 0 {
		return m.MaxInputTokens
	}
	return 180000 // 兜底
}

// FetchModels 调上游动态模型接口。
// GET 无 body，签名用空串 ""（非 "{}"，后者 403 Signature invalid）。
// 参考 cliproxy-plugin models.go callModelsAPI 注释。
func (c *Client) FetchModels(cr *cred.Cred) ([]DynamicModel, error) {
	if cr.DT == "" {
		return nil, fmt.Errorf("no dt- available")
	}
	rawURL := c.Gateway + ModelsPath
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	sess, err := NewCosySession(cr, cr.DT, cr.DRT)
	if err != nil {
		return nil, fmt.Errorf("cosy session: %w", err)
	}
	if err := sess.ApplyHeaders(req, "", rawURL, cr.UID, false, ""); err != nil {
		return nil, fmt.Errorf("cosy headers: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("models api status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}
	var apiResp map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}
	chatRaw, ok := apiResp["chat"]
	if !ok {
		return nil, fmt.Errorf("no chat scene in models response")
	}
	var models []DynamicModel
	if err := json.Unmarshal(chatRaw, &models); err != nil {
		return nil, fmt.Errorf("chat scene parse: %w", err)
	}
	// 只留启用的
	enabled := make([]DynamicModel, 0, len(models))
	for _, m := range models {
		if m.Enable && m.Key != "" {
			enabled = append(enabled, m)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("no enabled chat models")
	}
	return enabled, nil
}

// ResolveModelMap 动态模型 → 客户端模型映射。
// 客户端名 = display_name 规范化（无 display_name 用 key 兜底）。
// 返回：客户端名 → 上游 key。
func ResolveModelMap(dynamic []DynamicModel) map[string]string {
	out := make(map[string]string, len(dynamic))
	for _, m := range dynamic {
		name := m.Key
		if m.DisplayName != "" {
			name = NormalizeModelName(m.DisplayName)
		}
		out[name] = m.Key
	}
	return out
}

// NormalizeModelName 把 display_name 转成 OpenAI 风格客户端名：
// 小写、空格/下划线转连字符、保留点号（版本号）、去重连字符。
// "Qwen3.8-Max-Preview" → "qwen3.8-max-preview"
// "DeepSeek-V4-Pro"     → "deepseek-v4-pro"
// "GLM-5.2"             → "glm-5.2"
func NormalizeModelName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.':
			b.WriteRune(r)
			prevDash = false
		case r == ' ' || r == '_' || r == '-':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// 其他字符（中文等）原样保留
			b.WriteRune(r)
			prevDash = false
		}
	}
	return strings.Trim(b.String(), "-")
}
