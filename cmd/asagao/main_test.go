package main

import (
	"reflect"
	"testing"
)

// Every field of the wiring is set: a missing one would panic when a command uses it.
func TestWiringIsComplete(t *testing.T) {
	v := reflect.ValueOf(wiring())
	for i := range v.NumField() {
		if v.Field(i).IsNil() {
			t.Errorf("cli.Wiring.%s is not set", v.Type().Field(i).Name)
		}
	}
}
