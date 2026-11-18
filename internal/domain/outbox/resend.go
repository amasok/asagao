package outbox

import "github.com/amasok/asagao/internal/domain/gid"

// SafeToResend reports whether sending op twice has the same effect as sending it once.
// Creating things (tasks, comments, attachments, status updates, sections, tags) is not.
//
// Adding a tag created offline first creates the tag. Once that is recorded only the
// (harmless) adding remains, so it depends on tagCreated, which reports whether the local
// tag has a real gid now. tagCreated is called only for such an entry.
func SafeToResend(op Op, tagCreated func(localTag string) (bool, error)) (bool, error) {
	switch op.Kind {
	case KindCreateTask, KindDuplicateTask, KindAddComment, KindUploadAttachment, KindCreateStatus, KindCreateSection:
		return false, nil
	case KindAddTag:
		tag, _ := op.Payload["tag"].(string)
		if !gid.IsLocal(tag) {
			return true, nil
		}
		return tagCreated(tag)
	}
	return true, nil
}
