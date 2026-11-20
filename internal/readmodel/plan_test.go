package readmodel

import (
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/plan"
)

func TestPlanUnavailableError(t *testing.T) {
	err := &PlanUnavailableError{Feature: plan.StartOn}
	if !strings.Contains(err.Error(), "タスクの開始日") {
		t.Fatalf("Error() = %q, want feature description", err.Error())
	}
}

// Every feature of domain/plan has its words (never leaving "では、を API で使えません" with
// an empty feature name), and the error says the same as the message.
func TestPlanUnavailableMessageNamesEveryFeature(t *testing.T) {
	allFeatures := []string{
		plan.Sections, plan.Tags, plan.MySection, plan.Search, plan.Typeahead, plan.CustomID, plan.TimeTracking,
		plan.Goals, plan.Portfolios, plan.CustomFields, plan.ProjectBrief, plan.StartOn, plan.CustomTypes,
		plan.TaskTemplates, plan.ProjectTemplates,
	}
	for _, f := range allFeatures {
		msg := PlanUnavailableMessage(f)
		if strings.Contains(msg, "では、を API") {
			t.Errorf("PlanUnavailableMessage(%q) produced empty feature description: %q", f, msg)
		}
		if errMsg := (&PlanUnavailableError{Feature: f}).Error(); errMsg != msg {
			t.Errorf("PlanUnavailableError(%q).Error() = %q, want %q", f, errMsg, msg)
		}
	}
}
