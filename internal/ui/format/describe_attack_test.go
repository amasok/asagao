package format

import (
	"reflect"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// A caller with plain text only need not pass a converter: nil is never called then.
func TestDiscardDetailsNilConverterPlainOnly(t *testing.T) {
	d := readmodel.Discard{Kind: outbox.KindAddComment, Payload: map[string]any{"text": "平文"}}
	got := DiscardDetails(d, nil)
	if !reflect.DeepEqual(got, []DiscardDetail{{"コメント", "平文"}}) {
		t.Errorf("got %+v", got)
	}
}
