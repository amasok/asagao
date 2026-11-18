package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// Goals, portfolios and time periods (#7). Fetched when shown (sync.FetchGoals and the
// like); reading them never asks Asana.

func refGID(r *model.Ref) string {
	if r == nil {
		return ""
	}
	return r.GID
}

func refName(r *model.Ref) string {
	if r == nil {
		return ""
	}
	return r.Name
}

func userRef(gid, name string) *readmodel.UserRef {
	if gid == "" {
		return nil
	}
	return &readmodel.UserRef{GID: gid, Name: name}
}

// goalJSON encodes a goal's time period and metric for their columns ("" for none).
func goalJSON(g model.Goal) (period, metric string, err error) {
	if p := g.TimePeriod; p != nil && p.GID != "" {
		b, err := json.Marshal(readmodel.TimePeriodView{GID: p.GID, DisplayName: p.DisplayName, Period: p.Period, StartOn: p.StartOn, EndOn: p.EndOn})
		if err != nil {
			return "", "", err
		}
		period = string(b)
	}
	if m := g.Metric; m != nil {
		b, err := json.Marshal(readmodel.GoalMetric{Kind: m.ResourceSubtype, Currency: m.CurrencyCode, Initial: m.InitialNumberValue,
			Target: m.TargetNumberValue, Current: m.CurrentNumberValue, Display: m.CurrentDisplayValue, Source: m.ProgressSource,
			Precision: m.Precision})
		if err != nil {
			return "", "", err
		}
		metric = string(b)
	}
	return period, metric, nil
}

// upsertGoal writes the fields a goal list gives (not its rich notes or fetched_at).
func upsertGoal(ctx context.Context, tx *sql.Tx, workspaceGID string, position int, g model.Goal) error {
	period, metric, err := goalJSON(g)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO goals(gid, workspace_gid, position, name, notes, status, owner_gid, owner_name,
			team_name, workspace_level, start_on, due_on, time_period, metric) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET workspace_gid = excluded.workspace_gid, position = excluded.position, name = excluded.name,
			notes = excluded.notes, status = excluded.status, owner_gid = excluded.owner_gid, owner_name = excluded.owner_name,
			team_name = excluded.team_name, workspace_level = excluded.workspace_level, start_on = excluded.start_on,
			due_on = excluded.due_on, time_period = excluded.time_period, metric = excluded.metric`,
		g.GID, workspaceGID, position, g.Name, g.Notes, g.Status, refGID(g.Owner), refName(g.Owner), refName(g.Team),
		g.IsWorkspaceLevel, g.StartOn, g.DueOn, period, metric)
	return err
}

// ReplaceGoals stores the goals of a workspace, in Asana's order. Goals of the workspace
// that are not among them are gone from Asana (or from my view of it) and are deleted.
func (s *Store) ReplaceGoals(ctx context.Context, workspaceGID string, goals []model.Goal) error {
	keep := []string{}
	for _, g := range goals {
		keep = append(keep, g.GID)
	}
	keepJSON, err := json.Marshal(keep)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for i, g := range goals {
			if g.GID == "" {
				continue
			}
			if err := upsertGoal(ctx, tx, workspaceGID, i, g); err != nil {
				return err
			}
		}
		for _, q := range []string{
			"DELETE FROM goal_supports WHERE goal_gid IN (SELECT gid FROM goals WHERE workspace_gid = ? AND gid NOT IN (SELECT value FROM json_each(?)))",
			"DELETE FROM goals WHERE workspace_gid = ? AND gid NOT IN (SELECT value FROM json_each(?))",
		} {
			if _, err := tx.ExecContext(ctx, q, workspaceGID, string(keepJSON)); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveGoal stores one goal as fetched by itself (with its rich notes) and what supports
// it. A goal not listed yet goes after the listed ones of its workspace.
func (s *Store) SaveGoal(ctx context.Context, g model.Goal, supports []model.GoalRelationship, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		ws := refGID(g.Workspace)
		var position int
		var known string
		err := tx.QueryRowContext(ctx, "SELECT position, workspace_gid FROM goals WHERE gid = ?", g.GID).Scan(&position, &known)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(max(position) + 1, 0) FROM goals WHERE workspace_gid = ?", ws).Scan(&position); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if ws == "" {
			ws = known
		}
		if err := upsertGoal(ctx, tx, ws, position, g); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE goals SET html_notes = ?, fetched_at = ? WHERE gid = ?", g.HTMLNotes, formatTime(now), g.GID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM goal_supports WHERE goal_gid = ?", g.GID); err != nil {
			return err
		}
		for i, r := range supports {
			w := r.SupportingResource
			if w == nil || w.GID == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO goal_supports(goal_gid, position, gid, type, name, weight) VALUES(?, ?, ?, ?, ?, ?)",
				g.GID, i, w.GID, w.ResourceType, w.Name, r.ContributionWeight); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteGoal forgets a goal that Asana no longer has.
func (s *Store) DeleteGoal(ctx context.Context, gid string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{"DELETE FROM goal_supports WHERE goal_gid = ?", "DELETE FROM goals WHERE gid = ?"} {
			if _, err := tx.ExecContext(ctx, q, gid); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceTimePeriods stores the time periods of a workspace.
func (s *Store) ReplaceTimePeriods(ctx context.Context, workspaceGID string, periods []model.TimePeriod) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM time_periods WHERE workspace_gid = ?", workspaceGID); err != nil {
			return err
		}
		for _, p := range periods {
			if p.GID == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO time_periods(gid, workspace_gid, display_name, period, start_on, end_on)
				VALUES(?, ?, ?, ?, ?, ?)`, p.GID, workspaceGID, p.DisplayName, p.Period, p.StartOn, p.EndOn); err != nil {
				return err
			}
		}
		return nil
	})
}

