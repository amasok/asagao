// Package auth implements the Asana OAuth 2.0 (PKCE) login and a persisting token source.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asagao/internal/auth/tokenstore"
	"github.com/amasok/asagao/internal/browser"
	"github.com/amasok/asagao/internal/config"
)

const (
	AuthURL  = "https://app.asana.com/-/oauth_authorize"
	TokenURL = "https://app.asana.com/-/oauth_token"
	// OOBRedirect makes Asana show the code on screen instead of redirecting (for SSH / headless use).
	OOBRedirect = "urn:ietf:wg:oauth:2.0:oob"
)

// OAuthConfig builds the oauth2 config. redirectURL is either the localhost callback or OOBRedirect.
func OAuthConfig(cfg config.AuthConfig, clientSecret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: clientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   AuthURL,
			TokenURL:  TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
		RedirectURL: redirectURL,
		Scopes:      cfg.Scopes,
	}
}

func LocalRedirectURL(port int) string { return fmt.Sprintf("http://localhost:%d/callback", port) }

// LoginOptions controls the interactive login.
type LoginOptions struct {
	// Manual uses the out-of-band redirect: the user opens the URL anywhere and pastes the code.
	Manual bool
	// NoBrowser prints the URL instead of opening a browser.
	NoBrowser bool
	// Prompt reads a line from the user (used for the pasted code in manual mode).
	Prompt func(label string) (string, error)
	// Out receives instructions for the user.
	Out func(format string, args ...any)
}

// Login runs the authorization code flow with PKCE and returns the token.
func Login(ctx context.Context, oc *oauth2.Config, opt LoginOptions) (*oauth2.Token, error) {
	verifier := oauth2.GenerateVerifier()
	state, err := randomString()
	if err != nil {
		return nil, err
	}
	authOpts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if len(oc.Scopes) == 0 {
		authOpts = append(authOpts, oauth2.SetAuthURLParam("scope", "default"))
	}

	var code string
	if opt.Manual {
		oc.RedirectURL = OOBRedirect
		u := authCodeURL(oc, state, authOpts)
		opt.Out("次の URL をブラウザで開き、表示されたコードを貼り付けてください:\n\n  %s\n\n", u)
		code, err = opt.Prompt("コード: ")
		if err != nil {
			return nil, err
		}
		code = strings.TrimSpace(code)
	} else {
		code, err = receiveCode(ctx, oc, state, authOpts, opt)
		if err != nil {
			return nil, err
		}
	}
	if code == "" {
		return nil, errors.New("認可コードが空です")
	}
	return oc.Exchange(ctx, code, oauth2.VerifierOption(verifier))
}

func receiveCode(ctx context.Context, oc *oauth2.Config, state string, authOpts []oauth2.AuthCodeOption, opt LoginOptions) (string, error) {
	addr := strings.TrimPrefix(strings.TrimSuffix(oc.RedirectURL, "/callback"), "http://")
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("コールバック用ポートを開けません (%s): %w。auth.redirect_port を変えるか --manual を使ってください", addr, err)
	}
	handler, results := callbackHandler(state)
	mux := http.NewServeMux()
	mux.Handle("/callback", handler)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	u := authCodeURL(oc, state, authOpts)
	if opt.NoBrowser || browser.Open(u) != nil {
		opt.Out("次の URL をブラウザで開いてください:\n\n  %s\n\n", u)
	} else {
		opt.Out("ブラウザでログインしてください（開かない場合: %s）\n", u)
	}

	select {
	case r := <-results:
		return r.code, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(5 * time.Minute):
		return "", errors.New("ログインがタイムアウトしました")
	}
}

type callbackResult struct {
	code string
	err  error
}

// callbackHandler handles the OAuth redirect to /callback. Requests whose state does not
// match (a stale tab, a forged link) get an error page and are ignored, so the login keeps
// waiting for the real callback. The first matching callback is reported on the returned
// channel; later ones (a reload, a second tab) only get an error page, so they never block
// on the channel nobody reads any more.
func callbackHandler(state string) (http.Handler, <-chan callbackResult) {
	ch := make(chan callbackResult, 1)
	var once sync.Once
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "asagao: state が一致しません。asagao に表示された URL からログインし直してください。", http.StatusBadRequest)
			return
		}
		first := false
		once.Do(func() { first = true })
		if !first {
			http.Error(w, "asagao: このログインはすでに処理されています。", http.StatusConflict)
			return
		}
		var res callbackResult
		if e := q.Get("error"); e != "" {
			http.Error(w, e, http.StatusBadRequest)
			res.err = fmt.Errorf("認可が拒否されました: %s %s", e, q.Get("error_description"))
		} else {
			fmt.Fprintln(w, "asagao: ログインしました。このタブは閉じて構いません。")
			res.code = q.Get("code")
		}
		ch <- res // buffered and sent once: never blocks
	})
	return h, ch
}

func randomString() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// UserFromToken extracts the user info Asana returns in the token response's "data" field.
func UserFromToken(tok *oauth2.Token) (gid, name, email string) {
	data, ok := tok.Extra("data").(map[string]any)
	if !ok {
		return "", "", ""
	}
	s := func(k string) string { v, _ := data[k].(string); return v }
	return s("gid"), s("name"), s("email")
}

// CredentialsFromToken converts a fresh login token into storable credentials.
func CredentialsFromToken(tok *oauth2.Token, clientSecret string) *tokenstore.Credentials {
	gid, name, email := UserFromToken(tok)
	return &tokenstore.Credentials{
		ClientSecret: clientSecret,
		RefreshToken: tok.RefreshToken,
		AccessToken:  tok.AccessToken,
		Expiry:       tok.Expiry,
		UserGID:      gid,
		UserName:     name,
		UserEmail:    email,
	}
}

// authCodeURL builds the authorize URL with spaces encoded as %20.
// Go's url.Values encodes spaces as "+", but Asana requires space-delimited scopes
// URL-encoded as %20; literal "+" is always escaped as %2B, so the replacement is safe.
func authCodeURL(oc *oauth2.Config, state string, opts []oauth2.AuthCodeOption) string {
	u := oc.AuthCodeURL(state, opts...)
	base, query, ok := strings.Cut(u, "?")
	if !ok {
		return u
	}
	return base + "?" + strings.ReplaceAll(query, "+", "%20")
}
