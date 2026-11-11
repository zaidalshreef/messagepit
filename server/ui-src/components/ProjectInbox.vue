<script setup>
import { onBeforeUnmount, ref, watch } from "vue";
import axios from "axios";
import { useCommon } from "../composables/useCommon";

const props = defineProps({
	environment: { type: Object, required: true },
	canDelete: { type: Boolean, default: false },
});
const { resolve } = useCommon();
const kind = ref("messages");
const detailKind = ref("messages");
const query = ref("");
const submittedQuery = ref("");
const start = ref(0);
const items = ref([]);
const detail = ref(null);
const code = ref("");
const codeError = ref("");
const aiEnabled = ref(false);
const summary = ref("");
const summaryError = ref("");
const inspection = ref(null);
const inspectionError = ref("");
const simulation = ref(null);
const simulationError = ref("");
const total = ref(null);
const searchPages = ref({});
const expandedSearch = ref(false);
const busy = ref(false);
const error = ref("");
const live = ref(false);
const newActivity = ref(false);
let requestVersion = 0;
let socket = null;
let reconnectTimer = null;
let liveGeneration = 0;
let pendingRefresh = false;

function path(suffix = "", category = kind.value) {
	return resolve(`/api/v1/environments/${encodeURIComponent(props.environment.id)}/${category}${suffix}`);
}

async function load() {
	const version = ++requestVersion;
	newActivity.value = false;
	expandedSearch.value = false;
	searchPages.value = {};
	busy.value = true;
	error.value = "";
	detail.value = null;
	code.value = "";
	codeError.value = "";
	summary.value = "";
	summaryError.value = "";
	inspection.value = null;
	inspectionError.value = "";
	simulation.value = null;
	simulationError.value = "";
	if ((kind.value === "all" || kind.value === "runs") && !submittedQuery.value) {
		items.value = [];
		total.value = null;
		busy.value = false;
		pendingRefresh = false;
		return;
	}
	if (
		kind.value === "runs" &&
		(!/^[A-Za-z0-9._:-]{1,128}$/.test(submittedQuery.value) || [".", ".."].includes(submittedQuery.value))
	) {
		items.value = [];
		total.value = null;
		error.value = "Enter a valid test run ID (letters, digits, dots, underscores, hyphens, or colons).";
		busy.value = false;
		pendingRefresh = false;
		return;
	}
	try {
		const response =
			kind.value === "runs"
				? await axios.get(path(`/${encodeURIComponent(submittedQuery.value)}/timeline`, "correlations"), {
						params: { start: start.value, limit: 50 },
					})
				: await axios.get(path("", kind.value === "all" ? "search" : kind.value), {
						params: { query: submittedQuery.value, start: start.value, limit: 50 },
					});
		if (version !== requestVersion) return;
		if (kind.value === "runs") {
			items.value = response.data.records.map((record) => ({
				ID: record.id,
				_kind: record.kind,
				Subject: record.title,
				Snippet: record.snippet,
				Created: record.created,
			}));
			total.value = response.data.total;
		} else if (kind.value === "all") {
			const groups = [
				{ result: response.data.email, type: "messages" },
				{ result: response.data.sms, type: "sms" },
				{ result: response.data.webhooks, type: "webhooks" },
			];
			items.value = groups
				.flatMap(({ result, type }) => result.messages.map((item) => ({ ...item, _kind: type })))
				.sort((a, b) => new Date(b.Created) - new Date(a.Created));
			total.value = groups.reduce((sum, group) => sum + group.result.total, 0);
			searchPages.value = Object.fromEntries(
				groups.map(({ result, type }) => [type, { loaded: result.messages.length, total: result.total }]),
			);
		} else {
			items.value = response.data.messages;
			total.value = response.data.total ?? null;
		}
	} catch {
		if (version === requestVersion) error.value = "Unable to load captured data.";
	} finally {
		if (version === requestVersion) {
			busy.value = false;
			if (pendingRefresh) {
				pendingRefresh = false;
				if (!detail.value && start.value === 0) load();
				else newActivity.value = true;
			}
		}
	}
}

