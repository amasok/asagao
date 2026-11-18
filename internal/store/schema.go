package store

// migrations are applied in order; PRAGMA user_version records how many have run.
// Never edit an existing entry — append a new one.
var migrations = []string{
	`
CREATE TABLE workspaces (
	gid             TEXT PRIMARY KEY,
	name            TEXT NOT NULL,
	is_organization INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE users (
	gid   TEXT PRIMARY KEY,
	name  TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT ''
);

CREATE TABLE projects (
	gid           TEXT PRIMARY KEY,
	workspace_gid TEXT NOT NULL,
	name          TEXT NOT NULL,
	archived      INTEGER NOT NULL DEFAULT 0,
	color         TEXT NOT NULL DEFAULT '',
	notes         TEXT NOT NULL DEFAULT '',
	team_gid      TEXT NOT NULL DEFAULT '',
	team_name     TEXT NOT NULL DEFAULT '',
	is_member     INTEGER NOT NULL DEFAULT 0,
	permalink_url TEXT NOT NULL DEFAULT '',
	modified_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX projects_workspace ON projects(workspace_gid);

CREATE TABLE sections (
	gid         TEXT PRIMARY KEY,
	project_gid TEXT NOT NULL,
	name        TEXT NOT NULL,
	position    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX sections_project ON sections(project_gid, position);

CREATE TABLE tasks (
	gid              TEXT PRIMARY KEY,
	workspace_gid    TEXT NOT NULL DEFAULT '',
	name             TEXT NOT NULL,
	notes            TEXT NOT NULL DEFAULT '',
	resource_subtype TEXT NOT NULL DEFAULT 'default_task',
	completed        INTEGER NOT NULL DEFAULT 0,
	completed_at     TEXT,
	assignee_gid     TEXT NOT NULL DEFAULT '',
	due_on           TEXT NOT NULL DEFAULT '',
	due_at           TEXT,
	start_on         TEXT NOT NULL DEFAULT '',
	parent_gid       TEXT NOT NULL DEFAULT '',
	num_subtasks     INTEGER NOT NULL DEFAULT 0,
	permalink_url    TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL DEFAULT '',
	modified_at      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX tasks_assignee ON tasks(assignee_gid, completed, due_on);
CREATE INDEX tasks_parent ON tasks(parent_gid);

CREATE TABLE task_memberships (
	task_gid    TEXT NOT NULL REFERENCES tasks(gid) ON DELETE CASCADE,
	project_gid TEXT NOT NULL,
	section_gid TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (task_gid, project_gid)
);
CREATE INDEX task_memberships_project ON task_memberships(project_gid, section_gid);

CREATE TABLE tags (
	gid  TEXT PRIMARY KEY,
	name TEXT NOT NULL
);

CREATE TABLE task_tags (
	task_gid TEXT NOT NULL REFERENCES tasks(gid) ON DELETE CASCADE,
	tag_gid  TEXT NOT NULL,
	PRIMARY KEY (task_gid, tag_gid)
);

CREATE TABLE sync_state (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`,
	`
CREATE TABLE stories (
	gid              TEXT PRIMARY KEY,
	task_gid         TEXT NOT NULL REFERENCES tasks(gid) ON DELETE CASCADE,
	type             TEXT NOT NULL DEFAULT '',
	resource_subtype TEXT NOT NULL DEFAULT '',
	text             TEXT NOT NULL DEFAULT '',
	created_by_gid   TEXT NOT NULL DEFAULT '',
	created_by_name  TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX stories_task ON stories(task_gid, created_at);

-- When subtasks / stories of a task were last fetched from Asana.
CREATE TABLE task_details (
	task_gid   TEXT PRIMARY KEY REFERENCES tasks(gid) ON DELETE CASCADE,
	fetched_at TEXT NOT NULL
);

-- Full-text search. The trigram tokenizer matches substrings of 3+ characters, which works
-- for Japanese (no spaces between words). Shorter terms fall back to LIKE (see read.go).
CREATE VIRTUAL TABLE tasks_fts USING fts5(name, notes, content='tasks', content_rowid='rowid', tokenize='trigram');
CREATE TRIGGER tasks_fts_ai AFTER INSERT ON tasks BEGIN
	INSERT INTO tasks_fts(rowid, name, notes) VALUES (new.rowid, new.name, new.notes);
END;
CREATE TRIGGER tasks_fts_ad AFTER DELETE ON tasks BEGIN
	INSERT INTO tasks_fts(tasks_fts, rowid, name, notes) VALUES ('delete', old.rowid, old.name, old.notes);
END;
CREATE TRIGGER tasks_fts_au AFTER UPDATE OF name, notes ON tasks BEGIN
	INSERT INTO tasks_fts(tasks_fts, rowid, name, notes) VALUES ('delete', old.rowid, old.name, old.notes);
	INSERT INTO tasks_fts(rowid, name, notes) VALUES (new.rowid, new.name, new.notes);
END;
INSERT INTO tasks_fts(tasks_fts) VALUES ('rebuild');

CREATE VIRTUAL TABLE stories_fts USING fts5(text, content='stories', content_rowid='rowid', tokenize='trigram');
CREATE TRIGGER stories_fts_ai AFTER INSERT ON stories BEGIN
	INSERT INTO stories_fts(rowid, text) VALUES (new.rowid, new.text);
END;
CREATE TRIGGER stories_fts_ad AFTER DELETE ON stories BEGIN
	INSERT INTO stories_fts(stories_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
END;
CREATE TRIGGER stories_fts_au AFTER UPDATE OF text ON stories BEGIN
	INSERT INTO stories_fts(stories_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
	INSERT INTO stories_fts(rowid, text) VALUES (new.rowid, new.text);
END;
`,
	`
-- Local changes waiting to be sent to Asana (offline edits). See outbox.go.
CREATE TABLE outbox (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	kind             TEXT NOT NULL,              -- update_task | create_task | add_comment
	task_gid         TEXT NOT NULL,              -- real gid, or "local:..." for a task created offline
	payload          TEXT NOT NULL,              -- JSON
	base             TEXT NOT NULL DEFAULT '{}', -- JSON: field values before the local edit (conflict detection)
	base_modified_at TEXT NOT NULL DEFAULT '',
	state            TEXT NOT NULL DEFAULT 'pending', -- pending | conflict | failed
	conflict         TEXT NOT NULL DEFAULT '{}', -- JSON: server values of conflicting fields
	last_error       TEXT NOT NULL DEFAULT '',
	attempts         INTEGER NOT NULL DEFAULT 0,
	created_at       TEXT NOT NULL
);
CREATE INDEX outbox_task ON outbox(task_gid);
`,
	`
-- Attachments of tasks (metadata). Files are downloaded on demand into the cache dir;
-- local_path remembers where, so images can be viewed offline afterwards.
CREATE TABLE attachments (
	gid              TEXT PRIMARY KEY,
	parent_gid       TEXT NOT NULL REFERENCES tasks(gid) ON DELETE CASCADE,
	name             TEXT NOT NULL DEFAULT '',
	resource_subtype TEXT NOT NULL DEFAULT '',
	host             TEXT NOT NULL DEFAULT '',
	view_url         TEXT NOT NULL DEFAULT '',
	permanent_url    TEXT NOT NULL DEFAULT '',
	size             INTEGER NOT NULL DEFAULT 0,
	created_at       TEXT NOT NULL DEFAULT '',
	local_path       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX attachments_parent ON attachments(parent_gid, created_at);
`,
	`
-- Details fetched before attachments were supported did not include them; forget the
-- fetch times so every task is fetched again (with attachments) the next time it is opened.
DELETE FROM task_details;
`,
	`
-- Rich text (Asana's HTML subset) for Markdown rendering. Re-fetch details to fill them.
ALTER TABLE tasks ADD COLUMN html_notes TEXT NOT NULL DEFAULT '';
ALTER TABLE stories ADD COLUMN html_text TEXT NOT NULL DEFAULT '';
DELETE FROM task_details;
`,
	`
-- Likes and followers of tasks.
ALTER TABLE tasks ADD COLUMN liked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN num_likes INTEGER NOT NULL DEFAULT 0;
CREATE TABLE task_followers (
	task_gid TEXT NOT NULL REFERENCES tasks(gid) ON DELETE CASCADE,
	user_gid TEXT NOT NULL,
	PRIMARY KEY (task_gid, user_gid)
);
`,
	`
-- Project members and status updates.
CREATE TABLE project_members (
	project_gid TEXT NOT NULL,
	user_gid    TEXT NOT NULL,
	PRIMARY KEY (project_gid, user_gid)
);
CREATE TABLE status_updates (
	gid         TEXT PRIMARY KEY,
	project_gid TEXT NOT NULL,
	status_type TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	text        TEXT NOT NULL DEFAULT '',
	html_text   TEXT NOT NULL DEFAULT '',
	author_name TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX status_updates_project ON status_updates(project_gid, created_at);
`,
	`
-- Outbox entries whose result is unknown (e.g. a create answered with 5xx): verify = 1 means
-- Push first looks in Asana for what the entry created since sent_at, before sending it again.
ALTER TABLE outbox ADD COLUMN sent_at TEXT NOT NULL DEFAULT '';
ALTER TABLE outbox ADD COLUMN verify INTEGER NOT NULL DEFAULT 0;
`,
	`
-- When the entry's latest request went out (sent_at is the first): Asana may still be
-- processing that one, so a missing result is trusted only some time after it.
ALTER TABLE outbox ADD COLUMN last_sent_at TEXT NOT NULL DEFAULT '';
`,
	`
-- Local changes dropped automatically because what they change was deleted in Asana
-- (see discard.go). notified = 1 once a front end has shown them.
CREATE TABLE discarded_changes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT NOT NULL,
    task_gid     TEXT NOT NULL DEFAULT '',
    task_name    TEXT NOT NULL DEFAULT '',
    summary      TEXT NOT NULL,
    reason       TEXT NOT NULL DEFAULT '',
    discarded_at TEXT NOT NULL,
    notified     INTEGER NOT NULL DEFAULT 0
);
`,
	`
-- The dropped change itself (outbox payload JSON), so what the user wrote can be recovered.
ALTER TABLE discarded_changes ADD COLUMN payload TEXT NOT NULL DEFAULT '{}';
`,
	`
-- The user's Asana inbox (notifications), newest first per workspace (see notifications.go).
CREATE TABLE notifications (
	gid           TEXT PRIMARY KEY,
	workspace_gid TEXT NOT NULL,
	is_read       INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL DEFAULT '',
	actor_gid     TEXT NOT NULL DEFAULT '',
	actor_name    TEXT NOT NULL DEFAULT '',
	subtype       TEXT NOT NULL DEFAULT '',
	text          TEXT NOT NULL DEFAULT '',
	target_gid    TEXT NOT NULL DEFAULT '',
	target_type   TEXT NOT NULL DEFAULT '',
	target_name   TEXT NOT NULL DEFAULT '',
	position      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notifications_created ON notifications(created_at);
`,
	`
-- Custom fields: definitions (from project settings and tasks), the fields of each project,
-- and the values of tasks. value is JSON in the form Asana accepts (enum option gid,
-- [option gids], number, text, "YYYY-MM-DD", [user gids]; null when empty); display is
-- the value as text. Forget the last full sync so the next sync fetches every task's values.
CREATE TABLE custom_fields (
	gid          TEXT PRIMARY KEY,
	name         TEXT NOT NULL DEFAULT '',
	type         TEXT NOT NULL DEFAULT '',
	precision    INTEGER NOT NULL DEFAULT 0,
	read_only    INTEGER NOT NULL DEFAULT 0,
	enum_options TEXT NOT NULL DEFAULT '' -- JSON [{gid, name, color, enabled}]; '' = not known
);
CREATE TABLE project_custom_fields (
	project_gid  TEXT NOT NULL,
	field_gid    TEXT NOT NULL,
	position     INTEGER NOT NULL DEFAULT 0,
	is_important INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (project_gid, field_gid)
);
CREATE TABLE task_custom_fields (
	task_gid  TEXT NOT NULL REFERENCES tasks(gid) ON DELETE CASCADE,
	field_gid TEXT NOT NULL,
	position  INTEGER NOT NULL DEFAULT 0,
	value     TEXT NOT NULL DEFAULT 'null',
	display   TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (task_gid, field_gid)
);
CREATE INDEX task_custom_fields_field ON task_custom_fields(field_gid);
DELETE FROM sync_state WHERE key = 'last_full_sync';
`,
	`
-- Dependencies between tasks (task_gid waits for dependency_gid), the approval status of
-- approval tasks, and the names of referenced tasks that are not cached (task_refs).
-- Forget the last full sync so the next sync fetches them for every task.
CREATE TABLE task_dependencies (
	task_gid       TEXT NOT NULL,
	dependency_gid TEXT NOT NULL,
	PRIMARY KEY (task_gid, dependency_gid)
);
CREATE INDEX task_dependencies_dependency ON task_dependencies(dependency_gid);
CREATE TABLE task_refs (
	gid              TEXT PRIMARY KEY,
	name             TEXT NOT NULL DEFAULT '',
	completed        INTEGER NOT NULL DEFAULT 0,
	resource_subtype TEXT NOT NULL DEFAULT ''
);
ALTER TABLE tasks ADD COLUMN approval_status TEXT NOT NULL DEFAULT '';
DELETE FROM sync_state WHERE key = 'last_full_sync';
`,
	`
-- Projects (#38, #39): a project's rich notes, owner and dates; the teams I am in and my
-- favorite projects (to group the project list); and a project's overview (task counts
-- and brief), fetched when it is opened. Counts are NULL until fetched; brief_state is ''
-- (not fetched), 'none' (no brief), 'unavailable' (not on this plan) or 'available'.
ALTER TABLE projects ADD COLUMN html_notes TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN owner_gid TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN start_on TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN due_on TEXT NOT NULL DEFAULT '';
CREATE TABLE teams (
	gid           TEXT PRIMARY KEY,
	workspace_gid TEXT NOT NULL,
	name          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX teams_workspace ON teams(workspace_gid);
CREATE TABLE project_favorites (
	project_gid   TEXT PRIMARY KEY,
	workspace_gid TEXT NOT NULL,
	position      INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE project_overviews (
	project_gid              TEXT PRIMARY KEY,
	num_tasks                INTEGER,
	num_completed_tasks      INTEGER,
	num_milestones           INTEGER,
	num_completed_milestones INTEGER,
	brief_state              TEXT NOT NULL DEFAULT '',
	brief_title              TEXT NOT NULL DEFAULT '',
	brief_text               TEXT NOT NULL DEFAULT '',
	brief_html               TEXT NOT NULL DEFAULT '',
	fetched_at               TEXT NOT NULL DEFAULT ''
);
`,
}
