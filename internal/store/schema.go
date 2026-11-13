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
}
