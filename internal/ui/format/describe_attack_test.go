package store

import (
	"reflect"
	"testing"
)

// A caller with plain text only need not pass a converter: nil is never called then.
func TestDiscardDetailsNilConverterPlainOnly(t *testing.T) {
	d := Discard{Kind: OpAddComment, Payload: map[string]any{"text": "平文"}}
	got := DiscardDetails(d, nil)
	if !reflect.DeepEqual(got, []DiscardDetail{{"コメント", "平文"}}) {
		t.Errorf("got %+v", got)
	}
}
