// signin 一次性批量签到工具：遍历 ./auths/qoderwork-*.json 全部账号，
// 自动 EnsureDT（refresh 过期 token），逐个调 daily-check-in/claim，
// 顺手查 quota 打印剩余额度。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"qoderwork2api/internal/cred"
	"qoderwork2api/internal/upstream"
)

type row struct {
	file    string
	uid     string
	nick    string
	status  string // OK | ALREADY | FAIL | AUTH_INVALID | LOAD_ERR
	detail  string
	remain  int64
	hasQuota bool
}

func main() {
	dir := "auths"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	files, err := filepath.Glob(filepath.Join(dir, "qoderwork-*.json"))
	if err != nil || len(files) == 0 {
		fmt.Fprintf(os.Stderr, "no auth files in %s\n", dir)
		os.Exit(1)
	}
	sort.Strings(files)
	up := upstream.New()

	var rows []row
	okN, alreadyN, failN := 0, 0, 0
	for _, f := range files {
		r := row{file: filepath.Base(f)}
		c, err := cred.LoadFile(f)
		if err != nil {
			r.status, r.detail = "LOAD_ERR", err.Error()
			rows = append(rows, r)
			failN++
			continue
		}
		r.uid, r.nick = c.UID, c.Nickname
		dtBefore := c.DT
		if err := c.EnsureDT(up.Base); err != nil {
			if cred.IsAuthInvalid(err) {
				r.status = "AUTH_INVALID"
			} else {
				r.status = "FAIL"
			}
			r.detail = "ensure_dt: " + err.Error()
			rows = append(rows, r)
			failN++
			continue
		}
		// refresh 实际发生（DT 变化）→ 写回 auth 文件，避免重启后重复 refresh
		if c.DT != dtBefore {
			if err := c.SaveAtomic(); err != nil {
				fmt.Fprintf(os.Stderr, "save %s: %v\n", r.uid, err)
			}
		}
		ok, err := up.DailyCheckin(c.DT)
		switch {
		case err == nil && ok:
			r.status = "OK"
			okN++
		case err != nil:
			// DailyCheckin 把 4xx body 截短塞 error，"unknown"/409/NOT_ELIGIBLE 都可能是已签
			// 用 status 接口确认真实状态
			st, serr := up.CheckinStatus(c.DT)
			switch {
			case serr == nil && st.Status == "CLAIMED_TODAY":
				r.status = "ALREADY"
				r.detail = fmt.Sprintf("streak=%d total=%d", st.CurrentStreakDays, st.TotalClaimDays)
				alreadyN++
			case serr == nil && st.Status == "CLAIMABLE":
				// 今天还没签，但 claim 失败了 —— 真正的失败
				r.status = "FAIL"
				r.detail = "claim_failed: " + short(err.Error())
				failN++
			case isAlready(err.Error()):
				// status 也查不到，但 claim 返回 409/NOT_ELIGIBLE —— 已签或不可签
				r.status = "ALREADY"
				r.detail = short(err.Error())
				alreadyN++
			default:
				r.status = "FAIL"
				r.detail = short(err.Error())
				failN++
			}
		default:
			r.status = "FAIL"
			r.detail = "unknown false"
			failN++
		}
		// 顺手查 quota（不阻塞主流程）
		if remain, _, qerr := up.QuotaUsage(c.DT); qerr == nil {
			r.remain, r.hasQuota = remain, true
		}
		rows = append(rows, r)
	}

	// 报告
	fmt.Printf("uid                                  | nick        | status       | remain | detail\n")
	fmt.Printf("-------------------------------------+-------------+--------------+--------+------------------------------\n")
	for _, r := range rows {
		remain := "-"
		if r.hasQuota {
			remain = fmt.Sprintf("%d", r.remain)
		}
		fmt.Printf("%-36s | %-11s | %-12s | %-6s | %s\n",
			trunc(r.uid, 36), trunc(r.nick, 11), r.status, remain, r.detail)
	}
	fmt.Printf("\ntotal=%d ok=%d already=%d fail=%d\n", len(rows), okN, alreadyN, failN)
}

// 已签/不可签判定：409 / NOT_ELIGIBLE / already 字样
func isAlready(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "409") ||
		strings.Contains(s, "not_eligible") ||
		strings.Contains(s, "already") ||
		strings.Contains(s, "claimed")
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func short(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 60 {
		return s[:60]
	}
	return s
}
