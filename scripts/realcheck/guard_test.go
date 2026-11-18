package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testTree makes a fake ~/asagao-test with a config and a branch binary.
func testTree(t *testing.T, cfg string) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	root := filepath.Join(home, "asagao-test")
	for _, d := range []string{"config/asagao", "data", "cache", "bin"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "config/asagao/config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"asagao", "asagao-realcheck"} {
		if err := os.WriteFile(filepath.Join(root, "bin", b), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home, filepath.Join(root, "bin", "asagao-realcheck")
}

const fileStore = "[auth]\ntoken_store = \"file\"\n"

func TestCheckEnvAcceptsTestTree(t *testing.T) {
	home, bin := testTree(t, fileStore)
	if err := CheckEnv(NewEnv(home, bin, "/usr/bin")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckEnvRefuses(t *testing.T) {
	home, bin := testTree(t, fileStore)
	cases := map[string]func(e *Env){
		"usual config": func(e *Env) { e.Vars[3] = "XDG_CONFIG_HOME=" + filepath.Join(home, ".config") },
		"usual data":   func(e *Env) { e.Vars[4] = "XDG_DATA_HOME=" + filepath.Join(home, ".local/share") },
		"dotdot":       func(e *Env) { e.Vars[5] = "XDG_CACHE_HOME=" + filepath.Join(home, "asagao-test") + "/../.cache" },
		"relative":     func(e *Env) { e.Vars[3] = "XDG_CONFIG_HOME=asagao-test/config" },
		"sibling":      func(e *Env) { e.Vars[4] = "XDG_DATA_HOME=" + filepath.Join(home, "asagao-test2") },
		"token":        func(e *Env) { e.Vars = append(e.Vars, "ASANA_TOKEN=x") },
		"client":       func(e *Env) { e.Vars = append(e.Vars, "ASANA_CLIENT_SECRET=x") },
		"data field":   func(e *Env) { e.Data = filepath.Join(home, ".local/share") },
		"usual binary": func(e *Env) { e.Bin = filepath.Join(home, "asagao-test/bin/asagao") },
		"no binary":    func(e *Env) { e.Bin = filepath.Join(home, "nope") },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			e := NewEnv(home, bin, "/usr/bin")
			mod(&e)
			if err := CheckEnv(e); err == nil {
				t.Fatal("CheckEnv accepted it")
			}
		})
	}
}

func TestCheckEnvRefusesKeychainConfig(t *testing.T) {
	for _, cfg := range []string{"", "[auth]\ntoken_store = \"keychain\"\n", "# token_store = \"file\"\n"} {
		home, bin := testTree(t, cfg)
		if err := CheckEnv(NewEnv(home, bin, "/usr/bin")); err == nil {
			t.Errorf("config %q accepted", cfg)
		}
	}
}

func TestCheckMe(t *testing.T) {
	ok := `{"data":{"gid":"42","workspaces":[{"gid":"` + TestWorkspace + `"}]}}`
	if gid, err := CheckMe([]byte(ok)); err != nil || gid != "42" {
		t.Fatalf("got %q, %v", gid, err)
	}
	bad := []string{
		`{"data":{"gid":"42","workspaces":[]}}`,
		`{"data":{"gid":"42","workspaces":[{"gid":"1"}]}}`,
		`{"data":{"gid":"42","workspaces":[{"gid":"` + TestWorkspace + `"},{"gid":"1"}]}}`,
		`{"data":{"gid":"","workspaces":[{"gid":"` + TestWorkspace + `"}]}}`,
		`{"errors":[{"message":"Not Authorized"}]}`,
		`not json`,
	}
	for _, b := range bad {
		if _, err := CheckMe([]byte(b)); err == nil {
			t.Errorf("accepted %s", b)
		} else if strings.Contains(err.Error(), "token") {
			t.Errorf("error mentions a token: %v", err)
		}
	}
}
