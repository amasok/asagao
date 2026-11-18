package tokenstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// OnePassword stores the refresh token (or a Personal Access Token) and client secret in a
// 1Password item via the `op` CLI.
//
// Only long-lived values are stored, so 1Password is written at login (or when Asana rotates
// the refresh token) and not on every hourly access-token refresh. Secrets are passed to `op`
// on stdin as a JSON item template, never as command-line arguments.
type OnePassword struct {
	Vault   string
	Item    string
	Account string

	// run executes op; replaced in tests.
	run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

const (
	fieldRefreshToken = "refresh_token"
	fieldPAT          = "personal_access_token"
	fieldClientSecret = "client_secret"
	fieldUserGID      = "user_gid"
	fieldUserName     = "user_name"
	fieldUserEmail    = "user_email"
)

type opItem struct {
	ID       string    `json:"id,omitempty"`
	Title    string    `json:"title"`
	Category string    `json:"category"`
	Fields   []opField `json:"fields"`
	// Keep everything else op returns so an edit does not drop it.
	Extra map[string]json.RawMessage `json:"-"`
}

type opField struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
	Value string `json:"value"`
}

func (o *OnePassword) Name() string            { return "1password" }
func (o *OnePassword) CachesAccessToken() bool { return false }

func (o *OnePassword) Load(ctx context.Context) (*Credentials, error) {
	item, err := o.get(ctx)
	if err != nil {
		return nil, err
	}
	c := &Credentials{
		PersonalAccessToken: item.field(fieldPAT),
		RefreshToken:        item.field(fieldRefreshToken),
		ClientSecret:        item.field(fieldClientSecret),
		UserGID:             item.field(fieldUserGID),
		UserName:            item.field(fieldUserName),
		UserEmail:           item.field(fieldUserEmail),
	}
	if c.RefreshToken == "" && c.PersonalAccessToken == "" {
		return nil, ErrNotFound
	}
	return c, nil
}

func (o *OnePassword) Save(ctx context.Context, c *Credentials) error {
	item, err := o.get(ctx)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if !exists {
		item = &opItem{Title: o.Item, Category: "API_CREDENTIAL"}
	}
	// Both are always written, so switching between OAuth and PAT clears the other one.
	item.set(fieldRefreshToken, "CONCEALED", c.RefreshToken)
	item.set(fieldPAT, "CONCEALED", c.PersonalAccessToken)
	item.set(fieldClientSecret, "CONCEALED", c.ClientSecret)
	item.set(fieldUserGID, "STRING", c.UserGID)
	item.set(fieldUserName, "STRING", c.UserName)
	item.set(fieldUserEmail, "STRING", c.UserEmail)

	body, err := item.marshal()
	if err != nil {
		return err
	}
	if exists {
		_, err = o.op(ctx, body, "item", "edit", item.ID, "--vault", o.Vault)
	} else {
		_, err = o.op(ctx, body, "item", "create", "--vault", o.Vault, "-")
	}
	return err
}

func (o *OnePassword) Delete(ctx context.Context) error {
	_, err := o.op(ctx, nil, "item", "delete", o.Item, "--vault", o.Vault, "--archive")
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func (o *OnePassword) get(ctx context.Context) (*opItem, error) {
	out, err := o.op(ctx, nil, "item", "get", o.Item, "--vault", o.Vault, "--format", "json", "--reveal")
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var item opItem
	if err := item.unmarshal(out); err != nil {
		return nil, fmt.Errorf("parse 1Password item: %w", err)
	}
	return &item, nil
}

func (o *OnePassword) op(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if o.Account != "" {
		args = append(args, "--account", o.Account)
	}
	if o.run != nil {
		return o.run(ctx, stdin, args...)
	}
	if testing.Testing() {
		// A test gives its own run: the real op would read or write the user's vault.
		return nil, errors.New("1Password CLI (op) is not run in tests (Issue #53)")
	}
	if _, err := exec.LookPath("op"); err != nil {
		return nil, errors.New("1Password CLI (op) not found: install it and enable the desktop app integration (https://developer.1password.com/docs/cli/get-started/)")
	}
	cmd := exec.CommandContext(ctx, "op", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, &opError{msg: strings.TrimSpace(stderr.String()), err: err}
	}
	return stdout.Bytes(), nil
}

type opError struct {
	msg string
	err error
}

func (e *opError) Error() string { return "op: " + e.msg }
func (e *opError) Unwrap() error { return e.err }

func isNotFound(err error) bool {
	var oe *opError
	return errors.As(err, &oe) && strings.Contains(oe.msg, "isn't an item")
}

func (i *opItem) field(label string) string {
	for _, f := range i.Fields {
		if f.Label == label || f.ID == label {
			return f.Value
		}
	}
	return ""
}

func (i *opItem) set(label, typ, value string) {
	for n, f := range i.Fields {
		if f.Label == label || f.ID == label {
			i.Fields[n].Value = value
			return
		}
	}
	if value == "" {
		return
	}
	i.Fields = append(i.Fields, opField{ID: label, Type: typ, Label: label, Value: value})
}

func (i *opItem) unmarshal(b []byte) error {
	type plain opItem
	if err := json.Unmarshal(b, (*plain)(i)); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &i.Extra); err != nil {
		return err
	}
	for _, k := range []string{"id", "title", "category", "fields"} {
		delete(i.Extra, k)
	}
	return nil
}

func (i *opItem) marshal() ([]byte, error) {
	m := map[string]any{}
	for k, v := range i.Extra {
		m[k] = v
	}
	if i.ID != "" {
		m["id"] = i.ID
	}
	m["title"] = i.Title
	m["category"] = i.Category
	m["fields"] = i.Fields
	return json.Marshal(m)
}
