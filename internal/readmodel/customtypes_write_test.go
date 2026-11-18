package readmodel

import (
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
)

func TestDescribeCustomTypeChange(t *testing.T) {
	op := outbox.Op{Kind: outbox.KindUpdateTask, Payload: map[string]any{"resource_subtype": "custom", "custom_type": "ct1", "custom_type_status_option": "cs1"}}
	got := DescribeOp(op)
	if !strings.Contains(got, "種類をカスタムタイプに変更") || !strings.Contains(got, "ステータスの変更") || strings.Contains(got, "種類を「") {
		t.Errorf("describe %q", got)
	}
}
