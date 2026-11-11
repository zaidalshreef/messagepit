-- Legacy records belong to a reserved default environment until explicitly moved.
CREATE TABLE IF NOT EXISTS {{ tenant "message_ownership" }} (
	Kind TEXT NOT NULL CHECK (Kind IN ('email', 'sms', 'webhook')),
	RecordID TEXT NOT NULL,
	EnvironmentID TEXT NOT NULL,
	PRIMARY KEY (Kind, RecordID)
);

CREATE INDEX IF NOT EXISTS {{ tenant "idx_ownership_environment" }} ON {{ tenant "message_ownership" }} (EnvironmentID, Kind);

INSERT INTO {{ tenant "team_groups" }} (ID, Name, Created)
VALUES ('__messagepit_legacy_group__',
	CASE WHEN EXISTS (SELECT 1 FROM {{ tenant "team_groups" }} WHERE Name = 'Legacy data')
		THEN 'Legacy data (' || lower(hex(randomblob(16))) || ')'
		ELSE 'Legacy data' END, 0)
ON CONFLICT(ID) DO NOTHING;

INSERT INTO {{ tenant "team_projects" }} (ID, GroupID, Name, Created)
VALUES ('__messagepit_legacy_project__', '__messagepit_legacy_group__', 'Default', 0)
ON CONFLICT(ID) DO NOTHING;

INSERT INTO {{ tenant "project_environments" }} (ID, ProjectID, Name, Created)
VALUES ('__messagepit_legacy_environment__', '__messagepit_legacy_project__', 'default', 0)
ON CONFLICT(ID) DO NOTHING;
