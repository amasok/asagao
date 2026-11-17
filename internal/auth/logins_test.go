package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/amasok/asagao/internal/config"
)

func TestMoveLogin(t *testing.T) {
	keyring.MockInit() // never the developer's keychain
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()
	cfg := config.Default()
	cfg.Auth.TokenStore = config.TokenStoreFile
	from, err := OpenTokenStore(cfg.Auth)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := MoveLogin(ctx, from, cfg.Auth, config.TokenStoreFile); err == nil || !strings.Contains(err.Error(), "同じ") {
		t.Fatalf("to the same store: %v", err)
	}
	if _, err := MoveLogin(ctx, from, cfg.Auth, config.TokenStoreKeychain); !errors.Is(err, ErrNoSavedLogin) {
		t.Fatalf("nothing saved: %v", err)
	}

	saved := &Credentials{PersonalAccessToken: "pat", UserGID: "u1", UserName: "山田"}
	if err := from.Save(ctx, saved); err != nil {
		t.Fatal(err)
	}
	dst, err := MoveLogin(ctx, from, cfg.Auth, config.TokenStoreKeychain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dst.Load(ctx)
	if err != nil || got.PersonalAccessToken != "pat" || got.UserGID != "u1" || got.Method() != MethodPAT {
		t.Fatalf("moved login = %+v, %v", got, err)
	}
	if _, err := from.Load(ctx); !errors.Is(err, ErrNoSavedLogin) {
		t.Fatalf("the old store still has it: %v", err)
	}
	if _, err := MoveLogin(ctx, from, cfg.Auth, "nowhere"); err == nil {
		t.Fatal("an unknown store should fail")
	}
}
