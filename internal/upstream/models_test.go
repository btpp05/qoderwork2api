package upstream

import (
	"testing"
)

func TestNormalizeModelName(t *testing.T) {
	cases := map[string]string{
		"Qwen3.8-Max":         "qwen3.8-max",
		"DeepSeek-V4-Pro":     "deepseek-v4-pro",
		"GLM-5.2":             "glm-5.2",
		"Kimi-K2.7-Code":      "kimi-k2.7-code",
		"auto":                "auto",
		"  Auto  ":            "auto",
		"MiniMax_M2.7":        "minimax-m2.7",
		"Qwen3.7-Plus":        "qwen3.7-plus",
	}
	for in, want := range cases {
		if got := NormalizeModelName(in); got != want {
			t.Errorf("NormalizeModelName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestResolveModelMapDisplayName(t *testing.T) {
	dynamic := []DynamicModel{
		{Key: "qmodel_38max", DisplayName: "Qwen3.8-Max", Enable: true},
		{Key: "dmodel", DisplayName: "DeepSeek-V4-Pro", Enable: true},
		{Key: "dfmodel", DisplayName: "DeepSeek-V4-Flash", Enable: true},
		{Key: "auto", DisplayName: "Auto", Enable: true},
		{Key: "newkey", DisplayName: "", Enable: true}, // 无 display_name → 用 key
	}
	m := ResolveModelMap(dynamic)
	if m["qwen3.8-max"] != "qmodel_38max" {
		t.Errorf("qwen3.8-max → %v", m["qwen3.8-max"])
	}
	if m["deepseek-v4-pro"] != "dmodel" {
		t.Errorf("deepseek-v4-pro → %v", m["deepseek-v4-pro"])
	}
	if m["auto"] != "auto" {
		t.Errorf("auto → %v", m["auto"])
	}
	if m["newkey"] != "newkey" {
		t.Errorf("newkey → %v (should fallback to key)", m["newkey"])
	}
}
