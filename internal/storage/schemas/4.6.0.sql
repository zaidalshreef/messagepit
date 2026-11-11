-- Record management actions without storing message bodies or credentials.
CREATE TABLE IF NOT EXISTS {{ tenant "audit_events" }} (
	ID INTEGER PRIMARY KEY AUTOINCREMENT,
	Created INTEGER NOT NULL,
	Issuer TEXT NOT NULL,
	Subject TEXT NOT NULL,
	ServiceTokenID TEXT NOT NULL,
	Action TEXT NOT NULL,
	EnvironmentID TEXT NOT NULL,
	ResourceID TEXT NOT NULL,
	Status INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS {{ tenant "idx_audit_created" }} ON {{ tenant "audit_events" }} (Created DESC);
