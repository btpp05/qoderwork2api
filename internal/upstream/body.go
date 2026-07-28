// body.go 构造 agent_chat_generation 请求体（纯透传模式）。
//
// 纯透传：客户端消息全量转发（含 system/assistant/tool 多轮），
// tools 仅在客户端显式传入时注入。实测模板 system + 模板 tools 均非必需，
// 且 baseline prompt_tokens 从 ~10K 降到 ~60。
package upstream

import (
	"encoding/json"
	"time"
)

// BuildAgentBody 构造请求体。
//   - openaiMessages：客户端原始消息列表（可含 system/assistant/tool 多轮）
//   - modelKey：上游模型 key（如 qmodel_preview）
//   - clientTools：客户端传来的 OpenAI tools 数组；为空则不注入 tools 字段
func BuildAgentBody(openaiMessages []map[string]any, modelKey string, clientTools []any) ([]byte, error) {
	// 最后一条 user 消息文本（chat_context.text 上游协议要求必填）
	prompt := ""
	for i := len(openaiMessages) - 1; i >= 0; i-- {
		if openaiMessages[i]["role"] == "user" {
			if c, ok := openaiMessages[i]["content"].(string); ok && c != "" {
				prompt = c
				break
			}
		}
	}

	now := time.Now()
	newUUID := uuid4()

	base := map[string]any{
		"request_id":        newUUID,
		"chat_record_id":    newUUID,
		"request_set_id":    uuid4(),
		"session_id":        uuid4(),
		"stream":            true,
		"aliyun_user_type":  "personal_professional_trial",
		"agent_id":          "agent_common",
		"chat_task":         "FREE_INPUT",
		"is_reply":          true,
		"image_urls":        nil,
		"session_type":      "qodercli",
		"model_config":      map[string]any{"key": modelKey, "is_reasoning": false},
		"chat_context": map[string]any{
			"chatPrompt": "",
			"text":       map[string]any{"type": "text", "text": prompt},
			"extra": map[string]any{
				"context":         []any{},
				"modelConfig":     map[string]any{"key": modelKey, "is_reasoning": false},
				"originalContent": map[string]any{"type": "text", "text": prompt},
			},
			"features":  []any{},
			"imageUrls": nil,
		},
		"messages": openaiMessages,
		"business": map[string]any{
			"id":       uuid4(),
			"begin_at": now.UnixMilli(),
			"name":     truncateRunes(prompt, 30),
		},
	}

	// tools：客户端传了才注入
	if len(clientTools) > 0 {
		base["tools"] = clientTools
	}

	return json.Marshal(base)
}

// truncateRunes 截断到 n 个 rune。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
