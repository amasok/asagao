package model

// MaxBatchActions is how many requests Asana accepts in one POST /batch.
const MaxBatchActions = 10

// BatchWrite is one write sent with others in a POST /batch (asana.Client.Batch).
type BatchWrite struct {
	Method string         // "PUT" or "POST" (http.MethodPut, ...)
	Path   string         // relative to the API, e.g. "/tasks/123/addTag"
	Data   map[string]any // the request's "data"
	// Task: the response is a task. Its fields are asked for and it is decoded into
	// BatchWriteResult.Task.
	Task bool
}

// BatchWriteResult is Asana's answer to one BatchWrite.
type BatchWriteResult struct {
	Task *Task // the task in the response (BatchWrite.Task) when Err is nil
	Err  error // *Error when Asana answered this request with a status >= 400
}
