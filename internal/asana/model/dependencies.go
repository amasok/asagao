package model

// TaskRef is a task referenced by another one (a dependency or dependent).
type TaskRef struct {
	GID             string `json:"gid"`
	Name            string `json:"name,omitempty"`
	Completed       bool   `json:"completed,omitempty"`
	ResourceSubtype string `json:"resource_subtype,omitempty"`
}
