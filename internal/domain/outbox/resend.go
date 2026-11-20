package outbox

import "github.com/amasok/asagao/internal/domain/gid"

// SafeToResend reports whether sending op twice has the same effect as sending it once.
// Creating things (tasks, comments, attachments, status updates, sections, tags, time
// tracking entries, enum options) is not.
//
// Adding a tag created offline first creates the tag. Once that is recorded only the
// (harmless) adding remains, so it depends on tagCreated, which reports whether the local
// tag has a real gid now. tagCreated is called only for such an entry.
//
// An entry of a kind this version does not know (written by a newer one) may be a create:
// it is not taken for safe, so it is checked before it is sent again. Neither is an add_tag
// whose payload cannot be read.
func SafeToResend(op Op, tagCreated func(localTag string) (bool, error)) (bool, error) {
	if spec, ok := specs[op.Kind]; !ok || spec.Creates {
		return false, nil
	}
	if op.Kind == KindAddTag {
		p, err := PayloadOf[AddTag](op)
		if err != nil {
			return false, nil
		}
		if !gid.IsLocal(p.Tag) {
			return true, nil
		}
		return tagCreated(p.Tag)
	}
	return true, nil
}
