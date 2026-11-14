package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/auth/tokenstore"
	"github.com/amasok/asana-tui/internal/config"
)

type memStore struct {
	caches  bool
	saved   []tokenstore.Credentials
	creds   *tokenstore.Credentials // returned by Load (nil: ErrNotFound)
	loadErr error
	saveErr error
	loads   int
}

func (m *memStore) Name() string            { return "mem" }
func (m *memStore) CachesAccessToken() bool { return m.caches }
func (m *memStore) Load(context.Context) (*tokenstore.Credentials, error) {
	m.loads++
	switch {
	case m.loadErr != nil:
		return nil, m.loadErr
	case m.creds == nil:
		return nil, tokenstore.ErrNotFound
	}
	c := *m.creds
	return &c, nil
}
func (m *memStore) Save(_ context.Context, c *tokenstore.Credentials) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, *c)
	saved := *c
	m.creds = &saved
	return nil
}
func (m *memStore) Delete(context.Context) error { return nil }

type seqSource []*oauth2.Token

func (s *seqSource) Token() (*oauth2.Token, error) {
	t := (*s)[0]
	if len(*s) > 1 {
		*s = (*s)[1:]
	}
	return t, nil
}

func TestPersistingSource(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	tokens := func() *seqSource {
		return &seqSource{
			{AccessToken: "at1", RefreshToken: "rt1", Expiry: exp}, // same as stored
			{AccessToken: "at2", RefreshToken: "rt1", Expiry: exp}, // hourly refresh
			{AccessToken: "at2", RefreshToken: "rt1", Expiry: exp}, // cached
			{AccessToken: "at3", RefreshToken: "rt2", Expiry: exp}, // rotation
		}
	}
	for _, tc := range []struct {
		name   string
		caches bool
		want   []string // saved refresh/access pairs
	}{
		{"keychain saves every new access token", true, []string{"rt1/at2", "rt2/at3"}},
		{"1password saves only on rotation", false, []string{"rt2/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds := tokenstore.Credentials{RefreshToken: "rt1", AccessToken: "at1", Expiry: exp}
			st := &memStore{caches: tc.caches, creds: &creds}
			p := &persistingSource{ctx: context.Background(), base: tokens(), store: st, creds: creds}
			for range 4 {
				if _, err := p.Token(); err != nil {
					t.Fatal(err)
				}
			}
			var got []string
			for _, c := range st.saved {
				got = append(got, c.RefreshToken+"/"+c.AccessToken)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("saved %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("saved %v, want %v", got, tc.want)
				}
			}
		})
	}
}

type errSource struct{ err error }

func (s errSource) Token() (*oauth2.Token, error) { return nil, s.err }

func TestPersistingSourceRefreshErrors(t *testing.T) {
	other := errors.New("network down")
	for _, tc := range []struct {
		name        string
		err         error
		notLoggedIn bool
	}{
		{"invalid_grant means the login is gone", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, true},
		{"other OAuth errors pass through", &oauth2.RetrieveError{ErrorCode: "invalid_client"}, false},
		{"other errors pass through", other, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &memStore{caches: true}
			p := &persistingSource{ctx: context.Background(), base: errSource{tc.err}, store: st,
				creds: tokenstore.Credentials{RefreshToken: "rt1"}}
			tok, err := p.Token()
			if tok != nil || err == nil {
				t.Fatalf("Token() = %v, %v; want an error", tok, err)
			}
			if got := errors.Is(err, ErrNotLoggedIn); got != tc.notLoggedIn {
				t.Fatalf("errors.Is(%v, ErrNotLoggedIn) = %v, want %v", err, got, tc.notLoggedIn)
			}
			if !tc.notLoggedIn && !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want the original error %v", err, tc.err)
			}
			if len(st.saved) != 0 {
				t.Fatalf("nothing should be saved: %v", st.saved)
			}
		})
	}
}

// When saving a new token fails, Token reports the error and keeps the old credentials, so
// the next call tries to save again instead of forgetting the rotated refresh token.
func TestPersistingSourceSaveFailure(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name   string
		caches bool
		tok    oauth2.Token
		want   string
	}{
		{"rotation, 1password", false, oauth2.Token{AccessToken: "at2", RefreshToken: "rt2", Expiry: exp}, "rt2/"},
		{"rotation, keychain", true, oauth2.Token{AccessToken: "at2", RefreshToken: "rt2", Expiry: exp}, "rt2/at2"},
		{"refresh, keychain", true, oauth2.Token{AccessToken: "at2", Expiry: exp}, "rt1/at2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveErr := errors.New("keychain locked")
			creds := tokenstore.Credentials{RefreshToken: "rt1", AccessToken: "at1", Expiry: exp, UserGID: "9"}
			st := &memStore{caches: tc.caches, saveErr: saveErr, creds: &creds}
			tok := tc.tok
			p := &persistingSource{ctx: context.Background(), base: &seqSource{&tok}, store: st, creds: creds}
			if got, err := p.Token(); got != nil || !errors.Is(err, saveErr) {
				t.Fatalf("Token() = %v, %v; want the save error", got, err)
			}
			if p.creds.RefreshToken != "rt1" || p.creds.AccessToken != "at1" {
				t.Fatalf("creds changed after a failed save: %+v", p.creds)
			}
			st.saveErr = nil
			got, err := p.Token()
			if err != nil || got.AccessToken != "at2" {
				t.Fatalf("retry Token() = %v, %v", got, err)
			}
			if len(st.saved) != 1 {
				t.Fatalf("saved %d times, want 1 (the retry)", len(st.saved))
			}
			c := st.saved[0]
			if c.RefreshToken+"/"+c.AccessToken != tc.want || c.UserGID != "9" {
				t.Fatalf("saved %+v, want %s keeping the user", c, tc.want)
			}
			if _, err := p.Token(); err != nil || len(st.saved) != 1 {
				t.Fatalf("third call: err %v, saved %d times (want no new save)", err, len(st.saved))
			}
		})
	}
}

