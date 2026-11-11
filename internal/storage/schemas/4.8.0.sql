-- Local users authenticate without an external identity provider.
CREATE TABLE IF NOT EXISTS {{ tenant "local_users" }} (
	Username TEXT PRIMARY KEY NOT NULL,
	PasswordHash TEXT NOT NULL,
	Admin INTEGER NOT NULL DEFAULT 0,
	Disabled INTEGER NOT NULL DEFAULT 0,
	Version INTEGER NOT NULL DEFAULT 1,
	Created INTEGER NOT NULL
);
