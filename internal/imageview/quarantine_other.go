//go:build !darwin

package imageview

// markQuarantine does nothing outside macOS (there is no quarantine attribute).
func markQuarantine(string) error { return nil }
