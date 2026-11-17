package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/auth/tokenstore"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/xdg"
)

// EnvClientSecret lets the client secret be passed non-interactively (e.g. `op run`).
const EnvClientSecret = "ASANA_CLIENT_SECRET"

func (a *app) authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Asana へのログイン・ログアウト"}
	cmd.AddCommand(a.authLoginCmd(), a.authStatusCmd(), a.authLogoutCmd(), a.authMigrateCmd())
	return cmd
}

func (a *app) authLoginCmd() *cobra.Command {
	var manual, noBrowser, pat bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Asana にログインし、トークンを保存する（OAuth、または --pat で Personal Access Token）",
		Long: `Asana にログインし、トークンを保存します。

Personal Access Token (PAT) を使う方法がいちばん手軽です（OAuth アプリの登録は要りません）:
  1. ` + auth.PATConsoleURL + ` の「Personal access tokens」でトークンを作る
  2. asagao auth login --pat を実行し、トークンを貼り付ける（入力は表示されません）
     標準入力からも渡せます: pbpaste | asagao auth login --pat
     （トークンをコマンドの引数に書くとシェルの履歴に残るので、引数では受け付けません）

auth.client_id が設定されていなければ、--pat を付けなくても PAT でのログインになります。
OAuth で使うなら、Asana に OAuth アプリを登録して config.toml の auth.client_id を設定し、
--pat を付けずに実行してください（ブラウザが開きます）。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ts, err := a.tokenStore()
			if err != nil {
				return err
			}
			if a.cfg.Auth.ClientID == "" && !pat && (manual || noBrowser) {
				return fmt.Errorf("auth.client_id が未設定です。OAuth を使うなら %s に設定するか ASANA_CLIENT_ID を指定してください"+
					"（OAuth アプリを登録しないなら asagao auth login --pat）", xdg.ConfigFile())
			}
			if pat || a.cfg.Auth.ClientID == "" {
				return a.loginPAT(cmd, ts)
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
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) としてログインしました（OAuth、保存先: %s）\n", creds.UserName, creds.UserEmail, ts.Name())
			a.afterLogin(cmd, creds.UserGID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&pat, "pat", false, "Personal Access Token でログインする（貼り付けるか標準入力から渡す）")
	cmd.Flags().BoolVar(&manual, "manual", false, "OAuth: コードを手で貼り付ける方式（SSH 先などブラウザがない環境向け）")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "OAuth: ブラウザを自動で開かず URL を表示する")
	cmd.MarkFlagsMutuallyExclusive("pat", "manual")
	cmd.MarkFlagsMutuallyExclusive("pat", "no-browser")
	return cmd
}

// loginRetryDelay is the longest wait before retrying the token check at login, and
// loginTimeout the longest the whole check may take (an Asana that accepts the connection
// but never answers would keep the user waiting otherwise).
var (
	loginRetryDelay = 2 * time.Second
	loginTimeout    = 20 * time.Second
)

// busyMessage says what an HTTP status the token check got means: 429 is a rate limit,
// 5xx a failure on Asana's side.
func busyMessage(status int) string {
	if status == http.StatusTooManyRequests {
		return fmt.Sprintf("Asana が混み合っています (HTTP %d)", status)
	}
	return fmt.Sprintf("Asana が一時的に応答しません (HTTP %d)", status)
}

// loginPAT reads a Personal Access Token, checks it with /users/me and saves it.
func (a *app) loginPAT(cmd *cobra.Command, ts tokenstore.Store) error {
	ctx := cmd.Context()
	token, err := readPAT(cmd)
	if err != nil {
		return err
	}
	checkCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	// The user is waiting: retry a busy or failing Asana once, briefly and saying so, not
	// with the backoff of a sync (about 30 s in all, #24).
	me, err := wiring.CheckPAT(checkCtx, token, a.baseURL, loginRetryDelay, func(status int, wait time.Duration) {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s。%s後にもう一度確かめます…\n", busyMessage(status), wait.Round(time.Second))
	})
	if model.IsUnauthorized(err) {
		return fmt.Errorf("Asana がこのトークンを受け付けませんでした。貼り付け間違いがないか確かめるか、%s で作り直してください", auth.PATConsoleURL)
	}
	var ae *model.Error
	if errors.As(err, &ae) && (ae.Status >= 500 || ae.Status == http.StatusTooManyRequests) {
		return fmt.Errorf("%s。トークンは保存していません。少し待ってから、もう一度 asagao auth login --pat を実行してください", busyMessage(ae.Status))
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("Asana から %s 以内に返事がありませんでした。トークンは保存していません。少し待ってから、もう一度 asagao auth login --pat を実行してください", loginTimeout)
	}
	if err != nil {
		return fmt.Errorf("トークンの確認 (/users/me): %w", err)
	}
	creds := &tokenstore.Credentials{PersonalAccessToken: token, UserGID: me.GID, UserName: me.Name, UserEmail: me.Email}
	// Keep the client secret of an earlier OAuth login, so switching back needs no re-entry.
	if old, err := ts.Load(ctx); err == nil {
		creds.ClientSecret = old.ClientSecret
	}
	if err := ts.Save(ctx, creds); err != nil {
		return fmt.Errorf("トークンの保存 (%s): %w", ts.Name(), err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) としてログインしました（Personal Access Token、保存先: %s）\n", me.Name, me.Email, ts.Name())
	a.afterLogin(cmd, me.GID)
	return nil
}

// readPAT takes the token from a hidden prompt on a terminal, or else from stdin, never
// from the command line (it would stay in the shell history).
func readPAT(cmd *cobra.Command) (string, error) {
	var b []byte
	var err error
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s で作成した Personal Access Token を貼り付けてください（入力は表示されません）: ", auth.PATConsoleURL)
		b, err = term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), "標準入力から Personal Access Token を読んでいます…")
		b, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64<<10))
	}
	if err != nil {
		return "", fmt.Errorf("Personal Access Token の読み込み: %w", err)
	}
	token := strings.TrimSpace(string(b))
	switch {
	case token == "":
		return "", fmt.Errorf("Personal Access Token が空です。%s で作成し、貼り付けるか標準入力から渡してください（例: pbpaste | asagao auth login --pat）", auth.PATConsoleURL)
	case strings.ContainsAny(token, " \t\r\n"):
		return "", errors.New("Personal Access Token に空白や改行が含まれています。トークンだけを 1 行で渡してください")
	}
	return token, nil
}

// afterLogin tells the user what else affects the login just made: ASANA_TOKEN still wins
// over it, and a different account than the local data's stops syncing
// (sync.ErrAccountChanged) until they log back in or reset.
func (a *app) afterLogin(cmd *cobra.Command, gid string) {
	if os.Getenv(auth.EnvToken) != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "注意: 環境変数 %s が設定されている間は、そちらのトークンが優先されます\n", auth.EnvToken)
	}
	if gid == "" {
		return
	}
	if _, err := os.Stat(a.storePath()); err != nil {
		return // no local data yet
	}
	ctx := cmd.Context()
	st, err := a.openStore(ctx)
	if err != nil {
		return
	}
	defer st.Close()
	_ = st.SetInboxWriteForbidden(ctx, false) // the new login may allow marking notifications
	if want, err := st.SyncState(ctx, readmodel.KeyMeGID); err != nil || want == "" || want == gid {
		return
	}
	n, _ := query(st).UnsentCount(ctx)
	fmt.Fprintln(cmd.ErrOrStderr(), "注意: "+readmodel.AccountChangedHelp(n, "asagao reset"))
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
		Short: "ログイン状態と方式（OAuth / Personal Access Token）を表示する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			type status struct {
				LoggedIn bool `json:"logged_in"`
				// Method is "oauth" or "personal_access_token" ("" when not logged in).
				Method     string `json:"method"`
				Env        string `json:"env,omitempty"` // the variable the token comes from
				TokenStore string `json:"token_store,omitempty"`
				UserName   string `json:"user_name,omitempty"`
				UserEmail  string `json:"user_email,omitempty"`
			}
			var st status
			if os.Getenv(auth.EnvToken) != "" {
				st = status{LoggedIn: true, Method: string(tokenstore.MethodPAT), Env: auth.EnvToken}
			} else {
				ts, err := a.tokenStore()
				if err != nil {
					return err
				}
				st.TokenStore = ts.Name()
				c, err := ts.Load(cmd.Context())
				switch {
				case errors.Is(err, tokenstore.ErrNotFound):
				case err != nil:
					return err
				default:
					st.LoggedIn, st.Method, st.UserName, st.UserEmail = true, string(c.Method()), c.UserName, c.UserEmail
				}
			}
			if a.json {
				return a.printJSON(st)
			}
			out := cmd.OutOrStdout()
			switch {
			case !st.LoggedIn:
				fmt.Fprintf(out, "ログインしていません（保存先: %s）\n%s\n", st.TokenStore, auth.ErrNotLoggedIn)
			case st.Env != "":
				fmt.Fprintf(out, "%s の Personal Access Token を使用中\n", st.Env)
			default:
				fmt.Fprintf(out, "%s (%s) としてログイン中（方式: %s、保存先: %s）\n",
					st.UserName, st.UserEmail, methodLabel(tokenstore.Method(st.Method)), st.TokenStore)
			}
			return nil
		},
	}
}

func methodLabel(m tokenstore.Method) string {
	if m == tokenstore.MethodPAT {
		return "Personal Access Token"
	}
	return "OAuth"
}

func (a *app) authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "保存したトークン（OAuth / Personal Access Token）を削除する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ts, err := a.tokenStore()
			if err != nil {
				return err
			}
			if err := ts.Delete(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ログアウトしました（%s から削除）\n", ts.Name())
			if os.Getenv(auth.EnvToken) != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "注意: 環境変数 %s が設定されているので、そのトークンは引き続き使われます\n", auth.EnvToken)
			}
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
				from.Name(), dst.Name(), xdg.ConfigFile(), to)
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
