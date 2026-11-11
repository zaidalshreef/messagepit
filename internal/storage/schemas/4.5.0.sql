-- Retention is opt-in per environment and applies to all capture types.
CREATE TABLE IF NOT EXISTS {{ tenant "environment_retention" }} (
	EnvironmentID TEXT PRIMARY KEY NOT NULL,
	MaxAgeSeconds INTEGER NOT NULL CHECK (MaxAgeSeconds >= 3600 AND MaxAgeSeconds <= 31536000)
);
