package app

import (
	"context"
	"time"
)

// NotificationStore is the part of the local cache that marking inbox notifications read
// needs. *store.Store satisfies it.
type NotificationStore interface {
	ResolveNotification(ctx context.Context, arg string) (string, error)
	MarkNotificationsLocal(ctx context.Context, gids []string, read bool, now time.Time) (int, error)
}

// Notifications is the use cases on the inbox: marking notifications read or unread.
type Notifications struct {
	Store NotificationStore
	Clock Clock
}

// Resolve finds each notification by its gid or a unique end of it (the CLI shows short
// ids), without writing anything. The first that does not resolve is the store's error,
// as it is.
func (n *Notifications) Resolve(ctx context.Context, args []string) ([]string, error) {
	gids := make([]string, 0, len(args))
	for _, a := range args {
		gid, err := n.Store.ResolveNotification(ctx, a)
		if err != nil {
			return nil, err
		}
		gids = append(gids, gid)
	}
	return gids, nil
}

// Mark resolves the notifications (Resolve) and marks them read, or unread, as MarkHere
// does. Nothing is written when one does not resolve.
func (n *Notifications) Mark(ctx context.Context, args []string, read bool) (int, error) {
	gids, err := n.Resolve(ctx, args)
	if err != nil {
		return 0, err
	}
	return n.MarkHere(ctx, gids, read)
}

// MarkHere marks the notifications gids read (or unread) locally, in one write, and queues
// the change. Those already in that state are left alone; it returns how many changed.
func (n *Notifications) MarkHere(ctx context.Context, gids []string, read bool) (int, error) {
	return n.Store.MarkNotificationsLocal(ctx, gids, read, n.Clock())
}
