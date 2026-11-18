package readmodel

import "time"

// A feature the workspace's plan lacks (Asana answered 402) is recorded per workspace, so
// a 402 in a free workspace does not hide the feature in a paid organization, and only
// for PlanRecheckAfter: it is then offered and tried again, so a workspace whose plan was
// upgraded (or whose trial started) gets it back without anyone clearing anything. (The
// older records of sections, tags and my_section are KeyPlanUnavailable + feature: #50.)

// PlanRecheckAfter is how long a 402 hides a feature.
const PlanRecheckAfter = 24 * time.Hour

// PlanKey is the sync_state key that records when Asana refused feature in a workspace
// with a 402 (the value is the time, RFC 3339).
func PlanKey(feature, workspaceGID string) string {
	return KeyPlanUnavailable + feature + ":" + workspaceGID
}

// PlanOffFor reports whether a PlanKey value still hides its feature at now: for
// PlanRecheckAfter. A value that is not a time (or one in the future, from a clock that
// was ahead) does not hide it, so the feature is tried again rather than hidden for good.
func PlanOffFor(value string, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	age := now.Sub(at)
	return age >= 0 && age < PlanRecheckAfter
}
