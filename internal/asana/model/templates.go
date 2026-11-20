package model

import "time"

// TaskTemplate is a task template of a project (GET /task_templates).
type TaskTemplate struct {
	GID       string    `json:"gid"`
	Name      string    `json:"name"`
	Project   *Ref      `json:"project"`
	CreatedAt time.Time `json:"created_at"`
}

// ProjectTemplate is a project template (GET /project_templates).
type ProjectTemplate struct {
	GID         string `json:"gid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Team        *Ref   `json:"team"`
	Public      bool   `json:"public"`
	// RequestedDates are the dates the template asks for when a project is made from it
	// (a start date, say); each is given as {gid, value} to instantiateProject.
	RequestedDates []TemplateDate `json:"requested_dates"`
	// RequestedRoles are the roles the template asks to be filled with users.
	RequestedRoles []Ref `json:"requested_roles"`
}

// TemplateDate is one date a project template asks for.
type TemplateDate struct {
	GID         string `json:"gid"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Job is an asynchronous job of Asana (GET /jobs/{gid}): instantiating a template or
// duplicating a project or a task.
type Job struct {
	GID             string `json:"gid"`
	ResourceSubtype string `json:"resource_subtype"`
	// Status is one of the Job* constants below (JobNotStarted, ...).
	Status     string `json:"status"`
	NewTask    *Ref   `json:"new_task"`
	NewProject *Ref   `json:"new_project"`
}

// Statuses of a Job.
const (
	JobNotStarted = "not_started"
	JobInProgress = "in_progress"
	JobSucceeded  = "succeeded"
	JobFailed     = "failed"
)

// Done reports whether the job has ended (succeeded or failed).
func (j *Job) Done() bool { return j.Status == JobSucceeded || j.Status == JobFailed }

// ProjectFromTemplate is what instantiateProject is given.
type ProjectFromTemplate struct {
	Name    string
	TeamGID string // "" for the template's team
	Public  bool
	// Dates are the values of the template's requested dates (gid → YYYY-MM-DD).
	Dates map[string]string
	// Roles are the users of the template's requested roles (gid → user gid).
	Roles map[string]string
}