func TestTokenSource(t *testing.T) {
	ctx := context.Background()
	cfg := config.AuthConfig{ClientID: "cid", RedirectPort: 8976}

	t.Run("ASANA_TOKEN wins", func(t *testing.T) {
		t.Setenv(EnvToken, "pat")
		st := &memStore{loadErr: errors.New("must not load")}
		ts, err := TokenSource(ctx, config.AuthConfig{}, st)
		if err != nil {
			t.Fatal(err)
		}
		tok, err := ts.Token()
		if err != nil || tok.AccessToken != "pat" || tok.TokenType != "Bearer" {
			t.Fatalf("Token() = %+v, %v", tok, err)
		}
		if st.loads != 0 {
			t.Fatal("store should not be read when ASANA_TOKEN is set")
		}
	})

	t.Setenv(EnvToken, "")
	t.Run("no client id with OAuth credentials", func(t *testing.T) {
		st := &memStore{creds: &tokenstore.Credentials{RefreshToken: "rt"}}
		_, err := TokenSource(ctx, config.AuthConfig{}, st)
		if err == nil || errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("err = %v, want a client_id error", err)
		}
	})
	t.Run("nothing stored and no client id", func(t *testing.T) {
		if _, err := TokenSource(ctx, config.AuthConfig{}, &memStore{}); !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("err = %v, want ErrNotLoggedIn", err)
		}
	})
	t.Run("stored PAT needs no client id and is never refreshed", func(t *testing.T) {
		st := &memStore{caches: true, creds: &tokenstore.Credentials{PersonalAccessToken: "pat", ClientSecret: "old-secret"}}
		src, err := TokenSource(ctx, config.AuthConfig{}, st)
		if err != nil {
			t.Fatal(err)
		}
		if src.Method != tokenstore.MethodPAT || src.FromEnv {
			t.Fatalf("source = %+v, want a stored PAT", src)
		}
		for range 2 {
			tok, err := src.Token()
			if err != nil || tok.AccessToken != "pat" || tok.TokenType != "Bearer" || !tok.Expiry.IsZero() || tok.RefreshToken != "" {
				t.Fatalf("Token() = %+v, %v", tok, err)
			}
		}
		if len(st.saved) != 0 {
			t.Fatalf("a PAT must never be saved back: %v", st.saved)
		}
		if h := src.UnauthorizedHint(); !strings.Contains(h, "auth login --pat") {
			t.Fatalf("hint = %q, want the PAT login command", h)
		}
	})
	t.Run("nothing stored", func(t *testing.T) {
		if _, err := TokenSource(ctx, cfg, &memStore{}); !errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("err = %v, want ErrNotLoggedIn", err)
		}
	})
	t.Run("store error", func(t *testing.T) {
		loadErr := errors.New("op: not signed in")
		_, err := TokenSource(ctx, cfg, &memStore{loadErr: loadErr})
		if !errors.Is(err, loadErr) || errors.Is(err, ErrNotLoggedIn) {
			t.Fatalf("err = %v, want the store error", err)
		}
	})
	t.Run("valid stored token is used without refreshing", func(t *testing.T) {
		st := &memStore{caches: true, creds: &tokenstore.Credentials{
			RefreshToken: "rt", AccessToken: "at", Expiry: time.Now().Add(time.Hour)}}
		ts, err := TokenSource(ctx, cfg, st)
		if err != nil {
			t.Fatal(err)
		}
		tok, err := ts.Token()
		if err != nil || tok.AccessToken != "at" || tok.RefreshToken != "rt" {
			t.Fatalf("Token() = %+v, %v", tok, err)
		}
		if len(st.saved) != 0 {
			t.Fatalf("unchanged token should not be saved: %v", st.saved)
		}
	})
}