async function loadMore(category) {
	if (busy.value || !searchPages.value[category]) return;
	const version = requestVersion;
	const offset = searchPages.value[category].loaded;
	busy.value = true;
	expandedSearch.value = true;
	error.value = "";
	try {
		const response = await axios.get(path("", category), {
			params: { query: submittedQuery.value, start: offset, limit: 20 },
		});
		if (version !== requestVersion) return;
		const known = new Set(items.value.map((item) => `${item._kind}:${item.ID}`));
		const more = response.data.messages
			.filter((item) => !known.has(`${category}:${item.ID}`))
			.map((item) => ({ ...item, _kind: category }));
		items.value = [...items.value, ...more].sort((a, b) => new Date(b.Created) - new Date(a.Created));
		searchPages.value[category].loaded += response.data.messages.length;
		searchPages.value[category].total = response.data.total;
		total.value = Object.values(searchPages.value).reduce((sum, page) => sum + page.total, 0);
	} catch {
		if (version === requestVersion) error.value = "Unable to load more search results.";
	} finally {
		if (version === requestVersion) {
			busy.value = false;
			if (pendingRefresh) {
				pendingRefresh = false;
				newActivity.value = true;
			}
		}
	}
}

function refreshFromLive() {
	if (busy.value || detail.value || start.value > 0 || expandedSearch.value) newActivity.value = true;
	else load();
}

function stopLive() {
	liveGeneration++;
	clearTimeout(reconnectTimer);
	reconnectTimer = null;
	if (socket) socket.close();
	socket = null;
	live.value = false;
	pendingRefresh = false;
}

function connectLive(environmentID) {
	stopLive();
	const generation = liveGeneration;
	let retryDelay = 1000;
	function connect() {
		if (generation !== liveGeneration) return;
		const url = new URL(
			resolve(`/api/v1/environments/${encodeURIComponent(environmentID)}/events`),
			window.location.href,
		);
		url.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
		const current = new WebSocket(url);
		socket = current;
		current.onopen = () => {
			if (generation !== liveGeneration) return;
			live.value = true;
			retryDelay = 1000;
			// Recover changes that happened before the socket connected.
			if (busy.value && !detail.value && start.value === 0 && !expandedSearch.value) pendingRefresh = true;
			else refreshFromLive();
		};
		current.onmessage = (event) => {
			if (generation === liveGeneration && event.data === "changed") refreshFromLive();
		};
		current.onerror = () => current.close();
		current.onclose = () => {
			if (generation !== liveGeneration) return;
			live.value = false;
			socket = null;
			reconnectTimer = setTimeout(connect, retryDelay);
			retryDelay = Math.min(retryDelay * 2, 30000);
		};
	}
	connect();
}

async function show(item) {
	const version = ++requestVersion;
	detailKind.value = item._kind || kind.value;
	detail.value = null;
	busy.value = true;
	error.value = "";
	code.value = "";
	codeError.value = "";
	summary.value = "";
	summaryError.value = "";
	inspection.value = null;
	inspectionError.value = "";
	simulation.value = null;
	simulationError.value = "";
	try {
		const response = await axios.get(path(`/${encodeURIComponent(item.ID)}`, detailKind.value));
		if (version === requestVersion) detail.value = response.data;
	} catch {
		if (version === requestVersion) error.value = "Unable to load this record.";
	} finally {
		if (version === requestVersion) busy.value = false;
	}
}

async function inspect(operation) {
	if (!detail.value || busy.value) return;
	const version = requestVersion;
	busy.value = true;
	inspection.value = null;
	inspectionError.value = "";
	try {
		const response = await axios.get(
			path(`/${encodeURIComponent(detail.value.ID)}/${operation}`, detailKind.value),
		);
		if (version === requestVersion) inspection.value = response.data;
	} catch {
		if (version === requestVersion) inspectionError.value = "Unable to inspect this email.";
	} finally {
		if (version === requestVersion) busy.value = false;
	}
}

