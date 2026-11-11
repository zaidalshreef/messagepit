-- Groups and their explicitly assigned members
CREATE TABLE IF NOT EXISTS {{ tenant "team_groups" }} (
	ID TEXT PRIMARY KEY NOT NULL,
	Name TEXT NOT NULL UNIQUE,
	Created INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS {{ tenant "group_memberships" }} (
	GroupID TEXT NOT NULL,
	Issuer TEXT NOT NULL,
	Subject TEXT NOT NULL,
	PRIMARY KEY (GroupID, Issuer, Subject)
);

CREATE INDEX IF NOT EXISTS {{ tenant "idx_group_members_subject" }} ON {{ tenant "group_memberships" }} (Issuer, Subject);