const goalColumns = `gid, name, notes, html_notes, status, owner_gid, owner_name, team_name, workspace_level, start_on, due_on,
	time_period, metric, fetched_at`

func scanGoal(sc interface{ Scan(...any) error }) (readmodel.GoalDetail, error) {
	var d readmodel.GoalDetail
	var ownerGID, ownerName, period, metric string
	if err := sc.Scan(&d.GID, &d.Name, &d.Notes, &d.HTMLNotes, &d.Status, &ownerGID, &ownerName, &d.TeamName, &d.WorkspaceLevel,
		&d.StartOn, &d.DueOn, &period, &metric, &d.FetchedAt); err != nil {
		return d, err
	}
	d.Owner = userRef(ownerGID, ownerName)
	if period != "" {
		var p readmodel.TimePeriodView
		if err := termsafe.Unmarshal([]byte(period), &p); err == nil {
			d.TimePeriod = &p
		}
	}
	if metric != "" {
		var m readmodel.GoalMetric
		if err := termsafe.Unmarshal([]byte(metric), &m); err == nil {
			d.Metric = &m
		}
	}
	return d, nil
}

// ListGoals returns the cached goals, in Asana's order per workspace.
func (s *Store) ListGoals(ctx context.Context) ([]readmodel.GoalView, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+goalColumns+" FROM goals ORDER BY workspace_gid, position, gid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []readmodel.GoalView{}
	for rows.Next() {
		d, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d.GoalView)
	}
	return out, rows.Err()
}

