#!/usr/bin/env bash
# Assert that a correlated workflow produced the requested captured messages.
set -euo pipefail
set +x

: "${MESSAGEPIT_URL:?Set MESSAGEPIT_URL}"
: "${MESSAGEPIT_ENVIRONMENT_ID:?Set MESSAGEPIT_ENVIRONMENT_ID}"
: "${MESSAGEPIT_TOKEN:?Set MESSAGEPIT_TOKEN}"
: "${MESSAGEPIT_CORRELATION_ID:?Set MESSAGEPIT_CORRELATION_ID}"

expected_email=${MESSAGEPIT_EXPECT_EMAILS:-1}
expected_sms=${MESSAGEPIT_EXPECT_SMS:-0}
expected_webhooks=${MESSAGEPIT_EXPECT_WEBHOOKS:-0}
no_emails=${MESSAGEPIT_EXPECT_NO_EMAILS:-0}
no_sms=${MESSAGEPIT_EXPECT_NO_SMS:-0}
no_webhooks=${MESSAGEPIT_EXPECT_NO_WEBHOOKS:-0}
max_wait=${MESSAGEPIT_WAIT_SECONDS:-30}

# Each filter is optional. All filters for a channel must match the same record.
export MESSAGEPIT_EXPECT_EMAIL_SUBJECT=${MESSAGEPIT_EXPECT_EMAIL_SUBJECT:-}
export MESSAGEPIT_EXPECT_EMAIL_BODY=${MESSAGEPIT_EXPECT_EMAIL_BODY:-}
export MESSAGEPIT_EXPECT_EMAIL_TO=${MESSAGEPIT_EXPECT_EMAIL_TO:-}
export MESSAGEPIT_EXPECT_SMS_BODY=${MESSAGEPIT_EXPECT_SMS_BODY:-}
export MESSAGEPIT_EXPECT_SMS_TO=${MESSAGEPIT_EXPECT_SMS_TO:-}
export MESSAGEPIT_EXPECT_WEBHOOK_METHOD=${MESSAGEPIT_EXPECT_WEBHOOK_METHOD:-}
export MESSAGEPIT_EXPECT_WEBHOOK_PATH=${MESSAGEPIT_EXPECT_WEBHOOK_PATH:-}
export MESSAGEPIT_EXPECT_WEBHOOK_BODY=${MESSAGEPIT_EXPECT_WEBHOOK_BODY:-}

for value in "$expected_email" "$expected_sms" "$expected_webhooks" "$max_wait"; do
	if ! [[ $value =~ ^(0|[1-9][0-9]{0,2})$ ]]; then
		echo "Expected counts and wait time must be whole numbers." >&2
		exit 2
	fi
done
for value in "$no_emails" "$no_sms" "$no_webhooks"; do
	if [[ $value != 0 && $value != 1 ]]; then
		echo "Absence expectations must be 0 or 1." >&2
		exit 2
	fi
