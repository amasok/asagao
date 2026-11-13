package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/amasok/asana-tui/internal/auth"
	"github.com/amasok/asana-tui/internal/auth/tokenstore"
	"github.com/amasok/asana-tui/internal/config"
)

// EnvClientSecret lets the client secret be passed non-interactively (e.g. `op run`).
const EnvClientSecret = "ASANA_CLIENT_SECRET"

func (a *app) authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Asana へのログイン・ログアウト"}
	cmd.AddCommand(a.authLoginCmd(), a.authStatusCmd(), a.authLogoutCmd(), a.authMigrateCmd())
	return cmd
}

func (a *app) authLoginCmd() *cobra.Command {
	var manual, noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "OAuth 2.0 でログインし、トークンを保存する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if a.cfg.Auth.ClientID == "" {
				return fmt.Errorf("auth.client_id が未設定です。%s に設定するか ASANA_CLIENT_ID を指定してください", config.FilePath())
			}
			ts, err := a.tokenStore()
			if err != nil {
				return err
			}
			secret, err := a.clientSecret(cmd, ts)
			if err != nil {
				return err
			}
			oc := auth.OAuthConfig(a.cfg.Auth, secret, auth.LocalRedirectURL(a.cfg.Auth.RedirectPort))
			tok, err := auth.Login(ctx, oc, auth.LoginOptions{
				Manual:    manual,
				NoBrowser: noBrowser,
				Prompt:    func(label string) (string, error) { return promptLine(cmd, label) },
				Out:       func(f string, args ...any) { fmt.Fprintf(cmd.ErrOrStderr(), f, args...) },
			})
			if err != nil {
				return err
			}
			creds := auth.CredentialsFromToken(tok, secret)
			if err := ts.Save(ctx, creds); err != nil {
				return fmt.Errorf("トークンの保存 (%s): %w", ts.Name(), err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) としてログインしました。トークンの保存先: %s\n", creds.UserName, creds.UserEmail, ts.Name())
			return nil
		},
	}
	cmd.Flags().BoolVar(&manual, "manual", false, "コードを手で貼り付ける方式（SSH 先などブラウザがない環境向け）")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "ブラウザを自動で開かず URL を表示する")
	return cmd
}

// clientSecret takes the secret from the environment, an existing login, or a hidden prompt.
func (a *app) clientSecret(cmd *cobra.Command, ts tokenstore.Store) (string, error) {
	if v := os.Getenv(EnvClientSecret); v != "" {
		return v, nil
	}
	if c, err := ts.Load(cmd.Context()); err == nil && c.ClientSecret != "" {
		return c.ClientSecret, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("client secret がありません。%s を設定してください", EnvClientSecret)
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Client secret（入力は表示されません）: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (a *app) authStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "ログイン状態を表示する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			type status struct {
				LoggedIn   bool   `json:"logged_in"`
				Method     string `json:"method"`
				TokenStore string `json:"token_store,omitempty"`
				UserName   string `json:"user_name,omitempty"`
				UserEmail  string `json:"user_email,omitempty"`
			}
			var st status
			if os.Getenv(auth.EnvToken) != "" {
				st = status{LoggedIn: true, Method: "personal_access_token"}
			} else {
				ts, err := a.tokenStore()
				if err != nil {
					return err
				}
				st.Method, st.TokenStore = "oauth", ts.Name()
				c, err := ts.Load(cmd.Context())
				switch {
				case errors.Is(err, tokenstore.ErrNotFound):
				case err != nil:
					return err
				default:
					st.LoggedIn, st.UserName, st.UserEmail = true, c.UserName, c.UserEmail
				}
			}
			if a.json {
				return a.printJSON(st)
			}
			out := cmd.OutOrStdout()
			switch {
			case !st.LoggedIn:
				fmt.Fprintf(out, "ログインしていません（保存先: %s）\n", st.TokenStore)
			case st.Method == "personal_access_token":
				fmt.Fprintf(out, "%s の Personal Access Token を使用中\n", auth.EnvToken)
			default:
				fmt.Fprintf(out, "%s (%s) としてログイン中（保存先: %s）\n", st.UserName, st.UserEmail, st.TokenStore)
			}
			return nil
		},
	}
}

func (a *app) authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "保存したトークンを削除する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ts, err := a.tokenStore()
			if err != nil {
				return err
			}
			if err := ts.Delete(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ログアウトしました（%s から削除）\n", ts.Name())
			return nil
		},
	}
}

func (a *app) authMigrateCmd() *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "migrate --to <keychain|1password|file>",
		Short: "トークンの保存先を移す（移した後に config の auth.token_store も変更してください）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			from, err := a.tokenStore()
			if err != nil {
				return err
			}
			dstCfg := a.cfg.Auth
			dstCfg.TokenStore = to
			dst, err := tokenstore.New(dstCfg)
			if err != nil {
				return err
			}
			if dst.Name() == from.Name() {
				return fmt.Errorf("移行元と移行先が同じです (%s)", to)
			}
			c, err := from.Load(ctx)
			if err != nil {
				return fmt.Errorf("%s からの読み込み: %w", from.Name(), err)
			}
			if !dst.CachesAccessToken() {
				c.AccessToken = ""
			}
			if err := dst.Save(ctx, c); err != nil {
				return fmt.Errorf("%s への保存: %w", dst.Name(), err)
			}
			if err := from.Delete(ctx); err != nil {
				return fmt.Errorf("%s からの削除: %w", from.Name(), err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s から %s に移しました。%s の auth.token_store を %q にしてください\n",
				from.Name(), dst.Name(), config.FilePath(), to)
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "移行先 (keychain, 1password, file)")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func promptLine(cmd *cobra.Command, label string) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), label)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return line, nil
}
