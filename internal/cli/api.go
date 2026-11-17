package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/termsafe"
)

// rawAPI is the part of the Asana client `asagao api` uses (asana.Client.Raw).
type rawAPI interface {
	Raw(ctx context.Context, method, path string, body []byte) (status int, respBody []byte, err error)
}

type apiOpts struct {
	method    string
	rawFields []string
	fields    []string
	input     string
	paginate  bool
	dryRun    bool
}

// apiCmd is `asagao api` (#9): a request to the Asana API with the login's token, as
// `gh api` does for GitHub. It is the way out for what asagao does not do yet.
func (a *app) apiCmd() *cobra.Command {
	var o apiOpts
	cmd := &cobra.Command{
		Use:   "api <path>",
		Short: "Asana の API を、ログイン中の認証で直接呼ぶ",
		Long: `Asana の API (https://app.asana.com/api/1.0) を、ログイン中の認証で直接呼びます。
asagao にまだ無い操作の逃げ道です。

ローカルのキャッシュと outbox を通らず、その場で Asana に送ります。
-X POST / PUT / DELETE は Asana をすぐ書き換え、オフラインの取り消しや同期の照合はありません。
キャッシュに反映するには、あとで asagao sync を実行してください。

<path> は API の中のパスです (例: /users/me、/tasks/123?opt_fields=name)。
ほかのホストの URL や ".." は受け付けません。

フィールド (-f / -F) は、GET と DELETE ではクエリに、POST と PUT では本文の "data" に入ります。
  key=value      値は文字列 (-f)。-F では true / false / null / 数値をその型にする
  key[]=value    配列に足す (例: -f projects[]=123)
  a[b]=value     入れ子のオブジェクト (例: -f custom_fields[456]=高)
--input では本文の JSON をそのまま送ります ("-" で標準入力)。このときフィールドはクエリに入ります。

応答の JSON を整形して標準出力に出します。Asana がエラーを返したときも本文を出し、終了コードは 1 です。`,
		Example: `  asagao api /users/me
  asagao api /tasks/123 -f opt_fields=name,notes
  asagao api /workspaces/1/tasks/search -f text=買い物 --paginate
  asagao api -X PUT /tasks/123 -f name=新しい名前 -F completed=true
  asagao api -X POST /tasks --input task.json --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return a.runAPI(cmd, args[0], o) },
	}
	f := cmd.Flags()
	f.StringVarP(&o.method, "method", "X", http.MethodGet, "HTTP のメソッド (GET / POST / PUT / DELETE)")
	f.StringArrayVarP(&o.rawFields, "raw-field", "f", nil, "文字列のフィールド key=value (くり返せる)")
	f.StringArrayVarP(&o.fields, "field", "F", nil, "型付きのフィールド key=value。true / false / null / 数値はその型 (くり返せる)")
	f.StringVar(&o.input, "input", "", `本文の JSON のファイル ("-" で標準入力)。そのまま送る`)
	f.BoolVar(&o.paginate, "paginate", false, "GET で next_page をたどり、全ページの data を 1 つの配列にまとめて出す")
	f.BoolVar(&o.dryRun, "dry-run", false, "送らずに、送るリクエスト (メソッド・パス・本文) を表示する")
	return cmd
}

// apiRequest is what `asagao api` sends.
type apiRequest struct {
	method string
	path   string // "/tasks/1?opt_fields=name", checked by model.CheckRawPath
	body   []byte // nil: no body
}

func (a *app) runAPI(cmd *cobra.Command, path string, o apiOpts) error {
	req, err := buildAPIRequest(path, o, cmd.InOrStdin())
	if err != nil {
		return err
	}
	if o.dryRun {
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, req.method, req.path)
		if req.body != nil {
			a.writeAPIBody(cmd, req.body)
		}
		return nil
	}
	api, err := a.apiClient(cmd.Context())
	if err != nil {
		return err
	}
	rc, ok := api.(rawAPI)
	if !ok {
		return errors.New("この Asana クライアントは直接の呼び出しに対応していません")
	}
	if o.paginate {
		return a.apiPaginate(cmd, rc, req)
	}
	status, body, err := rc.Raw(cmd.Context(), req.method, req.path, req.body)
	if body != nil && (err == nil || status != 0) {
		a.writeAPIBody(cmd, body)
	}
	if err != nil {
		return err
	}
	return redirectError(status)
}

// redirectError is the error for a 3xx answer: Raw does not follow redirects, so that the
// token does not go elsewhere.
func redirectError(status int) error {
	if status >= 300 && status < 400 {
		return fmt.Errorf("asana: %d リダイレクトはたどりません", status)
	}
	return nil
}

// apiPaginate sends req (a GET) page by page, following next_page.offset, and prints
// {"data": [...]} with the data of every page.
func (a *app) apiPaginate(cmd *cobra.Command, rc rawAPI, req apiRequest) error {
	p, rawQuery, _ := strings.Cut(req.path, "?")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return fmt.Errorf("クエリを読めません: %w", err)
	}
	if q.Get("limit") == "" {
		q.Set("limit", "100") // Asana pages only a request with a limit
	}
	all := []json.RawMessage{}
	seen := map[string]bool{}
	for {
		status, body, err := rc.Raw(cmd.Context(), req.method, p+"?"+q.Encode(), nil)
		if err != nil {
			if body != nil && status != 0 {
				a.writeAPIBody(cmd, body)
			}
			return err
		}
		if err := redirectError(status); err != nil {
			return err
		}
		var page struct {
			Data     json.RawMessage `json:"data"`
			NextPage *struct {
				Offset string `json:"offset"`
			} `json:"next_page"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("asana: 応答を読めません: %w", err)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(page.Data, &items); err != nil {
			return errors.New("--paginate は、data が配列の応答 (一覧) にだけ使えます")
		}
		all = append(all, items...)
		if page.NextPage == nil || page.NextPage.Offset == "" {
			break
		}
		if seen[page.NextPage.Offset] {
			return errors.New("asana: 同じ offset が 2 回返ったので、ページをたどるのをやめました")
		}
		seen[page.NextPage.Offset] = true
		q.Set("offset", page.NextPage.Offset)
	}
	out, err := json.Marshal(map[string]any{"data": all})
	if err != nil {
		return err
	}
	a.writeAPIBody(cmd, out)
	return nil
}

