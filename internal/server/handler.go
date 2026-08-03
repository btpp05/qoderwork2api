package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"qoderwork2api/internal/cred"
	"qoderwork2api/internal/pool"
	"qoderwork2api/internal/upstream"
)

// Config handler 依赖。
type Config struct {
	Pool         *pool.Pool
	Upstream     *upstream.Client
	APIKey       string
	MaxRotate    int
	HardCooldown time.Duration
	SoftCooldown time.Duration
	ErrThreshold int
	ErrCooldown  time.Duration
}

// Handler 路由。
type Handler struct {
	cfg Config
	mux *http.ServeMux

	// 动态模型缓存
	modelsMu       sync.RWMutex
	dynamicModels  []upstream.DynamicModel // 上游原始模型（含 display_name/元数据）
	dynamicMap     map[string]string       // 客户端名 → 上游 key（解析后）
	dynamicFetched time.Time              // 最近一次成功拉取时间
	lastFetchFail  time.Time              // 最近一次拉取失败时间（负缓存）
}

const (
	dynamicModelsTTL        = time.Hour
	modelsFetchFailCooldown = 5 * time.Minute
)

// NewHandler 构建。
func NewHandler(cfg Config) *Handler {
	if cfg.MaxRotate <= 0 {
		cfg.MaxRotate = 3
	}
	if cfg.HardCooldown <= 0 {
		cfg.HardCooldown = 12 * time.Hour
	}
	if cfg.SoftCooldown <= 0 {
		cfg.SoftCooldown = 60 * time.Second
	}
	if cfg.ErrThreshold <= 0 {
		cfg.ErrThreshold = 3
	}
	if cfg.ErrCooldown <= 0 {
		cfg.ErrCooldown = 10 * time.Minute
	}
	h := &Handler{cfg: cfg, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /v1/chat/completions", h.withAuth(h.chatCompletions))
	h.mux.HandleFunc("GET /v1/models", h.withAuth(h.models))
	h.mux.HandleFunc("GET /status", h.withAuth(h.status))
	h.mux.HandleFunc("GET /healthz", h.healthz)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.APIKey != "" {
			authz := r.Header.Get("Authorization")
			if !strings.HasPrefix(authz, "Bearer ") || strings.TrimPrefix(authz, "Bearer ") != h.cfg.APIKey {
				writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
				return
			}
		}
		next(w, r)
	}
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": h.cfg.Pool.List(),
	})
}

