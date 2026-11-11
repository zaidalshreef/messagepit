-- Projects and environments require explicit per-user project grants.
CREATE TABLE IF NOT EXISTS {{ tenant "team_projects" }} (
	ID TEXT PRIMARY KEY NOT NULL,
	GroupID TEXT NOT NULL,
	Name TEXT NOT NULL,
	Created INTEGER NOT NULL,
	UNIQUE (GroupID, Name)
);

CREATE TABLE IF NOT EXISTS {{ tenant "project_environments" }} (
	ID TEXT PRIMARY KEY NOT NULL,
	ProjectID TEXT NOT NULL,
	Name TEXT NOT NULL,
	Created INTEGER NOT NULL,
	UNIQUE (ProjectID, Name)
);

CREATE TABLE IF NOT EXISTS {{ tenant "project_memberships" }} (
	ProjectID TEXT NOT NULL,
	Issuer TEXT NOT NULL,
	Subject TEXT NOT NULL,
	Role TEXT NOT NULL CHECK (Role IN ('viewer', 'developer', 'admin')),
	PRIMARY KEY (ProjectID, Issuer, Subject)
);

CREATE INDEX IF NOT EXISTS {{ tenant "idx_project_members_subject" }} ON {{ tenant "project_memberships" }} (Issuer, Subject);
