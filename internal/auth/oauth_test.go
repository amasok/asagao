package auth

import (
	"net/url"
	"strings"
	"testing"

	"github.com/amasok/asana-tui/internal/config"
)

func TestAuthCodeURLEncodesScopesWithPercent20(t *testing.T) {
	oc := OAuthConfig(config.AuthConfig{ClientID: "1", Scopes: []string{"tasks:read", "tasks:write"}}, "", LocalRedirectURL(8976))
	u := authCodeURL(oc, "st+ate", nil)
	if strings.Contains(u, "+") {
		t.Fatalf("URL contains '+': %s", u)
	}
	if !strings.Contains(u, "scope=tasks%3Aread%20tasks%3Awrite") {
		t.Fatalf("scope not %%20-separated: %s", u)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("state"); got != "st+ate" {
		t.Fatalf("state = %q", got)
	}
}
