package auth

import (
	"context"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asana-tui/internal/auth/tokenstore"
)

type memStore struct {
	caches bool
	saved  []tokenstore.Credentials
}

func (m *memStore) Name() string            { return "mem" }
func (m *memStore) CachesAccessToken() bool { return m.caches }
func (m *memStore) Load(context.Context) (*tokenstore.Credentials, error) {
	return nil, tokenstore.ErrNotFound
}
func (m *memStore) Save(_ context.Context, c *tokenstore.Credentials) error {
	m.saved = append(m.saved, *c)
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
			st := &memStore{caches: tc.caches}
			p := &persistingSource{
				ctx: context.Background(), base: tokens(), store: st,
				creds: tokenstore.Credentials{RefreshToken: "rt1", AccessToken: "at1", Expiry: exp},
			}
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
