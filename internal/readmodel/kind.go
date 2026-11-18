package readmodel

// TaskKind is what a task's type can be changed with (#59): its resource_subtype, its
// custom type and status (nil when it has none), and the custom types it can be given.
type TaskKind struct {
	Subtype string
	Type    *CustomTypeRef
	Status  *CustomTypeStatusView
	Choices []CustomTypeView
}