// GoalDetail returns a cached goal and what supports it.
func (s *Store) GoalDetail(ctx context.Context, gid string) (*readmodel.GoalDetail, error) {
	d, err := scanGoal(s.db.QueryRowContext(ctx, "SELECT "+goalColumns+" FROM goals WHERE gid = ?", gid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("goal %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT gid, type, name, weight FROM goal_supports WHERE goal_gid = ? ORDER BY position", gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Supporting = []readmodel.GoalSupport{}
	for rows.Next() {
		var g readmodel.GoalSupport
		var w sql.NullFloat64
		if err := rows.Scan(&g.GID, &g.Type, &g.Name, &w); err != nil {
			return nil, err
		}
		if w.Valid {
			g.Weight = &w.Float64
		}
		d.Supporting = append(d.Supporting, g)
	}
	return &d, rows.Err()
}

// ListTimePeriods returns the cached time periods, the latest first and the longest
// first among those that start on the same day (a year before its halves and quarters).
func (s *Store) ListTimePeriods(ctx context.Context) ([]readmodel.TimePeriodView, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT gid, display_name, period, start_on, end_on FROM time_periods ORDER BY start_on DESC, end_on DESC, gid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []readmodel.TimePeriodView{}
	for rows.Next() {
		var p readmodel.TimePeriodView
		if err := rows.Scan(&p.GID, &p.DisplayName, &p.Period, &p.StartOn, &p.EndOn); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// resolveNamed finds the row of table whose gid or name is arg, else the only one whose
// name contains it (what names it in errors).
func (s *Store) resolveNamed(ctx context.Context, table, where, what, arg string) (string, error) {
	cond := ""
	if where != "" {
		cond = " AND " + where
	}
	var gid string
	err := s.db.QueryRowContext(ctx, "SELECT gid FROM "+table+" WHERE (gid = ? OR name = ?)"+cond+" LIMIT 1", arg, arg).Scan(&gid)
	if err == nil {
		return gid, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(arg) + "%"
	rows, err := s.db.QueryContext(ctx, "SELECT gid, name FROM "+table+` WHERE name LIKE ? ESCAPE '\'`+cond+" LIMIT 5", like)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var gids, names []string
	for rows.Next() {
		var g, n string
		if err := rows.Scan(&g, &n); err != nil {
			return "", err
		}
		gids, names = append(gids, g), append(names, n)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(gids) {
	case 0:
		return "", fmt.Errorf("%s %q: %w", what, arg, ErrNotFound)
	case 1:
		return gids[0], nil
	}
	return "", fmt.Errorf("%s %q matches %s: %w", what, arg, strings.Join(names, ", "), ErrAmbiguous)
}

// ResolveGoal returns the gid of the cached goal arg names (a gid, its name, or a unique
// part of its name).
func (s *Store) ResolveGoal(ctx context.Context, arg string) (string, error) {
	return s.resolveNamed(ctx, "goals", "", "goal", arg)
}

// --- portfolios ---

func upsertPortfolio(ctx context.Context, tx *sql.Tx, workspaceGID string, p model.Portfolio) error {
	var st model.StatusBrief
	if p.CurrentStatusUpdate != nil {
		st = *p.CurrentStatusUpdate
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO portfolios(gid, workspace_gid, name, color, owner_gid, owner_name, start_on, due_on,
			permalink_url, status_type, status_title) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gid) DO UPDATE SET workspace_gid = CASE WHEN excluded.workspace_gid <> '' THEN excluded.workspace_gid ELSE workspace_gid END,
			name = excluded.name, color = excluded.color, owner_gid = excluded.owner_gid, owner_name = excluded.owner_name,
			start_on = excluded.start_on, due_on = excluded.due_on, permalink_url = excluded.permalink_url,
			status_type = excluded.status_type, status_title = excluded.status_title`,
		p.GID, workspaceGID, p.Name, p.Color, refGID(p.Owner), refName(p.Owner), p.StartOn, p.DueOn, p.PermalinkURL, st.StatusType, st.Title)
	return err
}

// ReplacePortfolios stores my portfolios of a workspace, in Asana's order. Those I no
// longer own there leave the list (and are deleted unless another portfolio holds them).
func (s *Store) ReplacePortfolios(ctx context.Context, workspaceGID string, ps []model.Portfolio) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE portfolios SET mine = 0 WHERE workspace_gid = ?", workspaceGID); err != nil {
			return err
		}
		for i, p := range ps {
			if p.GID == "" {
				continue
			}
			if err := upsertPortfolio(ctx, tx, workspaceGID, p); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE portfolios SET mine = 1, position = ? WHERE gid = ?", i, p.GID); err != nil {
				return err
			}
		}
		return prunePortfolios(ctx, tx)
	})
}

// prunePortfolios deletes the portfolios that are neither mine nor held by one that is
// kept, with their items.
func prunePortfolios(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `WITH RECURSIVE kept(gid) AS (
			SELECT gid FROM portfolios WHERE mine = 1
			UNION SELECT i.gid FROM portfolio_items i JOIN kept k ON i.portfolio_gid = k.gid WHERE i.type = 'portfolio'
		)
		DELETE FROM portfolio_items WHERE portfolio_gid NOT IN (SELECT gid FROM kept)`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM portfolios WHERE mine = 0 AND gid NOT IN
		(SELECT gid FROM portfolio_items WHERE type = 'portfolio')`)
	return err
}

// SavePortfolio stores a portfolio as fetched by itself and its items, in order.
func (s *Store) SavePortfolio(ctx context.Context, p model.Portfolio, items []model.PortfolioItem, now time.Time) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := upsertPortfolio(ctx, tx, "", p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE portfolios SET fetched_at = ? WHERE gid = ?", formatTime(now), p.GID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM portfolio_items WHERE portfolio_gid = ?", p.GID); err != nil {
			return err
		}
		for i, it := range items {
			if it.GID == "" {
				continue
			}
			var st model.StatusBrief
			if it.CurrentStatusUpdate != nil {
				st = *it.CurrentStatusUpdate
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO portfolio_items(portfolio_gid, position, gid, type, name, color, archived,
					owner_gid, owner_name, start_on, due_on, permalink_url, status_type, status_title)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.GID, i, it.GID, it.ResourceType, it.Name, it.Color, it.Archived,
				refGID(it.Owner), refName(it.Owner), it.StartOn, it.DueOn, it.PermalinkURL, st.StatusType, st.Title); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeletePortfolio forgets a portfolio that Asana no longer has (or that I can no longer
// see), with its items.
func (s *Store) DeletePortfolio(ctx context.Context, gid string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{"DELETE FROM portfolio_items WHERE portfolio_gid = ?", "DELETE FROM portfolios WHERE gid = ?"} {
			if _, err := tx.ExecContext(ctx, q, gid); err != nil {
				return err
			}
		}
		return nil
	})
}

const portfolioColumns = "gid, name, color, owner_gid, owner_name, start_on, due_on, permalink_url, status_type, status_title, fetched_at"

func scanPortfolio(sc interface{ Scan(...any) error }) (readmodel.PortfolioDetail, error) {
	var d readmodel.PortfolioDetail
	var ownerGID, ownerName, statusType, statusTitle string
	if err := sc.Scan(&d.GID, &d.Name, &d.Color, &ownerGID, &ownerName, &d.StartOn, &d.DueOn, &d.PermalinkURL, &statusType,
		&statusTitle, &d.FetchedAt); err != nil {
		return d, err
	}
	d.Owner = userRef(ownerGID, ownerName)
	if statusType != "" {
		d.Status = &readmodel.StatusBrief{StatusType: statusType, Title: statusTitle}
	}
	return d, nil
}

// ListPortfolios returns my cached portfolios, in Asana's order per workspace.
func (s *Store) ListPortfolios(ctx context.Context) ([]readmodel.PortfolioView, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+portfolioColumns+" FROM portfolios WHERE mine = 1 ORDER BY workspace_gid, position, gid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []readmodel.PortfolioView{}
	for rows.Next() {
		d, err := scanPortfolio(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d.PortfolioView)
	}
	return out, rows.Err()
}

// PortfolioDetail returns a cached portfolio and its items.
func (s *Store) PortfolioDetail(ctx context.Context, gid string) (*readmodel.PortfolioDetail, error) {
	d, err := scanPortfolio(s.db.QueryRowContext(ctx, "SELECT "+portfolioColumns+" FROM portfolios WHERE gid = ?", gid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("portfolio %s: %w", gid, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT gid, type, name, color, archived, owner_gid, owner_name, start_on, due_on,
		permalink_url, status_type, status_title FROM portfolio_items WHERE portfolio_gid = ? ORDER BY position`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d.Items = []readmodel.PortfolioItem{}
	for rows.Next() {
		var it readmodel.PortfolioItem
		var ownerGID, ownerName, statusType, statusTitle string
		if err := rows.Scan(&it.GID, &it.Type, &it.Name, &it.Color, &it.Archived, &ownerGID, &ownerName, &it.StartOn, &it.DueOn,
			&it.PermalinkURL, &statusType, &statusTitle); err != nil {
			return nil, err
		}
		it.Owner = userRef(ownerGID, ownerName)
		if statusType != "" {
			it.Status = &readmodel.StatusBrief{StatusType: statusType, Title: statusTitle}
		}
		d.Items = append(d.Items, it)
	}
	return &d, rows.Err()
}

// ResolvePortfolio returns the gid of the cached portfolio arg names (a gid, its name, or
// a unique part of the name of one of mine).
func (s *Store) ResolvePortfolio(ctx context.Context, arg string) (string, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, "SELECT gid FROM portfolios WHERE gid = ?", arg).Scan(&gid)
	if err == nil {
		return gid, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return s.resolveNamed(ctx, "portfolios", "mine = 1", "portfolio", arg)
}