func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	mm := h.effectiveModelMap()
	meta := h.dynamicModelsMeta()
	names := make([]string, 0, len(mm))
	for name := range mm {
		names = append(names, name)
	}
	// 稳定输出
	for i := 0; i < len(names)-1; i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	data := make([]map[string]any, 0, len(names))
	for _, name := range names {
		entry := map[string]any{
			"id":       name,
			"object":   "model",
			"created":  1753600000,
			"owned_by": "qoderwork",
		}
		// 动态模型的元数据（reasoning/vl/上下文）
		if m, ok := meta[name]; ok {
			entry["display_name"] = m.DisplayName
			entry["upstream_key"] = m.Key
			if m.IsReasoning {
				entry["reasoning"] = true
			}
			if m.IsVL {
				entry["vision"] = true
			}
			// 真实上下文上限：优先 context_config 中的最大 token_count，
			// 没有则回退 max_input_tokens（默认 180000）。
			maxCtx := m.MaxContextTokens()
			if maxCtx > 0 {
				entry["context_length"] = maxCtx
			}
			defCtx := m.DefaultContextTokens()
			if defCtx > 0 {
				entry["context_window"] = defCtx
			}
			if m.PriceFactor > 0 {
				entry["price_factor"] = m.PriceFactor
			}
		} else {
			entry["upstream_key"] = mm[name]
		}
		data = append(data, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// dynamicModelsMeta 返回 客户端名 → DynamicModel 的元数据索引（无动态时为空 map）。
func (h *Handler) dynamicModelsMeta() map[string]upstream.DynamicModel {
	h.modelsMu.RLock()
	defer h.modelsMu.RUnlock()
	out := make(map[string]upstream.DynamicModel, len(h.dynamicModels))
	for _, m := range h.dynamicModels {
		name := m.Key
		if m.DisplayName != "" {
			name = upstream.NormalizeModelName(m.DisplayName)
		}
		out[name] = m
	}
	return out
}

// effectiveModelMap 返回当前生效的 客户端名(display_name 规范化) → 上游 key 映射：
// 动态拉取上游模型列表（缓存 1h），失败回退内置静态表（KNOWLEDGE §6.2 实测）。
// 拉取失败记录时间戳进入 5min 负缓存，冷却期内直接用 fallback，避免反复打上游。
func (h *Handler) effectiveModelMap() map[string]string {
	h.modelsMu.RLock()
	if len(h.dynamicMap) > 0 && time.Since(h.dynamicFetched) < dynamicModelsTTL {
		m := h.dynamicMap
		h.modelsMu.RUnlock()
		return m
	}
	// 失败负缓存：冷却期内不再请求上游。
	if !h.lastFetchFail.IsZero() && time.Since(h.lastFetchFail) < modelsFetchFailCooldown {
		h.modelsMu.RUnlock()
		return fallbackModelMap()
	}
	h.modelsMu.RUnlock()

	// 拉动态
	acct := h.cfg.Pool.Pick()
	if acct == nil {
		return fallbackModelMap()
	}
	if err := acct.EnsureDT(h.cfg.Upstream.Base); err != nil {
		if cred.IsAuthInvalid(err) {
			h.cfg.Pool.Disable(acct.UID, "auth invalid (re-login required)")
		}
		h.recordFetchFail()
		return fallbackModelMap()
	}
	dyn, err := h.cfg.Upstream.FetchModels(acct)
	if err != nil || len(dyn) == 0 {
		h.recordFetchFail()
		return fallbackModelMap()
	}
	resolved := upstream.ResolveModelMap(dyn)
	h.modelsMu.Lock()
	h.dynamicModels = dyn
	h.dynamicMap = resolved
	h.dynamicFetched = time.Now()
	h.lastFetchFail = time.Time{} // 成功则清空负缓存
	h.modelsMu.Unlock()
	return resolved
}

// recordFetchFail 记录 models 拉取失败时间戳，进入负缓存冷却期。
func (h *Handler) recordFetchFail() {
	h.modelsMu.Lock()
	h.lastFetchFail = time.Now()
	h.modelsMu.Unlock()
}

// fallbackModelMap 内置静态模型表（上游模型接口 403 时的兜底）。
// 客户端名 = display_name 规范化，与动态路径命名一致。
func fallbackModelMap() map[string]string {
	return map[string]string{
		"auto":                "auto",
		"qwen3.8-max":         "qmodel_38max",
		"qwen3.7-max":         "qmodel_latest",
		"qwen3.7-plus":        "qmodel",
		"qwen3.6-flash":       "q36fmodel",
		"deepseek-v4-pro":     "dmodel",
		"deepseek-v4-flash":   "dfmodel",
		"glm-5.2":             "gm51model",
		"kimi-k2.7-code":      "kmodel",
		"minimax-m2.7":        "mmodel",
	}
}

type chatRequest struct {
	Model      string           `json:"model"`
	Messages   []map[string]any `json:"messages"`
	Stream     bool             `json:"stream"`
	Tools      []any            `json:"tools"`
	ToolChoice any              `json:"tool_choice"`
}

func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "parse json: "+err.Error())
		return
	}
	if req.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if len(req.Messages) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "messages is required")
		return
	}
	// 先确认有健康账号（凭证失效/全冷却时直接 503，不走模型校验）
	if h.cfg.Pool.Pick() == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_account", "all accounts unavailable (cooling/disabled)")
		return
	}
	modelKey, ok := h.effectiveModelMap()[strings.ToLower(req.Model)]
	if !ok {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_model", fmt.Sprintf("unknown model %q (see /v1/models)", req.Model))
		return
	}

	tried := map[string]bool{}
	var lastErr error
	for i := 0; i < h.cfg.MaxRotate; i++ {
		acct := h.cfg.Pool.PickExcluding(tried)
		if acct == nil {
			break
		}
		tried[acct.UID] = true

		// 确保 dt 有效
		if err := acct.EnsureDT(h.cfg.Upstream.Base); err != nil {
			lastErr = err
			if cred.IsAuthInvalid(err) {
				h.cfg.Pool.Disable(acct.UID, "auth invalid (re-login required)")
			} else {
				h.cfg.Pool.Cooldown(acct.UID, pool.CoolErr, h.cfg.ErrCooldown, "dt ensure: "+err.Error())
			}
			continue
		}
		acct.EnsureMachineFingerprint()

		rc, err := h.cfg.Upstream.ChatForward(acct, req.Messages, modelKey, req.Tools)
		if err != nil {
			var ue *upstream.Error
			if errors.As(err, &ue) {
				switch ue.Kind {
				case upstream.ErrHardCredit:
					h.cfg.Pool.Cooldown(acct.UID, pool.CoolHard, h.cfg.HardCooldown, "余额不足")
					lastErr = ue
					continue
				case upstream.ErrSoftRate:
					h.cfg.Pool.Cooldown(acct.UID, pool.CoolSoft, h.cfg.SoftCooldown, "429 rate limit")
					lastErr = ue
					continue
				case upstream.ErrTokenExpired:
					// 强制下次重新 exchange/refresh
					acct.DTExpiresAt = 0
					h.cfg.Pool.NoteError(acct.UID, h.cfg.ErrThreshold, h.cfg.ErrCooldown)
					lastErr = ue
					continue
				case upstream.ErrSessionDead:
					h.cfg.Pool.Disable(acct.UID, "session dead")
					lastErr = ue
					continue
				case upstream.ErrNotFound:
					// P2: 404 短冷却不累计 errCount（防雪崩）
					h.cfg.Pool.Cooldown(acct.UID, pool.CoolSoft, h.cfg.SoftCooldown, "upstream 404")
					lastErr = ue
					continue
				default:
					h.cfg.Pool.NoteError(acct.UID, h.cfg.ErrThreshold, h.cfg.ErrCooldown)
					lastErr = ue
					continue // P0: 轮转下一个账号，不直接返回（防雪崩）
				}
			}
			// 传输层错误
			h.cfg.Pool.NoteError(acct.UID, h.cfg.ErrThreshold, h.cfg.ErrCooldown)
			lastErr = err
			continue
		}
		defer rc.Close()
		h.cfg.Pool.NoteSuccess(acct.UID)

		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			fl, _ := w.(http.Flusher)
			flush := func() {
				if fl != nil {
					fl.Flush()
				}
			}
			_ = upstream.StreamAsOpenAI(w, rc, req.Model, flush)
			return
		}
		resp, err := upstream.AggregateNested(rc, req.Model)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "upstream_parse", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	msg := "all accounts unavailable (cooling/disabled)"
	if lastErr != nil {
		msg += ": " + lastErr.Error()
	}
	writeOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_account", msg)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "api_error",
			"code":    code,
		},
	})
}