func TestLoginHelp(t *testing.T) {
	patHint := (&Source{Method: tokenstore.MethodPAT}).UnauthorizedHint()
	for _, tc := range []struct {
		name string
		err  error
		want string // substring; "" = not a login problem
	}{
		{"nil", nil, ""},
		{"other", errors.New("boom"), ""},
		{"403", &asana.Error{Status: 403}, ""},
		{"not logged in", fmt.Errorf("x: %w", ErrNotLoggedIn), "auth login --pat"},
		{"401 with hint", fmt.Errorf("ユーザー情報の取得: %w", &asana.Error{Status: 401, Hint: patHint}), patHint},
		{"401 without hint", &asana.Error{Status: 401}, "auth login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := LoginHelp(tc.err)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("LoginHelp(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// A running OAuth session must not overwrite a login made meanwhile in another process
// (auth login --pat, another OAuth login): it switches to the same user's login instead.
// After a logout it stops with ErrNotLoggedIn and does not save the old login back.
func TestPersistingSourceAdoptsNewLogin(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	ours := tokenstore.Credentials{RefreshToken: "rt1", AccessToken: "at1", Expiry: exp, UserGID: "1"}
	refreshed := func() *seqSource { return &seqSource{{AccessToken: "at2", RefreshToken: "rt1", Expiry: exp}} }
	for _, tc := range []struct {
		name    string
		base    oauth2.TokenSource
		stored  *tokenstore.Credentials // what another process saved meanwhile (nil: logged out)
		want    string                  // access token now used ("" = ErrNotLoggedIn)
		newBase bool                    // the OAuth source is rebuilt for the stored login
	}{
		{"PAT saved meanwhile (refresh)", refreshed(), &tokenstore.Credentials{PersonalAccessToken: "pat", UserGID: "1"}, "pat", false},
		{"PAT saved meanwhile (invalid_grant)", errSource{&oauth2.RetrieveError{ErrorCode: "invalid_grant"}},
			&tokenstore.Credentials{PersonalAccessToken: "pat", UserGID: "1"}, "pat", false},
		{"other OAuth login saved meanwhile", refreshed(), &tokenstore.Credentials{RefreshToken: "rt9", AccessToken: "at9", Expiry: exp, UserGID: "1"}, "at9", true},
		{"logged out meanwhile", refreshed(), nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &memStore{caches: true, creds: tc.stored}
			var rebuilt bool
			p := &persistingSource{ctx: context.Background(), base: tc.base, store: st, creds: ours,
				newBase: func(c *tokenstore.Credentials) oauth2.TokenSource {
					rebuilt = true
					return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: c.AccessToken, RefreshToken: c.RefreshToken, Expiry: c.Expiry})
				}}
			for range 2 {
				tok, err := p.Token()
				if tc.want == "" {
					if !errors.Is(err, ErrNotLoggedIn) {
						t.Fatalf("Token() = %v, %v; want ErrNotLoggedIn", tok, err)
					}
					continue
				}
				if err != nil || tok.AccessToken != tc.want {
					t.Fatalf("Token() = %v, %v; want %s", tok, err, tc.want)
				}
			}
			if len(st.saved) != 0 {
				t.Fatalf("the login made elsewhere must not be overwritten: saved %+v", st.saved)
			}
			if rebuilt != tc.newBase {
				t.Fatalf("OAuth source rebuilt = %v, want %v", rebuilt, tc.newBase)
			}
			if tc.stored != nil && p.creds.UserGID != tc.stored.UserGID {
				t.Fatalf("creds = %+v, want the stored login", p.creds)
			}
		})
	}
}

// Another (or an unknown) user's login saved meanwhile is neither adopted nor overwritten:
// the session stops with ErrLoginReplaced, so its caller rebuilds it and the next sync's
// account check (sync.ErrAccountChanged) applies.
func TestPersistingSourceRefusesOtherUser(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	ours := tokenstore.Credentials{RefreshToken: "rt1", AccessToken: "at1", Expiry: exp, UserGID: "1"}
	for _, tc := range []struct {
		name   string
		base   oauth2.TokenSource
		stored tokenstore.Credentials
	}{
		{"other user's PAT (refresh)", &seqSource{{AccessToken: "at2", RefreshToken: "rt1", Expiry: exp}},
			tokenstore.Credentials{PersonalAccessToken: "pat", UserGID: "2"}},
		{"other user's OAuth (invalid_grant)", errSource{&oauth2.RetrieveError{ErrorCode: "invalid_grant"}},
			tokenstore.Credentials{RefreshToken: "rt9", UserGID: "2"}},
		{"unknown user", &seqSource{{AccessToken: "at2", RefreshToken: "rt1", Expiry: exp}},
			tokenstore.Credentials{PersonalAccessToken: "pat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := tc.stored
			st := &memStore{caches: true, creds: &stored}
			p := &persistingSource{ctx: context.Background(), base: tc.base, store: st, creds: ours,
				newBase: func(*tokenstore.Credentials) oauth2.TokenSource { t.Fatal("must not adopt another user"); return nil }}
			tok, err := p.Token()
			if tok != nil || !errors.Is(err, ErrLoginReplaced) || LoginHelp(err) == "" {
				t.Fatalf("Token() = %v, %v; want ErrLoginReplaced", tok, err)
			}
			if len(st.saved) != 0 || p.creds.UserGID != "1" {
				t.Fatalf("saved %+v, creds %+v", st.saved, p.creds)
			}
		})
	}
}
