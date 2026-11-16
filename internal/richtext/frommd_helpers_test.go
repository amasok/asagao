package richtext

// FromMarkdown and PlainFromMarkdown are the conversions for a task's notes, which most
// tests check (the code that sends rich text names its Target).
func FromMarkdown(src string) string      { return TaskNotes.FromMarkdown(src) }
func PlainFromMarkdown(src string) string { return TaskNotes.PlainFromMarkdown(src) }