async function summarize() {
	if (!detail.value || busy.value) return;
	const version = requestVersion;
	busy.value = true;
	summary.value = "";
	summaryError.value = "";
	try {
		const response = await axios.post(path(`/${encodeURIComponent(detail.value.ID)}/summary`, detailKind.value));
		if (version === requestVersion) summary.value = response.data.summary;
	} catch {
		if (version === requestVersion) summaryError.value = "Unable to contact the configured AI service.";
	} finally {
		if (version === requestVersion) busy.value = false;
	}
}

async function simulate() {
	if (!detail.value || busy.value) return;
	const version = requestVersion;
	busy.value = true;
	simulation.value = null;
	simulationError.value = "";
	try {
		const response = await axios.get(path(`/${encodeURIComponent(detail.value.ID)}/simulation`, detailKind.value));
		if (version === requestVersion) simulation.value = response.data.steps;
	} catch {
		if (version === requestVersion) simulationError.value = "Unable to load the delivery scenario.";
	} finally {
		if (version === requestVersion) busy.value = false;
	}
}

async function remove() {
	if (!detail.value || busy.value || !window.confirm("Delete this captured record?")) return;
	const version = requestVersion;
	busy.value = true;
	error.value = "";
	try {
		await axios.delete(path(`/${encodeURIComponent(detail.value.ID)}`, detailKind.value));
		if (version === requestVersion) await load();
	} catch {
		if (version === requestVersion) error.value = "Unable to delete this record.";
	} finally {
		if (version === requestVersion) busy.value = false;
	}
}

async function findCode() {
	if (!detail.value || busy.value) return;
	busy.value = true;
	code.value = "";
	codeError.value = "";
	const version = requestVersion;
	try {
		const response = await axios.get(path(`/${encodeURIComponent(detail.value.ID)}/otp`, detailKind.value));
		if (version === requestVersion) code.value = response.data.code;
	} catch (err) {
		if (version === requestVersion) {
			codeError.value =
				err.response?.status === 409
					? "More than one verification code was found."
					: "No labeled verification code was found.";
		}
	} finally {
		if (version === requestVersion) busy.value = false;
	}
}

function selectKind(value) {
	kind.value = value;
	detailKind.value = value;
	submittedQuery.value = query.value.trim();
	start.value = 0;
	load();
}

function search() {
	submittedQuery.value = query.value.trim();
	start.value = 0;
	load();
}

function page(change) {
	start.value = Math.max(0, start.value + change);
	load();
}

watch(
	() => props.environment.id,
	() => {
		query.value = "";
		submittedQuery.value = "";
		start.value = 0;
		items.value = [];
		aiEnabled.value = false;
		const environmentID = props.environment.id;
		axios
			.get(resolve(`/api/v1/environments/${encodeURIComponent(environmentID)}/ai`))
			.then((response) => {
				if (props.environment.id === environmentID) aiEnabled.value = response.data.enabled;
			})
			.catch(() => {});
		load();
		connectLive(environmentID);
	},
	{ immediate: true },
);
onBeforeUnmount(() => {
	requestVersion++;
	stopLive();
});
</script>

