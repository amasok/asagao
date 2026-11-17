package task

// Pruning: a full sync lists the tasks asagao should have (the tasks of the synced
// projects and the user's tasks, within the retention window, and their subtasks) and
// deletes the cached tasks it did not return: deleted in Asana, moved out of the synced
// projects, or completed before the window. These are the rules of what may be deleted.
//
// The store keeps them as the WHERE of one DELETE (store.PruneTasks), because deleting in
// one statement is what keeps an interrupted prune from leaving the cache half done;
// MayPrune and RememberRef say the same thing for one task, and the store's tests check
// that the two agree.

// PruneCandidate is what decides whether a full sync may delete a cached task.
type PruneCandidate struct {
	// Returned: the full sync returned the task.
	Returned bool
	// HasParent: the task is a subtask; ParentReturned: the full sync returned its parent.
	HasParent      bool
	ParentReturned bool
	// Queued: the outbox has an entry for the task (task_gid), in any state.
	Queued bool
	// Linked: a dependency links the task to another (either side).
	Linked bool
}

// MayPrune reports whether a full sync may delete the task from the cache. It must not
// when:
//
//   - the sync returned it;
//   - the outbox has an entry for it: a change not sent yet (or being sent, failed, in
//     conflict) is not dropped, and a task created offline (a local gid) has its create
//     queued. Sending the entry finds out whether the task is gone (sync.dropGone);
//   - it is a subtask whose parent the sync returned: a subtask goes with its parent, and
//     stays while the parent does, whether or not the sync fetched it (it fetches the
//     subtasks only of the tasks that say they have some). Only a returned parent counts:
//     a parent kept only for its queued changes does not keep its subtasks.
func MayPrune(c PruneCandidate) bool {
	if c.Returned || c.Queued {
		return false
	}
	return !c.HasParent || !c.ParentReturned
}

// RememberRef reports whether a prune records the task's name, completion and type
// (task_refs), so that a dependency on it can still be shown once it is deleted: it is
// linked by a dependency, and neither returned nor queued.
//
// It does not look at the parent: a linked subtask kept with its returned parent is
// recorded too. That record is not used while the task is cached (reads take the task's
// own values first), so it does no harm.
func RememberRef(c PruneCandidate) bool { return !c.Returned && !c.Queued && c.Linked }
