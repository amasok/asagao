// Package wire builds the adapters (the store, the Asana client, the token store) and hands
// them to the application and the sync engine. It is the only package besides cmd/asagao
// that may import everything (architecture.md, 依存の決まり 6); the entry points get what
// it builds through cli.Wiring, which cmd/asagao fills with these functions.
package wire

import (
	"context"
	"net/http"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/auth/tokenstore"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/ui/format"
)

// APIClient is the Asana client for the login in the token store of cfg (ASANA_TOKEN
// first). baseURL overrides Asana's URL ("" for the real one; tests). With no login it
// returns auth.ErrNotLoggedIn, and a nil client.
func APIClient(ctx context.Context, cfg config.Config, baseURL string) (sync.AsanaAPI, error) {
	ts, err := tokenstore.New(cfg.Auth)
	if err != nil {
		return nil, err
	}
	src, err := auth.TokenSource(ctx, cfg.Auth, ts)
	if err != nil {
		return nil, err
	}
	return loginClient(ctx, src, baseURL), nil
}

// loginClient is the Asana client for a login's token source. It sends through the
// *auth.Source itself (src.Client), which marks a request without a token
// (*auth.TokenError): that is how sync knows the request was not sent.
func loginClient(ctx context.Context, src *auth.Source, baseURL string) *asana.Client {
	c := newClient(src.Client(ctx), baseURL)
	c.AuthHint = src.UnauthorizedHint()
	return c
}

func newClient(hc *http.Client, baseURL string) *asana.Client {
	c := asana.NewClient(hc)
	if baseURL != "" {
		c.BaseURL = baseURL
	}
	return c
}

// Syncer is the Syncer for api and st, with the [sync] settings of cfg and the rich text
// conversion. A nil api leaves Syncer.API unset (offline).
func Syncer[S sync.LocalStore](api sync.AsanaAPI, st S, cfg config.Config) *sync.Syncer {
	return &sync.Syncer{API: api, Store: st, Cfg: sync.Config(cfg.Sync), RichText: richtext.Converter{}}
}

// UseCaseStore is the store as every use case's port: *store.Store (or, in a test, a
// wrapper of one). Only the wiring sees it whole; each use case gets its own port.
type UseCaseStore interface {
	app.TaskStore
	app.CommentStore
	app.StatusStore
	app.BriefStore
	app.ProjectStore
	app.RelationStore
	app.SectionStore
	app.TaskOpsStore
	app.TagStore
	app.CopyStore
	app.FieldOptionStore
	app.ProjectFieldStore
	app.NotificationStore
	app.OutboxStore
	app.GoalStore
	app.TimeTrackingStore
	app.PushStore
	app.GitLinkStore
	app.CustomIDStore
	app.QuickAddStore
	app.QueryStore
	app.ProjectOpenStore
	app.ViewStateStore
	app.ResetStore
}

// UseCases are the use cases over st, writing rich text with richtext.Converter: what the
// entry points (tui, cli, mcpserver) write and read the local cache with.
func UseCases[S UseCaseStore](st S) *app.UseCases {
	return app.NewUseCases(app.Ports{
		Tasks: st, Comments: st, Status: st, Briefs: st, Projects: st, Relations: st,
		Sections: st, TaskOps: st, Tags: st, Copies: st, FieldOptions: st, ProjectFields: st,
		Notifications: st, Outbox: st, Goals: st, TimeTracking: st, Push: st, GitLink: st,
		CustomIDs: st, QuickAdd: st, Query: st, ProjectOpen: st, ViewState: st, Reset: st,
	}, richtext.Converter{})
}

// OpenStore opens (creating it if need be) the local store at path. Dropped changes are
// recorded with the display's description of them (format.DescribeChange).
func OpenStore(ctx context.Context, path string) (*store.Store, error) {
	st, err := store.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	st.SetDescribeChange(format.DescribeChange)
	return st, nil
}

// CheckPAT asks Asana who a Personal Access Token belongs to (GET /users/me), for a login.
// The user is waiting, so a busy or failing Asana is retried once, after retryDelay at most
// (not with a sync's backoff, #24), and onRetry is told.
func CheckPAT(ctx context.Context, token, baseURL string, retryDelay time.Duration, onRetry func(status int, wait time.Duration)) (*model.User, error) {
	c := newClient(auth.PATClient(ctx, token), baseURL)
	c.MaxRetries, c.MaxRetryDelay = 1, retryDelay
	c.OnRetry = onRetry
	return c.Me(ctx)
}