// writeAPIBody prints a body Asana sent (or one about to be sent): JSON indented and with
// terminal control characters escaped (termsafe.JSON, still valid JSON), anything else
// through the termsafe writer.
func (a *app) writeAPIBody(cmd *cobra.Command, body []byte) {
	if out, ok := termsafe.JSON(body, "  "); ok {
		w := a.jsonOut
		if w == nil {
			w = cmd.OutOrStdout()
		}
		_, _ = w.Write(append(out, '\n'))
		return
	}
	if len(body) == 0 {
		return
	}
	s := string(body)
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	fmt.Fprint(cmd.OutOrStdout(), termsafe.String(s))
}

// buildAPIRequest turns the arguments of `asagao api` into the request to send.
func buildAPIRequest(path string, o apiOpts, stdin io.Reader) (apiRequest, error) {
	method := strings.ToUpper(o.method)
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		return apiRequest{}, fmt.Errorf("メソッドは GET / POST / PUT / DELETE のどれかにしてください: %q", o.method)
	}
	if !strings.HasPrefix(path, "/") {
		if p, _, _ := strings.Cut(path, "?"); strings.Contains(p, ":") {
			return apiRequest{}, fmt.Errorf("%w (URL ではなくパスを渡してください)", model.ErrRawPath)
		}
		path = "/" + path
	}
	if err := model.CheckRawPath(path); err != nil {
		return apiRequest{}, err
	}
	if o.paginate && method != http.MethodGet {
		return apiRequest{}, errors.New("--paginate は GET にだけ使えます")
	}

	data := map[string]any{}
	var query url.Values
	inQuery := method == http.MethodGet || method == http.MethodDelete || o.input != ""
	if inQuery {
		query = url.Values{}
	}
	add := func(kv string, typed bool) error {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return fmt.Errorf("フィールドは key=value の形にしてください: %q", kv)
		}
		if inQuery {
			query.Add(key, val)
			return nil
		}
		var v any = val
		if typed {
			v = typedValue(val)
		}
		return setField(data, key, v)
	}
	for _, kv := range o.rawFields {
		if err := add(kv, false); err != nil {
			return apiRequest{}, err
		}
	}
	for _, kv := range o.fields {
		if err := add(kv, true); err != nil {
			return apiRequest{}, err
		}
	}

	req := apiRequest{method: method, path: path}
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		req.path += sep + query.Encode()
	}
	switch {
	case o.input != "":
		if method == http.MethodGet || method == http.MethodDelete {
			return apiRequest{}, fmt.Errorf("%s には本文 (--input) を付けられません。-X POST か -X PUT を指定してください", method)
		}
		b, err := readInput(o.input, stdin)
		if err != nil {
			return apiRequest{}, err
		}
		if !json.Valid(b) {
			return apiRequest{}, fmt.Errorf("--input の中身が JSON として読めません: %s", o.input)
		}
		req.body = b
	case !inQuery && len(data) > 0:
		b, err := json.Marshal(map[string]any{"data": data})
		if err != nil {
			return apiRequest{}, err
		}
		req.body = b
	}
	return req, nil
}

