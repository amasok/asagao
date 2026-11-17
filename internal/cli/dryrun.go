package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app" // the cli package has its own type app
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/xdg"
)

// A dry run (--dry-run) carries out a write command on a throwaway copy of the store
// (VACUUM INTO a private temporary folder): the same checks run and the same changes are
// queued, but on the copy, which is removed afterwards. Nothing is sent to Asana and the
// real store and its outbox are not touched. What the copy's outbox gained is what the
// command would have queued, and it is shown instead of the usual result.

// errDryRunSend guards the paths that reach Asana: a dry run must never get there.
var errDryRunSend = errors.New("--dry-run では Asana に送りません")

// addDryRunFlag gives a write command the --dry-run flag.
func (a *app) addDryRunFlag(cmd *cobra.Command) *cobra.Command {
	cmd.Flags().BoolVar(&a.dryRun, "dry-run", false, "何も変えずに、行われる変更だけを表示する（Asana に送らず、outbox にも積まない）")
	return cmd
}

// dryRunStore is the copy of the store a dry run writes to. Close removes it.
type dryRunStore struct {
	Store
	dir string
	// before is the copy's outbox before the command ran.
	before []outbox.Op
}

func (d *dryRunStore) Close() error {
	return errors.Join(d.Store.Close(), os.RemoveAll(d.dir))
}

// openDryRun copies st (which it closes) to a private temporary folder and opens the copy.
func openDryRun(ctx context.Context, st Store) (_ Store, err error) {
	defer func() { err = errors.Join(err, st.Close()) }()
	removeStaleDryRuns(time.Now())
	dir, err := os.MkdirTemp("", dryRunPrefix) // 0700
	if err != nil {
		return nil, err
	}
	fail := func(err error) (Store, error) { return nil, errors.Join(err, os.RemoveAll(dir)) }
	path := filepath.Join(dir, "store.db")
	if err := st.CopyTo(ctx, path); err != nil {
		return fail(err)
	}
	cp, err := wiring.OpenStore(ctx, path)
	if err != nil {
		return fail(err)
	}
	before, err := cp.ListOps(ctx)
	if err != nil {
		return fail(errors.Join(err, cp.Close()))
	}
	return &dryRunStore{Store: cp, dir: dir, before: before}, nil
}

// dryRunPrefix starts the name of a dry run's temporary folder.
const dryRunPrefix = "asagao-dry-run-"

// staleDryRun is how old a dry run's folder must be to be taken as left behind (by a dry
// run stopped with Ctrl-C or killed, which could not remove it). A dry run takes seconds.
const staleDryRun = time.Hour

// removeStaleDryRuns removes the folders that earlier dry runs left behind. Folders of
// other users (in a shared /tmp) cannot be removed and are left alone.
func removeStaleDryRuns(now time.Time) {
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), dryRunPrefix+"*"))
	for _, d := range dirs {
		fi, err := os.Lstat(d)
		if err != nil || !fi.IsDir() || now.Sub(fi.ModTime()) < staleDryRun {
			continue
		}
		_ = os.RemoveAll(d)
	}
}

// plannedChange is one change a dry run would make.
type plannedChange struct {
	// Kind is the outbox kind (update_task, create_task, ...), or for a change made on
	// Asana directly create_project, archive_project, unarchive_project, add_member,
	// remove_member; for an outbox entry resolved: keep_local, keep_remote, retry, discard.
	Kind        string         `json:"kind"`
	TaskGID     string         `json:"task_gid,omitempty"`
	TaskName    string         `json:"task_name,omitempty"`
	Description string         `json:"description"`
	Payload     map[string]any `json:"payload,omitempty"`
}

// cancelledChange is a queued change (outbox entry) a dry run would drop.
type cancelledChange struct {
	ID          int64  `json:"id"`
	Description string `json:"description"`
}

// dryRunResult is the --json output of a dry run.
type dryRunResult struct {
	DryRun  bool            `json:"dry_run"`
	TaskGID string          `json:"task_gid,omitempty"`
	Planned []plannedChange `json:"planned"`
	// Cancelled are queued changes the command would drop (reopening a task whose
	// completion is not sent yet, say).
	Cancelled []cancelledChange `json:"cancelled,omitempty"`
	Quick     *quickAddJSON     `json:"quick,omitempty"`
}

