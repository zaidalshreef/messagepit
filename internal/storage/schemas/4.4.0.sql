-- Keep ownership in sync with every existing deletion path, including retention.
CREATE TRIGGER IF NOT EXISTS {{ tenant "delete_email_ownership" }}
AFTER DELETE ON {{ tenant "mailbox" }} BEGIN
	DELETE FROM {{ tenant "message_ownership" }} WHERE Kind = 'email' AND RecordID = OLD.ID;
END;

CREATE TRIGGER IF NOT EXISTS {{ tenant "delete_sms_ownership" }}
AFTER DELETE ON {{ tenant "sms_mailbox" }} BEGIN
	DELETE FROM {{ tenant "message_ownership" }} WHERE Kind = 'sms' AND RecordID = OLD.ID;
END;

CREATE TRIGGER IF NOT EXISTS {{ tenant "delete_webhook_ownership" }}
AFTER DELETE ON {{ tenant "webhook_requests" }} BEGIN
	DELETE FROM {{ tenant "message_ownership" }} WHERE Kind = 'webhook' AND RecordID = OLD.ID;
END;
