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
}
