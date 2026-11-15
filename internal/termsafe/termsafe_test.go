package termsafe

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

const (
	osc52 = "\x1b]52;c;ZXZpbA==\x07"             // write "evil" to the clipboard
	osc8  = "\x1b]8;;https://evil.example\x1b\\" // fake link
	apc   = "\x1b_Ga=T,f=100;AAAA\x1b\\"         // kitty graphics command
	csi   = "\x1b[2J\u009b2J"                    // clear the screen (7-bit and C1 forms)
)

func TestString(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ソファを買う", "ソファを買う"},
		{"a\nb\tc", "a\nb\tc"},
		{"名前" + osc52, "名前]52;c;ZXZpbA=="},
		{osc8 + "x", "]8;;https://evil.example\\x"},
		{apc, "_Ga=T,f=100;AAAA\\"},
		{csi, "[2J2J"},
		{"a\rb\x7fc\x00d\u0085e\u009df", "abcdef"},
		{"bad\xffutf8", "bad�utf8"},
		{"raw C1 byte \x9b2J", "raw C1 byte �2J"},
		{"keep �", "keep �"},
	} {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q) = %q, want %q", c.in, got, c.want)
		}
		if !Safe(String(c.in)) {
			t.Errorf("String(%q) is not Safe", c.in)
		}
	}
}

func TestWriter(t *testing.T) {
	var b bytes.Buffer
	w := Writer(&b)
	in := []byte("タスク" + apc + osc52 + "\n")
	// one byte at a time: multibyte characters split across writes must survive
	for i := range in {
		if n, err := w.Write(in[i : i+1]); n != 1 || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got, want := b.String(), "タスク_Ga=T,f=100;AAAA\\]52;c;ZXZpbA==\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestUnmarshal(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"name": "x" + osc52 + apc, "list": []any{"a\rb", map[string]any{"k" + csi: "v\x1b"}}})
	var m map[string]any
	if err := Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "x]52;c;ZXZpbA==_Ga=T,f=100;AAAA\\", "list": []any{"ab", map[string]any{"k[2J2J": "v"}}}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("got %q, want %q", m, want)
	}

	type opt struct {
		Name string
		Sub  *struct{ Notes []string }
		any  string // unexported: left alone
	}
	var o []opt
	if err := Unmarshal([]byte(`[{"Name":"a\u001b[31m","Sub":{"Notes":["\u009b2J","ok\n"]}}]`), &o); err != nil {
		t.Fatal(err)
	}
	if o[0].Name != "a[31m" || o[0].Sub.Notes[0] != "2J" || o[0].Sub.Notes[1] != "ok\n" {
		t.Fatalf("got %+v %q", o[0], o[0].Sub.Notes)
	}

	var v any
	if err := Unmarshal([]byte(`"\u001b]52;c;Zm9v\u0007"`), &v); err != nil || v != "]52;c;Zm9v" {
		t.Fatalf("got %q, %v", v, err)
	}
}
