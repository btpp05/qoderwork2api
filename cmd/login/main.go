// login.go — QoderWork OAuth 设备授权登录（与 CPA 插件 /root/cliproxy-plugin/oauth.go
// 的 handleStartLogin + deviceTokenPoll 逐字一致的实现，CN realm）。
//
// 两个子命令，由 login.sh 顺序驱动：
//
//	login url   → 生成 PKCE+nonce+machineID，state 落 /tmp/qw2api-login-state.json，
//	              stdout 打印授权 URL
//	login poll  → 读 state，调 deviceToken/poll 一次，成功打印 token JSON
//
// 这样 login.sh 不需要 coproc/进程间管道，纯顺序 bash。
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// 与 /root/cliproxy-plugin/oauth.go:28-33 完全一致的常量（CN only）
const (
	oauthWebsiteCN   = "https://qoder.com.cn"
	oauthOpenapiCN   = "https://openapi.qoder.com.cn"
	oauthClientID    = "1c5e33e1-364d-4ce6-b02c-acaa81274a5c"
	oauthRedirectURI = "qoder-work-cn://"
	clientUA         = "Go-http-client/2.0" // 与 upstream.go:19 一致
	stateFile        = "/tmp/qw2api-login-state.json"
)

type loginState struct {
	Verifier  string `json:"verifier"`
	Nonce     string `json:"nonce"`
	MachineID string `json:"machine_id"`
	AuthURL   string `json:"auth_url"`
}

// makePKCE 与 oauth.go:47-60 逐字一致（含模 66 采样，保持一致不"修"）
func makePKCE() (verifier, challenge string) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	buf := make([]byte, 64)
	_, _ = rand.Read(buf)
	var sb strings.Builder
	sb.Grow(64)
	for _, b := range buf {
		sb.WriteByte(alphabet[int(b)%len(alphabet)])
	}
	verifier = sb.String()
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// buildAuthURL 与 oauth.go:81-82 逐字一致
func buildAuthURL(challenge, nonce, machineID string) string {
	return fmt.Sprintf("%s/device/selectAccounts?challenge=%s&challenge_method=S256&nonce=%s&machine_id=%s&client_id=%s&redirect_uri=%s",
		oauthWebsiteCN, challenge, nonce, machineID, oauthClientID, oauthRedirectURI)
}

// deviceTokenPoll 与 upstream.go:123-141 逐字一致
func deviceTokenPoll(nonce, verifier string) (map[string]any, bool, error) {
	url := fmt.Sprintf("%s/api/v1/deviceToken/poll?nonce=%s&verifier=%s&challenge_method=S256",
		oauthOpenapiCN, nonce, verifier)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", clientUA)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusAccepted {
		return nil, true, nil
	}
	if resp.StatusCode >= 400 {
		return nil, false, fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}
	var tok map[string]any
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}
	t, _ := tok["token"].(string)
	if t == "" {
		t, _ = tok["device_token"].(string)
	}
	if t == "" {
		return nil, true, nil
	}
	return tok, false, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "login: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fatal("usage: login <url|poll>")
	}
	switch os.Args[1] {
	case "url":
		verifier, challenge := makePKCE()
		nonce := uuid4()
		machineID := uuid4()
		st := loginState{
			Verifier:  verifier,
			Nonce:     nonce,
			MachineID: machineID,
			AuthURL:   buildAuthURL(challenge, nonce, machineID),
		}
		raw, _ := json.Marshal(st)
		if err := os.WriteFile(stateFile, raw, 0o600); err != nil {
			fatal("write state: %v", err)
		}
		fmt.Println(st.AuthURL)

	case "poll":
		raw, err := os.ReadFile(stateFile)
		if err != nil {
			fatal("read state: %v (先跑 login url)", err)
		}
		var st loginState
		if err := json.Unmarshal(raw, &st); err != nil {
			fatal("parse state: %v", err)
		}
		tok, pending, err := deviceTokenPoll(st.Nonce, st.Verifier)
		if err != nil {
			fatal("%v", err)
		}
		if pending {
			fatal("授权未完成（device token not found）。请确认已在浏览器完成授权再按 y")
		}
		token, _ := tok["token"].(string)
		if token == "" {
			token, _ = tok["device_token"].(string)
		}
		refresh, _ := tok["refresh_token"].(string)
		uid, _ := tok["user_id"].(string)
		expiresIn, _ := tok["expires_in"].(float64)
		out := map[string]any{
			"token":         token,
			"refresh_token": refresh,
			"user_id":       uid,
			"expires_in":    int64(expiresIn),
		}
		oraw, _ := json.Marshal(out)
		fmt.Println(string(oraw))
		os.Remove(stateFile)

	default:
		fatal("unknown subcommand %q (want url|poll)", os.Args[1])
	}
}
