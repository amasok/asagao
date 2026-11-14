package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// MaxNotifications is how many notifications per workspace sync keeps (newest first).
const MaxNotifications = 100

// maxTargetFetches bounds how many notification tasks one sync fetches on their own (tasks
// not in a synced project nor assigned to the user, e.g. ones the user only follows).
const maxTargetFetches = 50

// syncInbox fetches the notifications of a workspace. It returns false when the inbox
// cannot be used (no notifications:read scope, plan, ...): that is recorded for the front
// ends, which then show recent updates instead, and the sync goes on.
func (s *Syncer) syncInbox(ctx context.Context, workspaceGID string) (bool, error) {
	ns, err := s.API.Notifications(ctx, workspaceGID, MaxNotifications)
	if err != nil {
		if isRejected(err) || asana.IsPaymentRequired(err) {
			return false, s.Store.SetInboxState(ctx, inboxStateFor(err))
		}
		return false, err
	}
	if err := s.Store.ReplaceNotifications(ctx, workspaceGID, ns); err != nil {
		return false, err
	}
	return true, nil
}

// inboxStateFor classifies why Asana refused to list notifications.
func inboxStateFor(err error) store.InboxState {
	var apiErr *asana.Error
	st := store.InboxState{Status: store.InboxError, Error: err.Error()}
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			st.Status = store.InboxForbidden
		case http.StatusPaymentRequired:
			st.Status = store.InboxPayment
		}
	}
	return st
}

// InboxUnavailableMessage explains (in Japanese) why the inbox shows recent updates
// instead, and what to do about it. scopes is config auth.scopes (empty: full permissions).
func InboxUnavailableMessage(st store.InboxState, scopes []string) string {
	switch st.Status {
	case store.InboxOK:
		return ""
	case "":
		return "受信トレイ（Asana の通知）は次の同期で取得します。それまでは最近の更新を表示します"
	case store.InboxForbidden:
		if len(scopes) > 0 && !slices.Contains(scopes, "notifications:read") {
			return "受信トレイ（Asana の通知）を見るには、config.toml の auth.scopes と Asana のアプリ設定に " +
				"notifications:read と notifications:write を追加し、asana-tui auth login でログインし直してください。" +
				"それまでは最近の更新を表示します"
		}
		return "受信トレイ（Asana の通知）を見る権限がありません。asana-tui auth login でログインし直してください" +
			"（Asana のアプリ設定で通知の権限 notifications:read / notifications:write も許可してください）。それまでは最近の更新を表示します"
	case store.InboxPayment:
		return "このワークスペースのプランでは Asana の通知を API で取得できません。最近の更新を表示します"
	}
	return "Asana の通知を取得できませんでした（" + st.Error + "）。最近の更新を表示します"
}

// fetchNotificationTargets fetches tasks that notifications are about but the sync did not
// bring (keep: tasks already fetched by this sync). On a full sync the cached ones are
// fetched again, so the prune keeps them; otherwise only the missing ones are fetched.
func (s *Syncer) fetchNotificationTargets(ctx context.Context, keep map[string]bool, full bool, upsert func([]asana.Task) error) error {
	gids, err := s.Store.NotificationTargets(ctx)
	if err != nil {
		return err
	}
	fetched := 0
	for _, gid := range gids {
		if keep[gid] || fetched >= maxTargetFetches {
			continue
		}
		if !full {
			if _, err := s.Store.GetTask(ctx, gid); err == nil {
				continue
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		fetched++
		t, err := s.API.Task(ctx, gid)
		switch {
		case err == nil:
			if err := upsert([]asana.Task{*t}); err != nil {
				return err
			}
		case isRejected(err): // deleted, or not visible to us: the notification is still listed
		default:
			return fmt.Errorf("通知のタスクの取得 (%s): %w", gid, err)
		}
	}
	return nil
}

func (s *Syncer) pushMarkNotification(ctx context.Context, op store.Op) error {
	ws, _ := op.Payload["workspace"].(string)
	read, _ := op.Payload["read"].(bool)
	failed, err := s.API.MarkNotifications(ctx, ws, []string{op.TaskGID}, read)
	if err != nil {
		var apiErr *asana.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
			return fmt.Errorf("通知を既読・未読にする権限 (notifications:write) がありません。asana-tui auth login でログインし直してください: %w", err)
		}
		return err
	}
	if slices.Contains(failed, op.TaskGID) { // deleted or archived in Asana
		return fmt.Errorf("通知が Asana にありません: %w", &asana.Error{Status: http.StatusNotFound})
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	_ = s.Store.ApplyNotificationRead(ctx, op.TaskGID, read) // the next sync fixes a failure
	return nil
}
