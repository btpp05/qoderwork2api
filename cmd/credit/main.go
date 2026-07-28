// credit.go — QoderWork 积分查询（全部账号 + 总计），JSON 输出到 stdout。
//
// 用法:
//
//	go run ./cmd/credit        # 或编译后 ./credit
//
// 输出结构:
//
//	{"service":"qoderwork","ts":N,
//	 "total":{"remain":N,"accounts":N,"ok":N,"failed":N},
//	 "accounts":[{"uid","nickname","remain","exceeded","ok","error?"}]}
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const openapiBase = "https://openapi.qoder.com.cn"

type authFile struct {
	Auth struct {
		AccessToken string `json:"accessToken"`
	} `json:"auth"`
	Account struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
	} `json:"account"`
}

type accountResult struct {
	UID      string `json:"uid"`
	Nickname string `json:"nickname"`
	Remain   *int64 `json:"remain"`
	Size     *int64 `json:"size"`
	Exceeded *bool  `json:"exceeded"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

func quotaUsage(dt string) (remain, size int64, exceeded bool, err error) {
	req, err := http.NewRequest(http.MethodGet, openapiBase+"/api/v2/quota/usage", nil)
	if err != nil {
		return 0, 0, false, err
	}
	req.Header.Set("Authorization", "Bearer "+dt)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Go-http-client/2.0")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, 0, false, fmt.Errorf("http %d", resp.StatusCode)
	}
	var q struct {
		UserQuota struct {
			Total     float64 `json:"total"`
			Remaining float64 `json:"remaining"`
		} `json:"userQuota"`
		AddOnQuota struct {
			Total     float64 `json:"total"`
			Remaining float64 `json:"remaining"`
		} `json:"addOnQuota"`
		IsQuotaExceeded bool `json:"isQuotaExceeded"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		return 0, 0, false, err
	}
	return int64(q.UserQuota.Remaining + q.AddOnQuota.Remaining),
		int64(q.UserQuota.Total + q.AddOnQuota.Total),
		q.IsQuotaExceeded, nil
}

func main() {
	pretty := len(os.Args) > 1 && os.Args[1] == "-pretty"
	authDir := "./auths"
	if v := os.Getenv("QW2A_AUTH_DIR"); v != "" {
		authDir = v
	}
	files, _ := filepath.Glob(filepath.Join(authDir, "qoderwork-*.json"))
	sort.Strings(files)

	accounts := make([]accountResult, 0, len(files))
	for _, f := range files {
		var af authFile
		raw, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(raw, &af) != nil {
			continue
		}
		res := accountResult{UID: af.Account.UID, Nickname: af.Account.Nickname}
		if af.Auth.AccessToken == "" {
			res.Error = "no accessToken"
			accounts = append(accounts, res)
			continue
		}
		remain, size, exceeded, err := quotaUsage(af.Auth.AccessToken)
		if err != nil {
			res.Error = err.Error()
		} else {
			res.Remain = &remain
			res.Size = &size
			res.Exceeded = &exceeded
			res.OK = true
		}
		accounts = append(accounts, res)
		time.Sleep(200 * time.Millisecond)
	}

	var totalRemain, totalSize int64
	okCount := 0
	for _, a := range accounts {
		if a.OK {
			okCount++
			if a.Remain != nil {
				totalRemain += *a.Remain
			}
			if a.Size != nil {
				totalSize += *a.Size
			}
		}
	}
	out := map[string]any{
		"service": "qoderwork",
		"ts":      time.Now().Unix(),
		"total": map[string]any{
			"remain":   totalRemain,
			"size":     totalSize,
			"accounts": len(accounts),
			"ok":       okCount,
			"failed":   len(accounts) - okCount,
		},
		"accounts": accounts,
	}
	if pretty {
		printPretty(accounts, totalRemain, totalSize, okCount)
		return
	}
	raw, _ := json.Marshal(out)
	fmt.Println(string(raw))
}

// printPretty 人类可读日报：四行汇总，无账号明细。
func printPretty(accounts []accountResult, totalRemain, totalSize int64, okCount int) {
	withBalance := 0
	var failed []string
	for _, a := range accounts {
		if a.OK && a.Remain != nil && *a.Remain > 0 {
			withBalance++
		}
		if !a.OK {
			name := a.Nickname
			if name == "" && len(a.UID) >= 8 {
				name = a.UID[:8]
			}
			failed = append(failed, name+" "+a.Error)
		}
	}
	pct := int64(0)
	if totalSize > 0 {
		pct = totalRemain * 100 / totalSize
	}
	fmt.Printf("📊 QoderWork 积分日报\n")
	fmt.Printf("账号: %d/%d\n", withBalance, len(accounts))
	fmt.Printf("总计: %d/%d\n", totalRemain, totalSize)
	fmt.Printf("剩余: %d%%\n", pct)
	for _, f := range failed {
		fmt.Printf("⚠️ %s\n", f)
	}
}
