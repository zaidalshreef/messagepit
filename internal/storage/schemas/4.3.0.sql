-- Short-lived service tokens are bound to one environment and role.
CREATE TABLE IF NOT EXISTS {{ tenant "service_tokens" }} (
	ID TEXT PRIMARY KEY NOT NULL,
	EnvironmentID TEXT NOT NULL,
	Name TEXT NOT NULL,
	Role TEXT NOT NULL CHECK (Role IN ('viewer', 'developer')),
	SecretHash TEXT NOT NULL,
	Created INTEGER NOT NULL,
	Expires INTEGER NOT NULL,
	Revoked INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS {{ tenant "idx_service_tokens_environment" }} ON {{ tenant "service_tokens" }} (EnvironmentID);
