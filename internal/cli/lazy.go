package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

var errOfflineFlag = errors.New("--offline のため Asana に問い合わせていません")

// Projects whose tasks are fetched when they are opened (#54): a command that lists a
// project's tasks fetches them first if they are not cached.

// openProject makes sure a project's tasks are cached before they are listed: it records
// the project as one to sync and, if its tasks are not cached, fetches them now (with the
// completed ones). If Asana cannot be reached, it says on stderr what is missing; the next
// sync fetches it. offline: Asana is not asked.
func (a *app) openProject(cmd *cobra.Command, st *Local, gid string, offline bool) {
	ctx := cmd.Context()
	state, err := st.App.WantProject(ctx, gid)
	if err != nil || state == readmodel.TasksAll {
		return
	}
	var s interface {
		OpenProject(context.Context, string, bool) (sync.OpenResult, error)
	}
	err = errOfflineFlag
	if !offline {
		s, err = a.onlineSyncer(ctx, st)
	}
	if err == nil {
		if res, oerr := s.OpenProject(ctx, gid, true); oerr != nil {
			err = oerr
		} else {
			state = res.State
			if res.Fetched {
				fmt.Fprintf(cmd.ErrOrStderr(), "（このプロジェクトのタスクを Asana から取得しました: タスク %d 件 / リクエスト %d 回）\n", res.Tasks, res.Requests)
			}
		}
	}
	if err == nil && state == readmodel.TasksAll {
		return
	}
	what := "このプロジェクトのタスク"
	if state == readmodel.TasksOpen {
		what = "このプロジェクトの完了済みのタスク"
	}
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "（%sはまだ取得していません: %v。次の同期で取得します）\n", what, err)
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "（%sはまだ取得していません。次の同期で取得します）\n", what)
}

// noteUnfetched says on stderr how many projects a search of the cache did not look in.
func (a *app) noteUnfetched(cmd *cobra.Command, st *Local) {
	n, err := st.App.Query().CountUnfetchedProjects(cmd.Context(), true)
	if err != nil || n == 0 {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "（タスクをまだ取得していないプロジェクトが %d 件あり、そこは探していません。--remote で Asana でも探せます）\n", n)
}