done
# The normal default is one email. An explicit absence expectation overrides
# that default, but never overrides a caller's explicit positive expectation.
if [[ $no_emails == 1 && -z ${MESSAGEPIT_EXPECT_EMAILS:-} ]]; then expected_email=0; fi
for value in "$MESSAGEPIT_EXPECT_EMAIL_SUBJECT" "$MESSAGEPIT_EXPECT_EMAIL_BODY" "$MESSAGEPIT_EXPECT_EMAIL_TO" \
	"$MESSAGEPIT_EXPECT_SMS_BODY" "$MESSAGEPIT_EXPECT_SMS_TO" "$MESSAGEPIT_EXPECT_WEBHOOK_METHOD" \
	"$MESSAGEPIT_EXPECT_WEBHOOK_PATH" "$MESSAGEPIT_EXPECT_WEBHOOK_BODY"; do
	if (( ${#value} > 1024 )); then
		echo "Each content expectation must be at most 1024 characters." >&2
		exit 2
	fi
done
email_filtered=false
sms_filtered=false
webhook_filtered=false
if [[ -n $MESSAGEPIT_EXPECT_EMAIL_SUBJECT$MESSAGEPIT_EXPECT_EMAIL_BODY$MESSAGEPIT_EXPECT_EMAIL_TO ]]; then
	email_filtered=true
	if (( expected_email == 0 )); then expected_email=1; fi
fi
if [[ -n $MESSAGEPIT_EXPECT_SMS_BODY$MESSAGEPIT_EXPECT_SMS_TO ]]; then
	sms_filtered=true
	if (( expected_sms == 0 )); then expected_sms=1; fi
fi
if [[ -n $MESSAGEPIT_EXPECT_WEBHOOK_METHOD$MESSAGEPIT_EXPECT_WEBHOOK_PATH$MESSAGEPIT_EXPECT_WEBHOOK_BODY ]]; then
	webhook_filtered=true
	if (( expected_webhooks == 0 )); then expected_webhooks=1; fi
fi
if (( (no_emails == 1 && expected_email > 0) || (no_sms == 1 && expected_sms > 0) || (no_webhooks == 1 && expected_webhooks > 0) )); then
	echo "Cannot require and forbid captures in the same channel." >&2
	exit 2
fi
forbidden_total=$((no_emails + no_sms + no_webhooks))
if (( expected_email + expected_sms + expected_webhooks > 100 || max_wait > 300 )); then
	echo "Expected counts must total at most 100 and wait time must be at most 300 seconds." >&2
	exit 2
fi
if ! [[ $MESSAGEPIT_ENVIRONMENT_ID =~ ^[A-Za-z0-9_-]{1,128}$ ]] ||
	! [[ $MESSAGEPIT_CORRELATION_ID =~ ^[A-Za-z0-9._:-]{1,128}$ ]] ||
	[[ $MESSAGEPIT_CORRELATION_ID == "." || $MESSAGEPIT_CORRELATION_ID == ".." ]] ||
	! [[ $MESSAGEPIT_TOKEN =~ ^mpt_[A-Za-z0-9]+\.[A-Za-z0-9_-]{43}$ ]]; then
	echo "Invalid environment ID, correlation ID, or service credential." >&2
	exit 2
fi
# Do not send a bearer credential to an unencrypted remote endpoint. Reject
# userinfo, fragments and queries so the host cannot be confused with a path.
https_url='^https://(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9._-]+)(:[0-9]{1,5})?(/[^[:space:]?#]*)?$'
loopback_url='^http://(localhost|127[.]0[.]0[.]1|\[::1\])(:[0-9]{1,5})?(/[^[:space:]?#]*)?$'
if ! [[ $MESSAGEPIT_URL =~ $https_url || $MESSAGEPIT_URL =~ $loopback_url ]]; then
	echo "MESSAGEPIT_URL must use HTTPS or direct loopback HTTP and must not contain userinfo, a query, or a fragment." >&2
	exit 2
fi
if ! command -v curl >/dev/null || ! command -v jq >/dev/null; then
	echo "curl and jq are required." >&2
	exit 2
fi

base="${MESSAGEPIT_URL%/}/api/v1/environments/${MESSAGEPIT_ENVIRONMENT_ID}"
url="${base}/correlations/${MESSAGEPIT_CORRELATION_ID}"
SECONDS=0
# Allow at most ten extra seconds for the final set of network requests.
request_deadline=$((max_wait + 10))
curl_proxy_options=()
if [[ $MESSAGEPIT_URL =~ $loopback_url ]]; then
	curl_proxy_options+=(--noproxy '*')
	if [[ $MESSAGEPIT_URL =~ ^http://localhost(:([0-9]{1,5}))?(/|$) ]]; then
		curl_proxy_options+=(--resolve "localhost:${BASH_REMATCH[2]:-80}:127.0.0.1")
	fi
fi

# Pass the token through stdin so it does not appear in curl's process arguments.
# Responses are never printed, including on failure.
fetch() {
	local response remaining timeout
	remaining=$((request_deadline - SECONDS))
	if (( remaining <= 0 )); then return 1; fi
	timeout=10
	if (( remaining < timeout )); then timeout=$remaining; fi
	if ! response=$(printf 'header = "Authorization: Bearer %s"\n' "$MESSAGEPIT_TOKEN" |
		curl -q --config - --proto '=http,https' --silent --show-error --max-time "$timeout" \
			"${curl_proxy_options[@]}" --max-filesize 67108864 --write-out $'\n%{http_code}' --url "$1"); then
		return 1
	fi
	fetch_status=${response##*$'\n'}
	fetch_body=${response%$'\n'*}
}

# Return 0 on a match, 1 when more captures are needed, and 2 on a request error.
match_channel() {
	local kind=$1 minimum=$2 filtered=$3 filter=$4 id matched=0 raw_count jq_status
	raw_count=$(jq -r --arg kind "$kind" '[.records[] | select(.kind == $kind)] | length' <<<"$index_body")
	if (( raw_count < minimum )); then return 1; fi
	if (( minimum == 0 )) || [[ $filtered == false ]]; then return 0; fi
	while IFS= read -r id; do
		if ! fetch "$base/$kind/$id"; then return 2; fi
		case $fetch_status in
			200)
				if jq -e "$filter" <<<"$fetch_body" >/dev/null 2>&1; then
					((matched += 1))
					if (( matched >= minimum )); then return 0; fi
				else
					jq_status=$?
					if (( jq_status != 1 )); then return 2; fi
				fi
				;;
			404) continue ;; # A concurrent deletion or retention cleanup can remove a record.
			*) return 2 ;;
		esac
	done < <(jq -r --arg kind "$kind" '.records[] | select(.kind == $kind) | .id' <<<"$index_body")
	return 1
}

# jq expands its own variables; Bash must leave them unchanged.
# shellcheck disable=SC2016
email_filter='
	(env.MESSAGEPIT_EXPECT_EMAIL_SUBJECT // "") as $subject |
	(env.MESSAGEPIT_EXPECT_EMAIL_BODY // "") as $body |
	(env.MESSAGEPIT_EXPECT_EMAIL_TO // "" | ascii_downcase) as $to |
	($subject == "" or ((.Subject // "") | contains($subject))) and
	($body == "" or ([.Text // "", .HTML // ""] | any(.[]; contains($body)))) and
	($to == "" or ((.To // []) | any(.[]; ((.Address // "") | ascii_downcase) == $to)))
'
# shellcheck disable=SC2016
sms_filter='
	(env.MESSAGEPIT_EXPECT_SMS_BODY // "") as $body |
	(env.MESSAGEPIT_EXPECT_SMS_TO // "") as $to |
	($body == "" or ((.Body // "") | contains($body))) and
	($to == "" or (.To // "") == $to)
'
# shellcheck disable=SC2016
webhook_filter='
	(env.MESSAGEPIT_EXPECT_WEBHOOK_METHOD // "" | ascii_upcase) as $method |
	(env.MESSAGEPIT_EXPECT_WEBHOOK_PATH // "") as $path |
	(env.MESSAGEPIT_EXPECT_WEBHOOK_BODY // "") as $body |
	($method == "" or ((.Method // "") | ascii_upcase) == $method) and
	($path == "" or (.Path // "") == $path) and
	($body == "" or ((.Body // "") | contains($body)))
'

while true; do
	# For an absence check, take the final snapshot only after the full window.
	window_ended=false
	if (( SECONDS >= max_wait )); then window_ended=true; fi
	if ! fetch "$url" || [[ $fetch_status != 200 ]]; then
		echo "Unable to query the MessagePit environment." >&2
		exit 1
	fi
	index_body=$fetch_body
	if ! jq -e '
		.records | type == "array" and length <= 100 and
		all(.[]; type == "object" and
			(.kind == "messages" or .kind == "sms" or .kind == "webhooks") and
			(.id | type == "string" and test("^[A-Za-z0-9_-]{1,128}$")))
	' <<<"$index_body" >/dev/null 2>&1; then
		echo "Invalid MessagePit correlation response." >&2
		exit 1
	fi
	if (( forbidden_total > 0 )); then
		unexpected=$(jq -r --argjson emails "$no_emails" --argjson sms "$no_sms" --argjson webhooks "$no_webhooks" '
			[.records[] | select((.kind == "messages" and $emails == 1) or
				(.kind == "sms" and $sms == 1) or (.kind == "webhooks" and $webhooks == 1)) |
				if .kind == "messages" then "email" else .kind end] | unique | join(" ")
		' <<<"$index_body")
		if [[ -n $unexpected ]]; then
			echo "Unexpected captures in forbidden channels: $unexpected" >&2
			exit 1
		fi
		# The correlation API returns no more than 100 records. At the limit it
		# cannot establish absence because older records may be hidden.
		if jq -e '.records | length >= 100' <<<"$index_body" >/dev/null; then
			echo "Cannot verify absence: the 100-record correlation limit was reached." >&2
			exit 1
		fi
	fi
	all_matched=true
	unmatched_channels=()
	for kind in messages sms webhooks; do
		case $kind in
			messages) label=email; minimum=$expected_email; filtered=$email_filtered; filter=$email_filter ;;
			sms) label=sms; minimum=$expected_sms; filtered=$sms_filtered; filter=$sms_filter ;;
			webhooks) label=webhooks; minimum=$expected_webhooks; filtered=$webhook_filtered; filter=$webhook_filter ;;
		esac
		if match_channel "$kind" "$minimum" "$filtered" "$filter"; then
			:
		else
			result=$?
			if (( result != 1 )); then
				echo "Unable to inspect a MessagePit capture." >&2
				exit 1
			fi
			all_matched=false
			unmatched_channels+=("$label")
		fi
	done
	if [[ $all_matched == true ]] && { (( forbidden_total == 0 )) || [[ $window_ended == true ]]; }; then
		echo "MessagePit capture assertions passed."
		exit 0
	fi
	if [[ $window_ended == true ]]; then
		echo "Timed out waiting for the expected MessagePit captures." >&2
		echo "Unmatched channels: ${unmatched_channels[*]}" >&2
		exit 1
	fi
	sleep 1
done
