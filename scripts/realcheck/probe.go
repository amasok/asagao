package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// probes reads the paid features and records how this plan answers: data (how many),
// or the error (402 Payment Required, …). Nothing is written.
func (c *Check) probes() {
	ctx := c.ctxOr()
	c.group("プランごとの機能 (読み取りだけ)")
	ws := "/workspaces/" + TestWorkspace
	var team []struct {
		GID string `json:"gid"`
	}
	if err := c.R.JSON(ctx, &team, "project", "teams"); err == nil && len(team) > 0 {
		c.team = team[0].GID
	}
	list := []struct {
		name, path string
		fields     []string
	}{
		{"カスタムフィールド (#1)", ws + "/custom_fields", []string{"opt_fields=name,resource_subtype"}},
		{"タスクテンプレート (#2)", "/task_templates", []string{"project=" + c.anyProject()}},
		{"プロジェクトテンプレート (#2)", "/project_templates", []string{"team=" + c.team}},
		{"ゴール (#7)", "/goals", []string{"workspace=" + TestWorkspace}},
		{"ポートフォリオ (#7)", "/portfolios", []string{"workspace=" + TestWorkspace, "owner=" + c.Me}},
		{"ワークロード allocations (#6)", "/allocations", []string{"workspace=" + TestWorkspace, "assignee=" + c.Me}},
		{"typeahead (#36)", ws + "/typeahead", []string{"resource_type=task", "query=a"}},
		{"検索 API tasks/search (#36)", ws + "/tasks/search", []string{"text=a"}},
		{"受信トレイ notifications (#1)", "/users/me/notifications", []string{"workspace=" + TestWorkspace}},
	}
	for _, p := range list {
		data, err := c.R.API(ctx, "GET", p.path, p.fields...)
		c.Rep.note(p.name, describe(data, err))
	}
}

func (c *Check) anyProject() string {
	if c.p != "" {
		return c.p
	}
	var ps []struct {
		GID string `json:"gid"`
	}
	_ = c.R.JSON(c.ctxOr(), &ps, "project", "list")
	if len(ps) > 0 {
		return ps[0].GID
	}
	return ""
}

// describe is "200 (n 件)" or the error text Asana gave.
func describe(data json.RawMessage, err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return "エラー: " + ae.Error()
	}
	if err != nil {
		return "エラー: " + err.Error()
	}
	var arr []json.RawMessage
	if json.Unmarshal(data, &arr) == nil {
		return fmt.Sprintf("成功 (%d 件)", len(arr))
	}
	return "成功"
}

// inbox records whether the notifications reached the cache through sync.
func (c *Check) inbox() {
	var n struct {
		Available     bool  `json:"available"`
		Unread        int   `json:"unread"`
		Notifications []any `json:"notifications"`
	}
	err := c.R.JSON(c.ctxOr(), &n, "inbox", "--limit", "20")
	c.Rep.check("受信トレイ (inbox) が PAT の sync で取れている", err == nil && n.Available, errText(err))
	c.Rep.note("受信トレイ (inbox)", fmt.Sprintf("%d 件、未読 %d (sync で取れたもの)", len(n.Notifications), n.Unread))
}
