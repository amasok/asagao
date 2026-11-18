package readmodel

// Features of templates (#2) that a plan may lack. Their 402 is recorded per workspace
// (PlanKey) and hides them only for PlanRecheckAfter (plan.go).
const (
	PlanTaskTemplates    = "task_templates"    // making tasks from task templates
	PlanProjectTemplates = "project_templates" // making projects from project templates
)
