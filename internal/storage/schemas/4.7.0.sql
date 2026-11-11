-- Correlation IDs group related captures within an environment.
CREATE TABLE IF NOT EXISTS {{ tenant "capture_correlations" }} (
	Kind TEXT NOT NULL CHECK (Kind IN ('email', 'sms', 'webhook')),
	RecordID TEXT NOT NULL,
	CorrelationID TEXT NOT NULL,
	Created INTEGER NOT NULL,
	PRIMARY KEY (Kind, RecordID)
);

CREATE INDEX IF NOT EXISTS {{ tenant "idx_correlation_lookup" }} ON {{ tenant "capture_correlations" }} (CorrelationID, Created DESC);

CREATE TRIGGER IF NOT EXISTS {{ tenant "delete_email_correlation" }}
AFTER DELETE ON {{ tenant "mailbox" }} BEGIN
	DELETE FROM {{ tenant "capture_correlations" }} WHERE Kind = 'email' AND RecordID = OLD.ID;
END;

CREATE TRIGGER IF NOT EXISTS {{ tenant "delete_sms_correlation" }}
AFTER DELETE ON {{ tenant "sms_mailbox" }} BEGIN
	DELETE FROM {{ tenant "capture_correlations" }} WHERE Kind = 'sms' AND RecordID = OLD.ID;
END;

CREATE TRIGGER IF NOT EXISTS {{ tenant "delete_webhook_correlation" }}
AFTER DELETE ON {{ tenant "webhook_requests" }} BEGIN
	DELETE FROM {{ tenant "capture_correlations" }} WHERE Kind = 'webhook' AND RecordID = OLD.ID;
END;
