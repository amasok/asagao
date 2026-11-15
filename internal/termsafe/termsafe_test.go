package termsafe

import (
	"bytes"
	"encoding/json"
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

func TestJSON(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"name": "x" + osc52 + apc, "notes": "a\nb\tc\rd", "list": []string{"\u009b2J", `\u001b literal`}})
	got := JSON(string(raw))
	var v struct {
		Name  string   `json:"name"`
		Notes string   `json:"notes"`
		List  []string `json:"list"`
	}
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("%s: %v", got, err)
	}
	if !Safe(v.Name) || !Safe(v.Notes) || !Safe(v.List[0]) {
		t.Fatalf("control characters left: %q", v)
	}
	if v.Notes != "a\nb\tcd" || v.List[1] != `\u001b literal` || v.Name != "x]52;c;ZXZpbA==_Ga=T,f=100;AAAA\\" {
		t.Fatalf("got %q", v)
	}
	for _, s := range []string{"[メモ] \\u001b", `{"a":1}`, "plain"} {
		if JSON(s) != s {
			t.Errorf("JSON(%q) changed it", s)
		}
	}
}
