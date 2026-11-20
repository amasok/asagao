package tui

import (
	"context"
	"errors"
)

// attachmentSource downloads a task's attachment (or finds it downloaded before).
// *sync.Syncer is one.
type attachmentSource interface {
	DownloadAttachment(ctx context.Context, taskGID, gid string) (string, error)
}

// errNoAttachments is what an attachment comes to when there is neither a syncer nor a
// local one (Deps.LocalSyncer not set).
var errNoAttachments = errors.New("添付ファイルを取得できません（オフライン）")

// noAttachments finds no attachment.
type noAttachments struct{}

func (noAttachments) DownloadAttachment(context.Context, string, string) (string, error) {
	return "", errNoAttachments
}

// attachments is where attachments come from: the model's syncer, or offline the local
// syncer (Deps.LocalSyncer: what was downloaded before; in Deps.CacheDir when inCacheDir),
// or none at all.
func (m *Model) attachments(inCacheDir bool) attachmentSource {
	if m.syncer != nil {
		return m.syncer
	}
	if m.deps.LocalSyncer == nil {
		return noAttachments{}
	}
	s := m.deps.LocalSyncer()
	if inCacheDir {
		s.CacheDir = m.deps.CacheDir
	}
	return s
}