<template>
	<section class="card shadow-sm mt-4" aria-labelledby="inbox-title">
		<div class="card-body">
			<div class="d-flex flex-wrap justify-content-between align-items-center gap-2 mb-3">
				<div>
					<h2 id="inbox-title" class="h5 mb-1">Captured data</h2>
					<p class="small text-muted mb-0">
						Environment: {{ environment.name }} · {{ live ? "Live" : "Connecting" }}
					</p>
				</div>
				<button class="btn btn-sm btn-outline-secondary" :disabled="busy" @click="load">
					{{ newActivity ? "New activity · Refresh" : "Refresh" }}
				</button>
			</div>
			<div class="nav nav-pills gap-1 mb-3" aria-label="Message type">
				<button
					v-for="tab in [
						{ id: 'all', label: 'All' },
						{ id: 'runs', label: 'Test run' },
						{ id: 'messages', label: 'Email' },
						{ id: 'sms', label: 'SMS' },
						{ id: 'webhooks', label: 'Webhooks' },
					]"
					:key="tab.id"
					class="nav-link"
					:class="{ active: kind === tab.id }"
					:aria-pressed="kind === tab.id"
					@click="selectKind(tab.id)"
				>
					{{ tab.label }}
				</button>
			</div>
			<form class="d-flex gap-2 mb-3" @submit.prevent="search">
				<label class="visually-hidden" for="project-search">Search captured data</label>
				<input
					id="project-search"
					v-model="query"
					class="form-control"
					:maxlength="kind === 'runs' ? 128 : 512"
					:placeholder="kind === 'runs' ? 'Test run correlation ID' : 'Search this environment'"
				/>
				<button class="btn btn-primary" :disabled="busy">Search</button>
			</form>
			<p v-if="kind === 'messages'" class="small text-muted">
				Email search supports filters such as <code>from:</code>, <code>subject:</code>, and
				<code>has:attachment</code>.
			</p>
			<p v-if="kind === 'runs'" class="small text-muted">
				Find all channels using the same <code>X-MessagePit-Correlation-ID</code>.
			</p>
			<div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
			<div class="row g-3">
				<div class="col-lg-5">
					<p v-if="!busy && !items.length" class="text-muted">
						{{
							kind === "runs" && !submittedQuery
								? "Enter a test run ID to see its captures."
								: kind === "all" && !submittedQuery
									? "Enter a search term to search all channels."
									: "No matching records."
						}}
					</p>
					<div v-else class="list-group">
						<button
							v-for="item in items"
							:key="`${item._kind || kind}:${item.ID}`"
							class="list-group-item list-group-item-action text-start"
							:class="{ active: detail?.ID === item.ID }"
							@click="show(item)"
						>
							<strong class="d-block text-break">{{
								item.Subject ||
								(item.Method
									? `${item.Method} ${item.Path}`
									: typeof item.From === "object"
										? item.From?.Address
										: item.From)
							}}</strong>
							<span v-if="kind === 'all' || kind === 'runs'" class="d-block small text-muted">{{
								item._kind === "messages" ? "Email" : item._kind === "sms" ? "SMS" : "Webhook"
							}}</span>
							<span class="small text-break">{{ item.Snippet || item.Body }}</span>
							<span class="d-block small opacity-75">{{ new Date(item.Created).toLocaleString() }}</span>
						</button>
					</div>
					<div v-if="kind !== 'all'" class="d-flex align-items-center gap-2 mt-3">
						<button
							class="btn btn-sm btn-outline-secondary"
							:disabled="busy || start === 0"
							@click="page(-50)"
						>
							Previous
						</button>
						<button
							class="btn btn-sm btn-outline-secondary"
							:disabled="busy || items.length < 50 || (total !== null && start + 50 >= total)"
							@click="page(50)"
						>
							Next
						</button>
						<span v-if="total !== null" class="small text-muted">{{ total }} results</span>
					</div>
					<div v-else-if="total !== null" class="mt-3">
						<p class="small text-muted mb-2">Showing {{ items.length }} of {{ total }} results.</p>
						<template
							v-for="category in [
								{ id: 'messages', label: 'email' },
								{ id: 'sms', label: 'SMS' },
								{ id: 'webhooks', label: 'webhooks' },
							]"
							:key="category.id"
						>
							<button
								v-if="searchPages[category.id]?.loaded < searchPages[category.id]?.total"
								class="btn btn-sm btn-outline-secondary me-2 mb-2"
								:disabled="busy"
								@click="loadMore(category.id)"
							>
								Load more {{ category.label }} ({{
									searchPages[category.id].total - searchPages[category.id].loaded
								}}
								left)
							</button>
						</template>
					</div>
				</div>
				<div class="col-lg-7">
					<div v-if="detail" class="border rounded p-3 h-100 overflow-auto">
						<template v-if="detailKind === 'messages'">
							<h3 class="h6 text-break">{{ detail.Subject || "(No subject)" }}</h3>
							<p class="small text-muted text-break">From: {{ detail.From?.Address || "Unknown" }}</p>
							<pre class="text-wrap text-break">{{ detail.Text || detail.HTML || "(No body)" }}</pre>
							<a
								:href="path(`/${encodeURIComponent(detail.ID)}/raw`, detailKind)"
								class="btn btn-sm btn-outline-primary"
								>Download raw email</a
							>
							<button
								class="btn btn-sm btn-outline-secondary ms-2"
								:disabled="busy"
								@click="inspect('headers')"
							>
								View headers
							</button>
							<button
								v-if="detail.HTML"
								class="btn btn-sm btn-outline-secondary ms-2"
								:disabled="busy"
								@click="inspect('html-check')"
							>
								Check HTML
							</button>
							<pre v-if="inspection" class="text-wrap text-break mt-3">{{
								JSON.stringify(inspection, null, 2)
							}}</pre>
							<p v-if="inspectionError" class="small text-danger mt-2" role="status">
								{{ inspectionError }}
							</p>
							<ul v-if="detail.Attachments?.length" class="mt-3 mb-0">
								<li v-for="attachment in detail.Attachments" :key="attachment.PartID">
									<a
										:href="
											path(
												`/${encodeURIComponent(detail.ID)}/part/${encodeURIComponent(attachment.PartID)}`,
												detailKind,
											)
										"
										>{{ attachment.FileName || "Attachment" }}</a
									>
								</li>
							</ul>
						</template>
						<template v-else-if="detailKind === 'sms'">
							<h3 class="h6">{{ detail.From }} → {{ detail.To }}</h3>
							<pre class="text-wrap text-break mb-0">{{ detail.Body }}</pre>
						</template>
						<template v-else>
							<h3 class="h6 text-break">{{ detail.Method }} {{ detail.Path }}</h3>
							<p class="small text-muted text-break">{{ detail.Query }}</p>
							<pre class="text-wrap text-break">{{ JSON.stringify(detail.Headers, null, 2) }}</pre>
							<pre class="text-wrap text-break mb-0">{{ detail.Body }}</pre>
						</template>
						<div v-if="detailKind !== 'webhooks'" class="mt-3">
							<button class="btn btn-sm btn-outline-secondary" :disabled="busy" @click="findCode">
								Find verification code
							</button>
							<p v-if="code" class="mt-2 mb-0" role="status">
								Verification code: <code>{{ code }}</code>
							</p>
							<p v-if="codeError" class="small text-muted mt-2 mb-0" role="status">{{ codeError }}</p>
							<button class="btn btn-sm btn-outline-secondary ms-2" :disabled="busy" @click="simulate">
								Show delivery scenario
							</button>
							<div v-if="simulation" class="small mt-2" role="status">
								<p class="text-muted mb-1">Simulated sequence; no callback is sent.</p>
								<ol class="mb-0">
									<li v-for="(step, index) in simulation" :key="index">
										{{ step.status
										}}<span v-if="step.error_code">
											· {{ step.error_code }} {{ step.error_message }}</span
										>
									</li>
								</ol>
							</div>
							<p v-if="simulationError" class="small text-danger mt-2 mb-0" role="status">
								{{ simulationError }}
							</p>
						</div>
						<div v-if="aiEnabled" class="mt-3 border-top pt-3">
							<p class="small text-muted">
								This sends the selected record's content to the configured AI service. Summaries may be
								inaccurate.
							</p>
							<button class="btn btn-sm btn-outline-primary" :disabled="busy" @click="summarize">
								Summarize with AI
							</button>
							<p v-if="summary" class="text-break text-wrap mt-2 mb-0" role="status">{{ summary }}</p>
							<p v-if="summaryError" class="small text-danger mt-2 mb-0" role="status">
								{{ summaryError }}
							</p>
						</div>
						<button
							v-if="canDelete"
							class="btn btn-sm btn-outline-danger mt-3"
							:disabled="busy"
							@click="remove"
						>
							Delete record
						</button>
					</div>
					<p v-else class="text-muted">Select a record to inspect it.</p>
				</div>
			</div>
		</div>
	</section>
</template>
