package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// MaxNotifications is how many notifications per workspace sync keeps (newest first).
const MaxNotifications = 100

// maxTargetFetches bounds how many notification tasks one sync fetches on their own (tasks
// not in a synced project nor assigned to the user, e.g. ones the user only follows).
const maxTargetFetches = 50

// syncInbox fetches the notifications of a workspace into the store.
func (s *Syncer) syncInbox(ctx context.Context, workspaceGID string) error {
	ns, err := s.API.Notifications(ctx, workspaceGID, MaxNotifications)
	if err != nil {
		return err
	}
	return s.Store.ReplaceNotifications(ctx, workspaceGID, ns)
}

// syncInboxes fetches the notifications of every workspace. The inbox is usable when one
// workspace answered; when none did because Asana refused (no notifications:read scope,
// plan, ...), that is recorded for the front ends, which show recent updates instead, and
// the sync goes on. An invalid login or a network error stops the sync like any other
// fetch; Asana having trouble (5xx, 429) keeps the cached notifications.
func (s *Syncer) syncInboxes(ctx context.Context, workspaces []asana.Workspace) error {
	ok := 0
	var refused store.InboxState
	for _, w := range workspaces {
		s.progress("ワークスペース %s: 受信トレイ", w.Name)
		err := s.syncInbox(ctx, w.GID)
		switch {
		case err == nil:
			ok++
		case asana.IsUnauthorized(err) || isNetworkError(err):
			return fmt.Errorf("%s の通知の取得: %w", w.Name, err)
		case isRejected(err):
			if refused.Status == "" {
				refused = inboxStateFor(err)
			}
		case isTemporary(err):
		default:
			return err
		}
	}
	switch {
	case ok > 0:
		if err := s.Store.SetInboxState(ctx, store.InboxState{Status: store.InboxOK}); err != nil {
			return err
		}
	case refused.Status != "":
		return s.Store.SetInboxState(ctx, refused)
	default:
		return nil
	}
	return s.probeInboxWrite(ctx)
}

// probeInboxWrite checks, while marking notifications is known to be refused, whether it
// works now (e.g. after logging in again with notifications:write): it marks one
// notification as Asana already has it, which changes nothing.
func (s *Syncer) probeInboxWrite(ctx context.Context) error {
	st, err := s.Store.InboxState(ctx)
	if err != nil || !st.WriteForbidden {
		return err
	}
	n, ok, err := s.Store.NotificationProbe(ctx)
	if err != nil || !ok {
		return err
	}
	if _, err := s.API.MarkNotifications(ctx, n.WorkspaceGID, []string{n.GID}, n.Read); err != nil {
		return nil // still refused, or Asana cannot tell now: try again next sync
	}
	return s.Store.SetInboxWriteForbidden(ctx, false)
}

// inboxStateFor classifies why Asana refused to list notifications.
func inboxStateFor(err error) store.InboxState {
	var apiErr *asana.Error
	st := store.InboxState{Status: store.InboxError, Error: err.Error()}
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusForbidden:
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

// InboxReadOnlyMessage explains (in Japanese) that notifications cannot be marked read /
// unread, and what to do about it. scopes is config auth.scopes.
func InboxReadOnlyMessage(scopes []string) string {
	if len(scopes) > 0 && !slices.Contains(scopes, "notifications:write") {
		return "通知を既読・未読にするには、config.toml の auth.scopes と Asana のアプリ設定に notifications:write を追加し、" +
			"asana-tui auth login でログインし直してください（通知を見ることはできます）"
	}
	return "通知を既読・未読にする権限がありません。Asana のアプリ設定で notifications:write を許可し、" +
		"asana-tui auth login でログインし直してください（通知を見ることはできます）"
}

// fetchNotificationTargets fetches tasks that notifications are about but the sync did not
// bring (keep: tasks already fetched by this sync), newest notifications first: on a full
// sync every one (so the prune keeps them); otherwise the ones not cached yet and those
// with a notification since the previous sync (since), which have changed. At most
// maxTargetFetches per sync; the others are kept as cached (added to keep).
func (s *Syncer) fetchNotificationTargets(ctx context.Context, keep map[string]bool, full bool, since time.Time, upsert func([]asana.Task) error) error {
	var after string
	if !since.IsZero() {
		after = since.UTC().Format(time.RFC3339)
	}
	gids, recent, err := s.Store.NotificationTargets(ctx, after)
	if err != nil {
		return err
	}
	fetched := 0
	for _, gid := range gids {
		if keep[gid] {
			continue
		}
		if fetched >= maxTargetFetches { // fetched by a later sync; until then the cached copy stays
			keep[gid] = true
			continue
		}
		if !full && !recent[gid] {
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
		case isRejected(err) && !asana.IsUnauthorized(err): // deleted, or not visible to us: the notification is still listed
		default:
			return fmt.Errorf("通知のタスクの取得 (%s): %w", gid, err)
		}
	}
	return nil
}

// pushMarkNotification marks op's notification read / unread, together with up to 49 other
// queued changes in the same direction (one request for "mark all read"). Notifications
// Asana reports as failed (deleted, archived) are dropped like other changes to deleted
// things: op's own by returning a 404, the others by putting them back to be sent alone.
// When Asana refuses to mark notifications at all (no notifications:write scope), every
// queued read / unread change is dropped and no more are queued until it works again
// (see probeInboxWrite), so they do not pile up as failures. batched is how many other
// entries were sent along.
func (s *Syncer) pushMarkNotification(ctx context.Context, op store.Op) (out outcome, batched int, err error) {
	ws, _ := op.Payload["workspace"].(string)
	read, _ := op.Payload["read"].(bool)
	peers, err := s.Store.PendingNotificationMarks(ctx, op, asana.MaxNotificationGIDs-1)
	if err != nil {
		return sent, batched, err
	}
	var batch []store.Op
	gids := []string{op.TaskGID}
	for _, p := range peers {
		if ok, err := s.Store.BeginSend(ctx, p.ID, s.clock()); err != nil {
			return sent, batched, err
		} else if ok {
			batch = append(batch, p)
			gids = append(gids, p.TaskGID)
		}
	}
	release := func() {
		for _, b := range batch {
			_ = s.Store.ReleaseOp(context.WithoutCancel(ctx), b.ID) // harmless to send again
		}
	}
	failed, err := s.API.MarkNotifications(ctx, ws, gids, read)
	if err != nil {
		release()
		var apiErr *asana.Error
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
			if err := s.Store.SetInboxWriteForbidden(ctx, true); err != nil {
				return sent, batched, err
			}
			if _, err := s.Store.DropNotificationMarks(ctx, op.ID); err != nil {
				return sent, batched, err
			}
			return dropped, 0, nil
		}
		return sent, batched, err
	}
	for _, b := range batch {
		if slices.Contains(failed, b.TaskGID) {
			_ = s.Store.ReleaseOp(context.WithoutCancel(ctx), b.ID) // sent alone next: dropped then
		} else {
			_ = s.Store.CompleteOp(ctx, b.ID) // on failure it is sent again (harmless)
			batched++
		}
	}
	if slices.Contains(failed, op.TaskGID) { // deleted or archived in Asana
		return sent, batched, fmt.Errorf("通知が Asana にありません: %w", &asana.Error{Status: http.StatusNotFound})
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, batched, sentNotSaved(err)
	}
	return sent, batched, nil
}
