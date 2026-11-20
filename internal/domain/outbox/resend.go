package outbox

import "github.com/amasok/asagao/internal/domain/gid"

// SafeToResend reports whether sending op twice has the same effect as sending it once.
// Creating things (tasks, comments, attachments, status updates, sections, tags, time
// tracking entries, enum options) is not.
//
// Adding a tag created offline first creates the tag. Once that is recorded only the
// (harmless) adding remains, so it depends on tagCreated, which reports whether the local
// tag has a real gid now. tagCreated is called only for such an entry.
func SafeToResend(op Op, tagCreated func(localTag string) (bool, error)) (bool, error) {
	if specs[op.Kind].Creates {
		return false, nil
	}
	if op.Kind == KindAddTag {
		tag, _ := op.Payload["tag"].(string)
		if !gid.IsLocal(tag) {
			return true, nil
		}
		return tagCreated(tag)
	}
	return true, nil
}