func readInput(name string, stdin io.Reader) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(stdin)
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("--input を読めません: %w", err)
	}
	return b, nil
}

// typedValue is the value of a -F field: true, false, null and numbers as such, anything
// else as a string.
func typedValue(s string) any {
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if s != "" && (s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) && json.Valid([]byte(s)) {
		return json.Number(s) // a valid JSON text starting so is a number: sent as written
	}
	return s
}

// setField sets the field named key ("name", "projects[]", "custom_fields[123]",
// "a[b][]") in data to v.
func setField(data map[string]any, key string, v any) error {
	name, rest, bracket := strings.Cut(key, "[")
	if name == "" {
		return fmt.Errorf("フィールド名がありません: %q", key)
	}
	parts := []string{name}
	if bracket {
		rest = "[" + rest
		for rest != "" {
			if rest[0] != '[' {
				return fmt.Errorf("フィールド名の [] の形が正しくありません: %q", key)
			}
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return fmt.Errorf("フィールド名の [] の形が正しくありません: %q", key)
			}
			parts = append(parts, rest[1:end])
			rest = rest[end+1:]
		}
	}
	m := data
	for i, p := range parts {
		last := i == len(parts)-1
		if p == "" {
			// "[]": only at the end, after a name.
			if !last || i == 0 {
				return fmt.Errorf("[] は最後にだけ書けます: %q", key)
			}
			continue
		}
		appendNext := !last && parts[i+1] == "" && i+1 == len(parts)-1
		switch {
		case appendNext:
			cur, exists := m[p]
			arr, isArr := cur.([]any)
			if exists && !isArr {
				return fmt.Errorf("フィールド %q が、配列とそれ以外の両方で指定されています", key)
			}
			m[p] = append(arr, v)
			return nil
		case last:
			if _, exists := m[p]; exists {
				return fmt.Errorf("フィールド %q が 2 回指定されています", key)
			}
			m[p] = v
			return nil
		default:
			cur, exists := m[p]
			sub, isMap := cur.(map[string]any)
			if exists && !isMap {
				return fmt.Errorf("フィールド %q が、オブジェクトとそれ以外の両方で指定されています", key)
			}
			if !exists {
				sub = map[string]any{}
				m[p] = sub
			}
			m = sub
		}
	}
	return nil
}
