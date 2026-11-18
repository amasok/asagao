//go:build !race

package store

// raceEnabled: the tests run with -race (much slower), for time limits.
const raceEnabled = false
