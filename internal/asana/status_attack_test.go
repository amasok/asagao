package asana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// A status update is posted with exactly one of text / html_text, and the body is never lost (#31).
func TestCreateStatusUpdateBodyAttack(t *testing.T) {
	for _, c := range []struct {
		name, text, html string
		wantKey, want    string
	}{
		{"html only", "plain", "<body>x</body>", "html_text", "<body>x</body>"},
		{"html empty falls back to text", "plain", "", "text", "plain"},
		{"html whitespace is still html_text", "plain", " ", "text", "plain"},
		{"both empty keeps text", "", "", "text", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got map[string]any
			cl := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				var b struct{ Data map[string]any }
				_ = json.NewDecoder(r.Body).Decode(&b)
				got = b.Data
				fmt.Fprint(w, `{"data":{"gid":"s1"}}`)
			})
			if _, err := cl.CreateStatusUpdate(context.Background(), "p1", "on_track", "t", c.text, c.html); err != nil {
				t.Fatal(err)
			}
			_, hasText := got["text"]
			_, hasHTML := got["html_text"]
			if hasText && hasHTML {
				t.Fatalf("both sent: %v", got)
			}
			if got[c.wantKey] != c.want {
				t.Fatalf("body %v, want %s=%q", got, c.wantKey, c.want)
			}
			if got["parent"] != "p1" || got["status_type"] != "on_track" || got["title"] != "t" {
				t.Fatalf("other fields lost: %v", got)
			}
		})
	}
}
