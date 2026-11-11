# MessagePit

An email **and SMS** testing tool for developers. Send test emails and SMS messages from your application and inspect them in a clean web UI — nothing reaches real inboxes or real phones.

MessagePit is a fork of [Mailpit](https://github.com/axllent/mailpit) extended with a Twilio-compatible SMS ingest endpoint, a SendGrid v3 Mail Send API stub, SMS/email delivery status callbacks, an SMS inbox UI, and a dedicated SMS ingest server.

![MessagePit web UI with Email, SMS and Webhooks tabs and an inbox of captured test emails](server/ui-src/screenshot.png)

## Features

- **Email**: SMTP server, SendGrid v3 API stub, web UI, REST API, WebSocket live updates, search, tagging, POP3 server
- **SMS**: Twilio-compatible HTTP ingest, SMS inbox with read/unread tracking, live WebSocket updates
- **Webhook capture**: Dedicated HTTP server that captures any incoming request on any path/method and displays it in the UI — useful for inspecting outbound webhook calls from your app in development
- **Delivery callbacks**: Signed SMS status progressions (`queued` → `sent` → `delivered`) and full SendGrid event lifecycles, with magic numbers and scenario triggers for the failure branches
- **Shared**: Multi-arch Docker image, optional HTTP basic auth, Prometheus metrics

## Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 1025 | SMTP | Email ingest (mirrors port 25) |
| 1110 | POP3 | POP3 server (optional) |
| 8025 | HTTP | Web UI and management API |
| 8100 | HTTP | SendGrid v3 Mail Send stub — `POST /v3/mail/send` |
| 8200 | HTTP | SMS ingest — Twilio-compatible |
| 8300 | HTTP | Webhook capture — accepts any request on any path/method |

## Quick Start

```bash
# Docker
docker run -p 1025:1025 -p 8025:8025 -p 8100:8100 -p 8200:8200 -p 8300:8300 \
  -e MP_SENDGRID_BIND_ADDR=0.0.0.0:8100 ghcr.io/coreydaley/messagepit

# From source
make run
```

Open [http://localhost:8025](http://localhost:8025) in your browser.

## SMS Integration

Point your application's Twilio SDK at the SMS ingest server instead of `api.twilio.com`:

```
http://localhost:8200
```

The SMS server implements the Twilio Messages API endpoint:

```
POST /2010-04-01/Accounts/{AccountSid}/Messages.json
```

Required form fields: `From`, `To`, `Body`. Authentication uses HTTP Basic Auth (`AccountSid`:`AuthToken`).

### SMS status callbacks

When `MP_TWILIO_WEBHOOK_URL` is set (or a per-request `StatusCallback` form field is provided), MessagePit replays the message's full **status progression** to that URL — one signed `POST` per state change, exactly as Twilio does:

```
queued → sent → delivered
```

The `POST /Messages.json` response still reports `"status": "queued"`, matching real Twilio: the terminal state only ever arrives via callback.

Each callback body is `application/x-www-form-urlencoded`:

| Field | Notes |
|---|---|
| `MessageSid` / `SmsSid` | Stable across the whole progression |
| `MessageStatus` / `SmsStatus` | `queued`, `sent`, `delivered`, `undelivered`, `failed` |
| `To`, `From` | As submitted |
| `AccountSid` | From the request path |
| `ApiVersion` | `2010-04-01` |
| `ErrorCode`, `ErrorMessage` | Present only on `failed` / `undelivered` |

When `MP_TWILIO_AUTH_TOKEN` is set, every callback carries an `X-Twilio-Signature` HMAC-SHA1 header so your handler can validate it with the standard Twilio SDK.

**Priority**: the `StatusCallback` field in the send request takes precedence over the global `MP_TWILIO_WEBHOOK_URL`.

Set `--twilio-callback-delay` (`MP_TWILIO_CALLBACK_DELAY`, e.g. `750ms`) to space the callbacks out. The default is `0` — the states still arrive in order, just without the wall-clock gap. Callback logs report the event and response status without recording the destination URL or recipient address.

#### Magic numbers

Twilio reserves the `+1500555xxxx` range for test numbers. MessagePit honours the real ones and adds its own in the same range for the delivery-failure branches, which real Twilio has no way to trigger on demand.

**API rejections** (real Twilio behaviour) — the send returns HTTP 400 with a Twilio error code, nothing is stored, and no callback fires:

| Number | Field | Code | Meaning |
|---|---|---|---|
| `+15005550001` | `To` | 21211 | Not a valid phone number |
| `+15005550002` | `To` | 21612 | Not currently reachable via SMS |
| `+15005550003` | `To` | 21408 | Region not enabled for SMS |
| `+15005550004` | `To` | 21610 | Unsubscribed recipient |
| `+15005550009` | `To` | 21614 | Not a valid mobile number |
| `+15005550001` | `From` | 21212 | Not a valid sender |
| `+15005550007` | `From` | 21606 | Not an SMS-capable number on this account |
| `+15005550008` | `From` | 21611 | Sender queue full |

**Delivery failures** (MessagePit extension) — the send succeeds; the failure surfaces through the callback progression:

| `To` number | Progression | `ErrorCode` |
|---|---|---|
| `+15005550010` | `queued` → `failed` | 30008 (unknown error) |
| `+15005550011` | `queued` → `sent` → `undelivered` | 30003 (unreachable handset) |
| `+15005550012` | `queued` → `sent` → `undelivered` | 30005 (unknown handset) |
| `+15005550013` | `queued` → `sent` → `undelivered` | 30006 (landline / unreachable carrier) |
| `+15005550014` | `queued` → `sent` | — (carrier never confirms) |

## Webhook Capture

MessagePit runs a dedicated HTTP server (port 8300 by default) that accepts any incoming HTTP request on any path and method, stores it, and displays it in the **Webhooks** tab of the UI. This is useful for developing and testing outbound webhook delivery from your application without needing a public endpoint.

Point your webhook URL at the capture server:

```
http://localhost:8300/any/path/you/like
```

Any HTTP method works: `POST`, `GET`, `PUT`, `PATCH`, `DELETE`. The full request — method, path, headers, body, source IP — is captured and displayed in real time via WebSocket.

The capture server is enabled by default. Pass `--webhook ""` to disable it. (Setting `MP_WEBHOOK_BIND_ADDR=""` does not disable it — an empty environment variable is ignored and the default is used.)

### Webhook capture API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/webhooks` | List captured requests (paginated) |
| GET | `/api/v1/webhooks/search` | Search captured requests |
| GET | `/api/v1/webhook/{id}` | Get a single captured request |
| DELETE | `/api/v1/webhook/{id}` | Delete a single captured request |
| DELETE | `/api/v1/webhooks` | Delete all captured requests |

## Email Integration (SendGrid v3)

MessagePit exposes a SendGrid v3 Mail Send stub on port 8100:

```
POST /v3/mail/send
Authorization: Bearer <MP_SENDGRID_API_KEY>
Content-Type: application/json
```

Point your application's SendGrid SDK at the stub by setting the API base URL to `http://localhost:8100`. The endpoint accepts the supported SendGrid v3 JSON fields (`personalizations`, `from`, `subject`, `content`, `custom_args`) and stores each message in the MessagePit mailbox. In standalone mode, authentication is skipped when `MP_SENDGRID_API_KEY` is empty. Shared mode requires an environment-scoped developer credential.

Custom header names must be valid and unique, ignoring case; reserved SendGrid and generated MIME headers cannot be overridden. Content types must be valid MIME media types, and To, Cc, and Bcc addresses must be valid. Invalid input is rejected before any message is stored.

The SendGrid server defaults to `127.0.0.1:8100` (loopback only). Set `MP_SENDGRID_BIND_ADDR=0.0.0.0:8100` to expose it on all interfaces (e.g. inside Docker). Pass `--sendgrid ""` to disable it entirely (an empty `MP_SENDGRID_BIND_ADDR` is ignored).

### Email event webhooks

When `MP_EMAIL_WEBHOOK_URL` is set, MessagePit replays the **full SendGrid event lifecycle** for every captured email — one `POST` per event, in order:

```
processed → delivered
```

Real SendGrid fires its event webhook for everything it accepts, over both the v3 API and its SMTP relay, so MessagePit does the same. No `custom_args` are required.

Each request body is a JSON array holding a single event (SendGrid batches events that land in the same window; MessagePit sends them individually so the ordering stays visible in development):

```json
[
  {
    "email": "recipient@example.com",
    "timestamp": 1714000000,
    "smtp-id": "<abc123@messagepit>",
    "event": "delivered",
    "response": "250 2.0.0 OK",
    "category": ["welcome"],
    "sg_event_id": "rbtnWrG1DVDGGGFHFdun0A",
    "sg_message_id": "142d9f3f351f2dad77c8.messagepit",
    "notification_id": "<value from custom_args>"
  }
]
```

Every key in `custom_args` is flattened onto each event as a top-level field, exactly as real SendGrid does — so `notification_id` keeps working unchanged. `categories` from the v3 payload arrive as `category`.

Event-specific fields match the real API: `response` on `delivered`/`deferred`, `reason` + `status` + `type` + `bounce_classification` on `bounce`, `reason` + `status` on `dropped`, `useragent` + `ip` on `open`/`click`, and `url` + `url_offset` on `click`.

#### Delivery scenarios

The default lifecycle is `processed → delivered`. To exercise the failure and engagement branches, select a scenario either with a `mp_scenario` custom arg or by addressing the recipient with a matching local part or plus-tag (`bounce@example.com`, `user+bounce@example.com`). An explicit `mp_scenario` wins over the address.

| Scenario | Event sequence |
|---|---|
| `delivered` (default) | `processed` → `delivered` |
| `open` | `processed` → `delivered` → `open` |
| `click` | `processed` → `delivered` → `open` → `click` |
| `deferred` | `processed` → `deferred` → `delivered` |
| `bounce` | `processed` → `bounce` (`type: bounce`, 5.1.1) |
| `blocked` | `processed` → `bounce` (`type: blocked`, 5.7.1) |
| `dropped` | `processed` → `dropped` |
| `spamreport` | `processed` → `delivered` → `spamreport` |
| `unsubscribe` | `processed` → `delivered` → `unsubscribe` |
| `group_unsubscribe` | `processed` → `delivered` → `group_unsubscribe` |
| `group_resubscribe` | `processed` → `delivered` → `group_resubscribe` |

```json
{
  "personalizations": [{ "to": [{ "email": "user@example.com" }] }],
  "custom_args": { "notification_id": "42", "mp_scenario": "bounce" }
}
```

Set `--email-webhook-event-delay` (`MP_EMAIL_WEBHOOK_EVENT_DELAY`, e.g. `2s`) to space the events out. The default is `0`.

Webhooks are signed using ECDSA P-256 / SHA-256, with the signature and timestamp in the same headers real SendGrid uses:

| Header | Description |
|--------|-------------|
| `X-Twilio-Email-Event-Webhook-Signature` | Base64-encoded ECDSA signature |
| `X-Twilio-Email-Event-Webhook-Timestamp` | Unix timestamp as a string |

The payload that is signed is `timestamp + body` (timestamp string concatenated with the raw JSON body).

#### Key pair setup

Provide a stable key pair so the public key doesn't change across restarts:

```bash
# Generate the private key once (SEC1 DER, base64) — set as MP_EMAIL_WEBHOOK_SIGNING_KEY
export MP_EMAIL_WEBHOOK_SIGNING_KEY="$(openssl ecparam -name prime256v1 -genkey -noout \
  | openssl ec -outform DER 2>/dev/null \
  | base64 | tr -d '\n')"

# Derive the matching public key from THAT private key (PKIX DER, base64)
# — set as SENDGRID_WEBHOOK_PUBLIC_KEY in your app
printf '%s' "$MP_EMAIL_WEBHOOK_SIGNING_KEY" | base64 -d \
  | openssl ec -inform DER -pubout -outform DER 2>/dev/null \
  | base64 | tr -d '\n'
```

The public key must be derived from the same private key — running the generate step twice gives two unrelated keys and every signature check fails.

```bash
# (optional) sanity-check the private key
printf '%s' "$MP_EMAIL_WEBHOOK_SIGNING_KEY" | base64 -d | openssl ec -inform DER -noout -check
```

If `MP_EMAIL_WEBHOOK_SIGNING_KEY` is empty and `MP_EMAIL_WEBHOOK_URL` is set, MessagePit auto-generates a key pair at startup and logs the public key — useful for one-off testing but not stable across restarts.

#### SMTP vs. SendGrid v3

Events fire for **both** paths, mirroring SendGrid's own SMTP relay. Messages sent over SMTP simply have no `custom_args` to flatten onto the events unless you set them yourself: MessagePit reads SendGrid's `X-SMTPAPI` header (`{"category":[...],"unique_args":{...}}`), which the v3 handler also uses internally to carry that metadata through storage. Scenario selection by recipient address works over SMTP too, or set `X-MessagePit-Scenario` directly.

## Building

Requires Go 1.21+ and Node 22+.

```bash
make run     # build UI + binary and run with dev defaults
make install # build UI + binary and install to $GOPATH/bin
make test    # run Go test suite
make ui      # build frontend assets only
make build   # compile the binary only (requires ui assets)
```

## Configuration

All flags can also be set via environment variables (e.g. `--smtp` → `MP_SMTP_BIND_ADDR`, `--twilio` → `MP_TWILIO_BIND_ADDR`).

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--smtp` | `MP_SMTP_BIND_ADDR` | `0.0.0.0:1025` | SMTP bind address |
| `--sendgrid` | `MP_SENDGRID_BIND_ADDR` | `127.0.0.1:8100` | SendGrid v3 Mail Send API bind address (`--sendgrid ""` to disable) |
| `--sendgrid-api-key` | `MP_SENDGRID_API_KEY` | | Expected Bearer token for `/v3/mail/send` (skipped when empty) |
| `--twilio` | `MP_TWILIO_BIND_ADDR` | `[::]:8200` | Twilio SMS ingest bind address |
| `--twilio-auth-token` | `MP_TWILIO_AUTH_TOKEN` | | Twilio auth token — validates Basic Auth on inbound SMS; signs outgoing delivery callbacks |
| `--twilio-webhook-url` | `MP_TWILIO_WEBHOOK_URL` | | URL to POST SMS status callbacks to (fallback when no per-request `StatusCallback`) |
| `--twilio-callback-delay` | `MP_TWILIO_CALLBACK_DELAY` | `0s` | Delay between callbacks in the `queued`/`sent`/`delivered` progression |
| `--webhook` | `MP_WEBHOOK_BIND_ADDR` | `[::]:8300` | HTTP webhook capture bind address (`--webhook ""` to disable) |
| `--listen` | `MP_UI_BIND_ADDR` | `0.0.0.0:8025` | HTTP UI/API bind address |
| `--db` | `MP_DATABASE` | *(in-memory)* | SQLite database file path |
| `--email-webhook-url` | `MP_EMAIL_WEBHOOK_URL` | | URL to POST email delivery event webhooks to |
| `--email-webhook-signing-key` | `MP_EMAIL_WEBHOOK_SIGNING_KEY` | | Base64-encoded SEC1 DER ECDSA P-256 private key (auto-generated when empty) |
| `--email-webhook-event-delay` | `MP_EMAIL_WEBHOOK_EVENT_DELAY` | `0s` | Delay between events in the email delivery lifecycle |

Run `messagepit --help` for the full list.

HTML stylesheet checks, link checks, and the screenshot asset proxy block requests to loopback, private, link-local, and multicast addresses by default. `--allow-internal-http-requests` (or `MP_ALLOW_INTERNAL_HTTP_REQUESTS`) allows them when local resources are needed. This setting applies to all three features, so enable it only when captured message content is trusted. Outbound network controls should also restrict any other internal ranges used by your deployment.

### Local sign-in for shared projects

Local username and password accounts let teams use shared projects without an SSO service. On the first start, choose an administrator password of 12–72 UTF-8 bytes. For example, on a local development machine:

```sh
export MP_LOCAL_AUTH=1 MP_UI_BIND_ADDR=127.0.0.1:8025
export MP_DATABASE="$PWD/messagepit.db"
read -r -s -p 'Initial administrator password: ' MP_LOCAL_ADMIN_PASSWORD; echo
export MP_LOCAL_ADMIN_PASSWORD
messagepit
unset MP_LOCAL_ADMIN_PASSWORD
```

Open `http://127.0.0.1:8025` and sign in as `admin`. The administrator can create local users in **Groups → Local accounts**, add them to groups, and grant each project separately. There is no public self-registration. Usernames start with a lowercase letter or digit and contain 3–64 lowercase letters, digits, dots, underscores, or hyphens. Users can change their own password; administrators can reset a user's password or disable an account. Password changes invalidate existing sessions, and disabling an account removes its group and project grants. Usernames of disabled accounts cannot be reused.

`MP_LOCAL_ADMIN_USERNAME` changes the initial administrator username. The bootstrap password is used only when the local account table is empty; changing it later does not replace an existing password. The application stores bcrypt password hashes rather than passwords. Sign-in attempts are rate-limited, and browser sessions expire after eight hours or on sign-out or restart. Service credentials belong to an environment rather than the person who issued them, so revoke any credentials that should stop working when an account is disabled.

The default local public origin is `http://127.0.0.1:8025`. Set `MP_LOCAL_PUBLIC_URL` to the exact public origin when using a different host or port. Remote origins require HTTPS; when TLS terminates at a proxy, connect the proxy to a loopback-bound MessagePit listener. Local authentication cannot be combined with OIDC or the existing UI, Send API, SMTP, or POP3 password-file options. The normal standalone mode remains available when shared authentication is not enabled.

For offline administrator recovery, stop MessagePit, set `MP_LOCAL_ADMIN_PASSWORD` to a new password, and run `messagepit reset-local-admin /path/to/messagepit.db`. If the initial administrator had a custom username, add `--username <name>`; if the database uses a tenant prefix, add `--tenant-id <id>`. The command updates the existing account and invalidates its old sessions. Unset the password environment variable afterward.

### Optional OIDC sign-in

OIDC sign-in is optional. Configure an issuer, client ID, public origin, and a comma-separated allowlist of OIDC `sub` values using `MP_OIDC_ISSUER`, `MP_OIDC_CLIENT_ID`, `MP_OIDC_PUBLIC_URL`, and `MP_OIDC_ALLOWED_SUBJECTS`. Set `MP_OIDC_CLIENT_SECRET` securely if the provider requires a confidential client. Register `<public-origin><webroot>auth/callback` as the redirect URI. The provider must support the authorization-code flow with PKCE.

The issuer, public origin, and provider endpoints require HTTPS; HTTP is accepted only for localhost development. OIDC cannot be combined with UI, Send API, SMTP, or POP3 password-file authentication. Browser sessions expire at the identity token's expiry or after eight hours, whichever comes first; they also end on sign-out or process restart. Sign-out ends the MessagePit session but does not sign the user out of the identity provider. Allowed subjects must be maintained in the configuration even after they are added to a group.

### Shared access

Shared local-account or OIDC mode requires a local SQLite database. The existing rqlite driver does not provide transactional commits or rollbacks, so it is rejected in shared mode to protect project isolation. Standalone rqlite use remains available. New shared database files are created with owner-only permissions; restrict access to existing database files and backups as well.

Only system administrators can access the existing instance-wide mailbox. Existing captures are associated with a reserved default environment; grant access to that project explicitly if a user needs it. System administrators can see all captures through the existing mailbox. Other users can inspect only the projects explicitly shared with them through the Projects page and environment APIs.

When shared authentication is enabled, capture listeners require an environment-scoped developer credential. SendGrid and webhook capture accept it as a bearer token; Twilio uses it as the Basic Auth password with the account SID as the username; SMTP uses `mpt_<token-id>` as the username and the entire token as the password. Credentials can be issued and revoked by a system or project administrator. Viewer tokens can read but cannot capture or manage credentials. Tokens expire after at most 24 hours, and their secret is displayed only once.

SMTP authentication requires TLS unless `--smtp-auth-allow-insecure` is explicitly enabled for local development. With shared authentication enabled and neither TLS nor that option configured, SMTP remains closed to unauthenticated submissions. HTTP service credentials are accepted over TLS or a direct loopback connection; bind the HTTP capture listeners to loopback behind a local TLS-terminating proxy when they must be used remotely. Forwarded headers alone do not establish a trusted connection.

In shared mode, SMTP and webhook capture reject bodies over 10 MiB; SendGrid uses the existing 10 MiB limit and SMS requests are limited to 1 MiB.

Scoped captures do not trigger legacy instance-wide broadcasts, tags, forwarding, relay, or delivery callbacks. The project inbox receives environment-scoped live change notifications and refreshes its list; while inspecting a record or viewing a later page, it offers a refresh button instead. Notifications contain no message content, and access is rechecked throughout the connection. Live notifications are local to one MessagePit process; reconnecting refreshes the list. Environment administrators can optionally set an age limit from one hour to 365 days; a background task removes older email, SMS, and webhook records in bounded batches. Cleanup is disabled by default. With shared authentication enabled, the existing instance-wide mailbox limits apply to the legacy environment only. Scoped delivery callbacks are not yet implemented.

System administrators can view recent sign-ins, sign-outs, and authenticated management actions in the project workspace or through `GET /api/v1/audit`. Audit entries omit request bodies and token secrets. Recording is best effort and occurs separately from the action; failed sign-ins and individual capture requests are not currently recorded, and the log is not tamper-evident.

### Test shared sign-in locally with Dex

A temporary Dex identity provider can exercise shared access before an external SSO provider is available. Install Go 1.25, Docker, Node.js, and the frontend dependencies, then run:

```sh
npm ci
bash scripts/dev-sso.sh
```

The script builds MessagePit, starts Dex 2.46.1, and prints the local UI URL and the path to a private credentials file. It creates separate administrator and developer accounts with fresh random passwords. The credentials file also contains each account's OIDC subject; use the developer's subject to add that account to a group and explicitly grant it access to a project. Dex listens on `127.0.0.1:18880` and the UI, SMTP, SendGrid, Twilio, and webhook listeners use loopback ports `18825`–`18829`. This local test permits SMTP authentication without TLS; do not expose these listeners to a network. Press Ctrl-C to stop the script, or send SIGTERM to the process ID in the printed `session.pid` file. The temporary database, credentials, and Dex container are removed when the script stops.

To switch to MIM STS later, obtain its exact OIDC issuer URL, register the MessagePit callback URL with the provider, and configure the `MP_OIDC_*` values described above. User identities and project grants are keyed by issuer and subject, so grants made for the Dex test accounts do not automatically transfer to MIM identities; administrators must verify identities and assign access again.

### Optional AI summaries

To enable user-requested summaries, configure `MP_AI_BASE_URL` and `MP_AI_MODEL`; set `MP_AI_API_KEY` securely if the inference server requires it. The base URL can be an OpenAI-compatible API base such as `https://vllm-mimai-beta.mim.gov.sa/v1` or its `/v1/models` URL. Shared local or OIDC authentication must also be enabled. Remote endpoints require HTTPS with certificate verification; HTTP is accepted only for localhost, and redirects are refused.

The project inbox shows a **Summarize with AI** action only when configured. Selecting it sends the selected email body and subject, SMS body, or webhook method, path, and body to the configured service; webhook headers and query strings are excluded. Input is limited to 16 KiB. Captured data may contain sensitive information, and generated summaries can be inaccurate. Data is not sent to AI automatically.

### Groups

Local administrators can create groups and manage members in the Groups page. If optional OIDC is configured, set `MP_OIDC_ADMIN_SUBJECTS` to a comma-separated subset of the allowed OIDC subject IDs to grant group administration. Other users can see only their own groups. Group membership does not grant project access.

## API

The REST API is documented at [http://localhost:8025/api/v1](http://localhost:8025/api/v1).

With shared authentication enabled, `GET /api/v1/me` returns the current identity, and `/api/v1/groups` provides group listing and administrator-only creation. Administrators can delete `/api/v1/groups/{id}` and add or remove members with `/api/v1/groups/{id}/members`. Group members can list their own group's members.

Project metadata is available through `/api/v1/groups/{id}/projects`, and explicit viewer, developer, or administrator grants can be managed through `/api/v1/projects/{id}/members`. Project environments are available through `/api/v1/projects/{id}/environments`. Within an authorized environment, use `/api/v1/environments/{env}/messages`, `/sms`, or `/webhooks` to list records; append a record ID to inspect it. Email raw content, headers, HTML checks, and attachments are available under `/messages/{id}/raw`, `/headers`, `/html-check`, and `/part/{partID}`. Add `?query=...` to search: email supports existing search filters, SMS searches addresses and body, and webhook search includes request headers and the full body.

`GET /api/v1/environments/{env}/events` upgrades to a WebSocket that sends `changed` after captures or deletions in the authorized environment. Clients fetch data separately through scoped APIs; the connection closes when access expires or is revoked.

`GET /api/v1/environments/{env}/search?query=...` searches all three channels at once and returns counts plus the first 20 matches per channel. The project workspace offers the same cross-channel search under **All**, with buttons to load further matches from each channel.

Developers and project administrators can delete an individual record with `DELETE /api/v1/environments/{env}/{messages|sms|webhooks}/{id}`. Administrators can set `PUT /api/v1/environments/{env}/retention` with `{"max_age_seconds":3600}` or set it to zero to disable automatic cleanup.

For a test that produces several messages, include the same `X-MessagePit-Correlation-ID` header on SMTP email, SendGrid, Twilio, and webhook requests. IDs may contain up to 128 letters, digits, `.`, `_`, `-`, or `:`. `GET /api/v1/environments/{env}/correlations/{id}` returns up to 100 related record IDs and kinds from that environment, which can then be inspected through the scoped APIs.

In the project workspace, select **Test run** and enter the correlation ID to see related email, SMS, and webhooks together. Select a capture to inspect it. The timeline is also available at `GET /api/v1/environments/{env}/correlations/{id}/timeline`, with zero-based `start` and a `limit` of 1–50 (20 by default). It returns capture previews and a total without the 100-record limit of the ID-only lookup. Each page reflects the current records; captures added or deleted while paging can change later pages.

The included `scripts/assert-captures.sh` waits for a correlated workflow and asserts minimum capture counts, optional content expectations, and optional channel absence. It requires `curl`, `jq`, `MESSAGEPIT_URL`, `MESSAGEPIT_ENVIRONMENT_ID`, `MESSAGEPIT_CORRELATION_ID`, and a short-lived viewer or developer `MESSAGEPIT_TOKEN`:

```sh
MESSAGEPIT_EXPECT_EMAILS=1 MESSAGEPIT_EXPECT_SMS=1 MESSAGEPIT_WAIT_SECONDS=60 \
  bash scripts/assert-captures.sh
```

Set `MESSAGEPIT_EXPECT_WEBHOOKS` to include webhook captures. The default is one email, no SMS or webhooks, and a 30-second wait. Counts are minimums: zero means no minimum, not an assertion that a channel is empty. The correlation lookup returns at most the newest 100 records, and the requested counts must total at most 100.

To check that a channel has no captures for the correlation ID, set `MESSAGEPIT_EXPECT_NO_EMAILS=1`, `MESSAGEPIT_EXPECT_NO_SMS=1`, or `MESSAGEPIT_EXPECT_NO_WEBHOOKS=1`. For example, this waits five seconds for an email while checking that no SMS is captured:

```sh
MESSAGEPIT_EXPECT_EMAIL_SUBJECT='Welcome' MESSAGEPIT_EXPECT_NO_SMS=1 \
  MESSAGEPIT_WAIT_SECONDS=5 bash scripts/assert-captures.sh
```

The helper polls approximately once per second and takes a final snapshot after the full wait. A wait of zero checks once. If an absence check reaches the 100-record correlation limit, it fails because older records might be hidden. It observes records still retained at each poll; a capture deleted between polls can be missed, so use a unique correlation ID and avoid cleanup during the test. Presence and absence cannot be required for the same channel. `MESSAGEPIT_EXPECT_NO_EMAILS=1` changes the default email minimum to zero unless an explicit minimum is supplied.

Use the optional expectations below to require matching content. When several expectations are given for one channel, all must match the same record, and that channel's minimum applies to matching records. An expectation with a minimum of zero requires at least one match. Empty expectations are ignored.

| Environment variable | Match |
| --- | --- |
| `MESSAGEPIT_EXPECT_EMAIL_SUBJECT` | Email subject contains this text. |
| `MESSAGEPIT_EXPECT_EMAIL_BODY` | Email text or HTML body contains this text. |
| `MESSAGEPIT_EXPECT_EMAIL_TO` | An address in the email To list matches, ignoring case. |
| `MESSAGEPIT_EXPECT_SMS_BODY` | SMS body contains this text. |
| `MESSAGEPIT_EXPECT_SMS_TO` | SMS recipient matches this number exactly. |
| `MESSAGEPIT_EXPECT_WEBHOOK_METHOD` | Webhook method matches, ignoring case. |
| `MESSAGEPIT_EXPECT_WEBHOOK_PATH` | Webhook path matches exactly. |
| `MESSAGEPIT_EXPECT_WEBHOOK_BODY` | Webhook body contains this text. |

For example:

```sh
MESSAGEPIT_EXPECT_EMAIL_SUBJECT='Welcome' MESSAGEPIT_EXPECT_EMAIL_TO='tester@example.com' \
  MESSAGEPIT_EXPECT_SMS_BODY='verification code' MESSAGEPIT_WAIT_SECONDS=60 \
  bash scripts/assert-captures.sh
```

Substring matching is case-sensitive. Expectations are limited to 1024 characters each. The helper prints neither captured content nor the credential. It accepts HTTPS URLs or HTTP URLs on `localhost`, `127.0.0.1`, or `[::1]`. Store the token as a masked CI variable and pass the correlation ID to the application under test.

`GET` and `POST /api/v1/environments/{env}/tokens` list metadata and issue credentials; `DELETE /api/v1/environments/{env}/tokens/{id}` revokes one. For example, a project administrator can issue a one-hour developer credential with:

```json
{"name":"GitLab CI","role":"developer","ttl_seconds":3600}
```

`GET /api/v1/environments/{env}/{messages|sms}/{id}/otp` retrieves a labeled numeric verification code from an authorized email or SMS. It returns 404 when no code is found and 409 when different codes are present.

For an automated test run, `GET /api/v1/environments/{env}/correlations/{id}/otp?kind=messages` or `?kind=sms` checks correlated captures in the authorized environment. Repeated occurrences of the same code are accepted; different codes return 409. A response of 404 means no matching code was found or the environment is inaccessible, and 503 means a capture could not be read and the request can be retried. The lookup returns 409 if the correlation index reaches its 100-record limit, since older records could contain a different code. Records in every channel count toward this limit. Responses are marked `no-store`.

Use a unique correlation ID for each test and wait for its expected capture before retrieving the code. The lookup examines the captures available when it starts; a later capture can introduce another code. For example, in a CI job with `curl`, `jq`, and a short-lived viewer token in the masked variable `MESSAGEPIT_TOKEN`:

```sh
set -euo pipefail
set +x
MESSAGEPIT_EXPECT_EMAILS=0 MESSAGEPIT_EXPECT_SMS=1 \
  MESSAGEPIT_EXPECT_SMS_BODY='verification code' bash scripts/assert-captures.sh
api="${MESSAGEPIT_URL%/}/api/v1/environments/$MESSAGEPIT_ENVIRONMENT_ID"
code=$(printf 'header = "Authorization: Bearer %s"\n' "$MESSAGEPIT_TOKEN" |
  curl -q --config - --noproxy '*' --proto '=http,https' --fail --silent --show-error \
    --max-time 10 --url "$api/correlations/$MESSAGEPIT_CORRELATION_ID/otp?kind=sms" |
  jq -er '.code')
```

The assertion helper validates the URL and waits for a matching message. Avoid printing the token or code in CI logs. The example connects directly; deployments that require a proxy must configure a trusted proxy for their environment.

`GET /api/v1/environments/{env}/{messages|sms}/{id}/simulation` returns the expected test delivery sequence, including error codes for Twilio magic-number failure scenarios. This is a simulation, and it does not send callbacks.

The included GitLab pipeline builds and tests MessagePit without publishing it. Automated exchange of GitLab-issued identities for MessagePit credentials is not yet implemented; a project administrator must provision the short-lived credential for a job.

The project workspace uses the Ministry of Industry and Mineral Resources logo from its [public website](https://www.mim.gov.sa/ar) and colors informed by that site. Organization-specific brand guidelines and accessibility review remain to be confirmed.

### SQLite backup and restore

Use a persistent `MP_DATABASE` path for data that must survive restarts. SQLite's online backup API or the `sqlite3` `.backup` command creates a consistent backup even when WAL mode is enabled:

```sh
sqlite3 /path/to/messagepit.db ".backup '/secure/backups/messagepit.db'"
```

Store the backup with restricted access: it contains captured messages, access grants, and credential hashes. To restore, stop MessagePit, place the backup at a new database path, and start MessagePit with `MP_DATABASE` pointing to that file. Browser sessions are not restored and users must sign in again.

SMS endpoints:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/sms/messages` | List SMS messages |
| GET | `/api/v1/sms/message/{id}` | Get single SMS message |
| PUT | `/api/v1/sms/message/{id}/read` | Mark as read |
| DELETE | `/api/v1/sms/message/{id}` | Delete message |
| DELETE | `/api/v1/sms/messages` | Delete all messages |

## Docker

Images are published to the GitHub Container Registry for each tagged release (`v*`); `latest` is the most recent release. Features listed under "Unreleased" in the [CHANGELOG](CHANGELOG.md) are only on `main` — build from source to try them.

```bash
docker pull ghcr.io/coreydaley/messagepit:latest
```

### docker-compose example

The example below enables full delivery callback support for both SMS and email. The `extra_hosts` entry is required on Linux so `host.docker.internal` resolves to the host machine (Docker Desktop handles this automatically on Mac and Windows).

```yaml
services:
  messagepit:
    image: ghcr.io/coreydaley/messagepit:latest
    restart: always
    extra_hosts:
      - "host.docker.internal:host-gateway"
    ports:
      - "1025:1025"   # SMTP
      - "8025:8025"   # Web UI
      - "8100:8100"   # SendGrid v3 Mail Send stub
      - "8200:8200"   # SMS ingest (Twilio-compatible)
      - "8300:8300"   # Webhook capture
    environment:
      # SendGrid v3 stub — must match SENDGRID_API_KEY in your app
      MP_SENDGRID_API_KEY: your-sendgrid-api-key
      # Expose SendGrid on all interfaces inside the container
      MP_SENDGRID_BIND_ADDR: "0.0.0.0:8100"

      # Twilio SMS — must match TWILIO_AUTH_TOKEN in your app
      MP_TWILIO_AUTH_TOKEN: your-twilio-auth-token
      # Fallback SMS callback URL (per-request StatusCallback takes priority)
      MP_TWILIO_WEBHOOK_URL: http://host.docker.internal:3000/webhooks/v1/sms
      # Email delivery webhook URL
      MP_EMAIL_WEBHOOK_URL: http://host.docker.internal:3000/webhooks/v1/email
      # ECDSA private key for signing email webhooks (generate with openssl, see above)
      MP_EMAIL_WEBHOOK_SIGNING_KEY: "<base64-encoded SEC1 DER private key>"
    healthcheck:
      test: ["CMD", "/messagepit", "readyz"]
      interval: 10s
      timeout: 5s
      retries: 5
```

## Rails Integration

MessagePit is designed to be a drop-in local replacement for Twilio and SendGrid, using the same official gems your production app uses.

### Gems

```ruby
# Gemfile
gem "twilio-ruby", "~> 7"
gem "sendgrid-actionmailer"
```

### Environment variables (`.env.development`)

```bash
# SMS
TWILIO_ACCOUNT_SID=test
TWILIO_AUTH_TOKEN=test           # must match MP_TWILIO_AUTH_TOKEN
TWILIO_ACCOUNT_NUMBER=+15550000000
TWILIO_API_URL=http://localhost:8200
# Callback URL must use host.docker.internal so MessagePit (in Docker)
# can reach your Rails app running on the host.
TWILIO_STATUS_CALLBACK_URL=http://host.docker.internal:3000/webhooks/v1/sms

# Email
SENDGRID_API_KEY=test            # must match MP_SENDGRID_API_KEY
SENDGRID_API_URL=http://localhost:8100
# PKIX DER base64 public key matching MP_EMAIL_WEBHOOK_SIGNING_KEY
SENDGRID_WEBHOOK_PUBLIC_KEY=<base64-encoded PKIX DER public key>
```

### Action Mailer (`config/environments/development.rb`)

```ruby
config.action_mailer.delivery_method = :sendgrid_actionmailer
config.action_mailer.sendgrid_actionmailer_settings = {
  api_key:               ENV.fetch("SENDGRID_API_KEY", "test"),
  host:                  ENV.fetch("SENDGRID_API_URL", "https://api.sendgrid.com"),
  raise_delivery_errors: true
}
```

### SMS sender

Route `twilio-ruby` to MessagePit by rewriting the API base URL. When `TWILIO_API_URL` is not set or equals `https://api.twilio.com` the real Twilio API is used.

```ruby
require "twilio-ruby"

class SmsSender
  def self.call(to:, body:)
    sid      = ENV.fetch("TWILIO_ACCOUNT_SID")
    token    = ENV.fetch("TWILIO_AUTH_TOKEN")
    from     = ENV.fetch("TWILIO_ACCOUNT_NUMBER")
    callback = ENV["TWILIO_STATUS_CALLBACK_URL"]

    client = build_client(sid, token)
    params = { body: body, from: from, to: to }
    params[:status_callback] = callback if callback.present?

    message = client.messages.create(**params)
    { status: "Sent", code: 201, sid: message.sid }
  end

  def self.build_client(sid, token)
    base_url = ENV.fetch("TWILIO_API_URL", "https://api.twilio.com")
    return Twilio::REST::Client.new(sid, token) if base_url == "https://api.twilio.com"

    Twilio::REST::Client.new(sid, token, nil, nil, ProxyHttpClient.new(base_url))
  end

  # Rewrites the Twilio API host so the gem can target MessagePit.
  class ProxyHttpClient < Twilio::HTTP::Client
    def initialize(base_url)
      super()
      uri         = URI.parse(base_url)
      @proxy_host = "#{uri.scheme}://#{uri.host}"
      @proxy_base = "#{uri.scheme}://#{uri.host}:#{uri.port}"
      @proxy_port = uri.port
    end

    def request(host, port, method, url, params = {}, data = {}, headers = {}, auth = nil, timeout = nil)
      rewritten_url = url.sub(%r{\Ahttps?://[^/]+}, @proxy_base)
      super(@proxy_host, @proxy_port, method, rewritten_url, params, data, headers, auth, timeout)
    end
  end
end
```

### Email delivery tracking

Set `custom_args: { notification_id: record.id.to_s }` in your `mail()` call. MessagePit flattens every custom arg onto each event in the lifecycle so your app can update the delivery status on the corresponding record. Add `mp_scenario` to drive a bounce, deferral, or open/click instead of a plain delivery.

```ruby
mail(
  to:          recipient,
  subject:     "Your subject",
  custom_args: { notification_id: @notification.id.to_s }
)
```

### Webhook controllers

**SMS** — validate with `Twilio::Security::RequestValidator`:

```ruby
require "twilio-ruby"

class SmsWebhookController < ApplicationController
  skip_before_action :verify_authenticity_token

  before_action :verify_twilio_signature

  # Called once per state change: queued, sent, then delivered — or failed /
  # undelivered with an ErrorCode.
  def update
    notification = Notification.find_by(sms_id: params[:MessageSid])
    notification&.update_columns(
      sms_delivery_status: params[:MessageStatus],
      sms_error_code:      params[:ErrorCode]
    )
    head :ok
  end

  private

  def verify_twilio_signature
    token     = ENV["TWILIO_AUTH_TOKEN"]
    validator = Twilio::Security::RequestValidator.new(token)
    unless validator.validate(request.original_url, request.POST, request.headers["X-Twilio-Signature"].to_s)
      head :forbidden
    end
  end
end
```

**Email** — validate with ECDSA using the `SENDGRID_WEBHOOK_PUBLIC_KEY`:

```ruby
class EmailWebhookController < ApplicationController
  skip_before_action :verify_authenticity_token

  before_action :verify_sendgrid_signature

  # Every event in the lifecycle arrives here — processed, delivered, bounce,
  # dropped, deferred, open, click — so record the status rather than matching
  # on a single event type.
  TERMINAL_EVENTS = %w[delivered bounce dropped spamreport].freeze

  def update
    (params["_json"] || []).each do |event|
      next unless TERMINAL_EVENTS.include?(event["event"])
      next if event["notification_id"].blank?

      Notification.find_by(id: event["notification_id"])
                  &.update_columns(email_delivery_status: event["event"])
    end
    head :ok
  end

  private

  def verify_sendgrid_signature
    public_key = OpenSSL::PKey.read(Base64.decode64(ENV["SENDGRID_WEBHOOK_PUBLIC_KEY"]))
    signature  = Base64.decode64(request.headers["X-Twilio-Email-Event-Webhook-Signature"].to_s)
    payload    = request.headers["X-Twilio-Email-Event-Webhook-Timestamp"].to_s + request.raw_post
    head :forbidden unless public_key.verify(OpenSSL::Digest::SHA256.new, signature, payload)
  rescue OpenSSL::PKey::PKeyError, ArgumentError
    head :forbidden
  end
end
```

## License

MIT — see [LICENSE](LICENSE).

Portions of this project are derived from [Mailpit](https://github.com/axllent/mailpit) by Ralph Slooten, also MIT licensed.