// queuedChanges compares the copy's outbox with what it was before the command: the
// entries added or changed, and the ids of those that went away.
func (d *dryRunStore) queuedChanges(ctx context.Context) ([]plannedChange, []cancelledChange, error) {
	after, err := d.ListOps(ctx)
	if err != nil {
		return nil, nil, err
	}
	old := map[int64]outbox.Op{}
	for _, op := range d.before {
		old[op.ID] = op
	}
	var planned []plannedChange
	for _, op := range after {
		if prev, ok := old[op.ID]; ok {
			delete(old, op.ID)
			if reflect.DeepEqual(prev, op) {
				continue
			}
		}
		planned = append(planned, plannedChange{
			Kind: op.Kind, TaskGID: dryRunGID(op.TaskGID), TaskName: op.TaskName,
			Description: readmodel.DescribeOp(op), Payload: op.Payload,
		})
	}
	var cancelled []cancelledChange
	for _, op := range d.before {
		if _, gone := old[op.ID]; gone {
			cancelled = append(cancelled, cancelledChange{ID: op.ID, Description: readmodel.DescribeOp(op)})
		}
	}
	return planned, cancelled, nil
}

// dryRunGID hides a local gid (local:...): it names something only the thrown-away copy has.
func dryRunGID(gid string) string {
	if localgid.IsLocal(gid) {
		return ""
	}
	return gid
}

// reportDryRun shows what a write command would have queued (instead of reportWrite).
func (a *app) reportDryRun(cmd *cobra.Command, d *dryRunStore, r writeResult) error {
	planned, cancelled, err := d.queuedChanges(cmd.Context())
	if err != nil {
		return err
	}
	return a.printDryRun(cmd, dryRunResult{TaskGID: dryRunGID(r.TaskGID), Planned: planned, Cancelled: cancelled, Quick: r.Quick})
}

// printDryRun prints a dry run's result.
func (a *app) printDryRun(cmd *cobra.Command, r dryRunResult) error {
	r.DryRun = true
	if r.Planned == nil {
		r.Planned = []plannedChange{}
	}
	if a.json {
		return a.printJSON(r)
	}
	out := cmd.OutOrStdout()
	if len(r.Planned) == 0 && len(r.Cancelled) == 0 {
		fmt.Fprintln(out, "--dry-run: 変更はありません")
		return nil
	}
	fmt.Fprintln(out, "--dry-run: 次の変更をするところでした（何も変えていません）:")
	for _, p := range r.Planned {
		fmt.Fprintln(out, "  - "+p.Description)
	}
	for _, c := range r.Cancelled {
		fmt.Fprintf(out, "  - 未送信の変更 #%d（%s）の取り消し\n", c.ID, c.Description)
	}
	return nil
}

// dryRunPlan prints one change that is made on Asana directly (not through the outbox).
func (a *app) dryRunPlan(cmd *cobra.Command, planned ...plannedChange) error {
	return a.printDryRun(cmd, dryRunResult{Planned: planned})
}

// attachmentCacheDir is where a file to attach is copied before it is sent: in a dry run,
// the copy's folder, so that the real cache is not touched either.
func attachmentCacheDir(st Store) string {
	if d, ok := st.(*dryRunStore); ok {
		return filepath.Join(d.dir, "attachments")
	}
	return filepath.Join(xdg.CacheDir(), "attachments")
}

// resolveLabels say what resolving an outbox entry would do.
var resolveLabels = map[usecase.ResolveAction]string{
	usecase.KeepLocal:  "こちらの値で Asana を上書き",
	usecase.KeepRemote: "Asana の値を採用して取り消し",
	usecase.Retry:      "もう一度送信",
	usecase.Discard:    "取り消し",
}

// dryRunResolve resolves outbox entry id on the copy (so that the same checks run) and
// says what it would do.
func (a *app) dryRunResolve(cmd *cobra.Command, d *dryRunStore, id int64, act usecase.ResolveAction) error {
	r, err := (&usecase.Outbox{Store: d}).Resolve(cmd.Context(), id, act)
	if err != nil {
		return err
	}
	return a.dryRunPlan(cmd, plannedChange{
		Kind: string(act), TaskGID: dryRunGID(r.Op.TaskGID), TaskName: r.Op.TaskName,
		Description: fmt.Sprintf("#%d %s の%s", id, readmodel.DescribeOp(r.Op), resolveLabels[act]),
	})
}

// projectName is the cached name of project gid (the gid itself when it is not cached).
func projectName(ctx context.Context, st Store, gid string) string {
	ps, err := query(st).ListProjects(ctx, false)
	if err == nil {
		for _, p := range ps {
			if p.GID == gid {
				return p.Name
			}
		}
	}
	return gid
}
