<script setup>
import { ref, watch } from "vue";
import axios from "axios";
import { useCommon } from "../composables/useCommon";

const props = defineProps({ environment: { type: Object, required: true } });
const { resolve } = useCommon();
const tokens = ref([]);
const name = ref("");
const role = ref("developer");
const minutes = ref(60);
const secret = ref("");
const issuedID = ref("");
const error = ref("");
const busy = ref(false);
let requestVersion = 0;

function path(suffix = "") {
	return resolve(`/api/v1/environments/${encodeURIComponent(props.environment.id)}/tokens${suffix}`);
}

async function load() {
	const version = ++requestVersion;
	try {
		const response = await axios.get(path());
		if (version === requestVersion) tokens.value = response.data.tokens;
	} catch {
		if (version === requestVersion) error.value = "Unable to load credentials.";
	}
}

async function issue() {
	if (!name.value.trim() || busy.value) return;
	busy.value = true;
	error.value = "";
	secret.value = "";
	const environmentID = props.environment.id;
	try {
		const response = await axios.post(path(), {
			name: name.value.trim(),
			role: role.value,
			ttl_seconds: Number(minutes.value) * 60,
		});
		if (environmentID !== props.environment.id) return;
		secret.value = response.data.secret;
		issuedID.value = response.data.token.id;
		name.value = "";
		await load();
	} catch {
		if (environmentID === props.environment.id)
			error.value = "Unable to issue the credential. Check the name and lifetime.";
	} finally {
		busy.value = false;
	}
}

async function revoke(token) {
	if (busy.value || !window.confirm(`Revoke the credential “${token.name}”?`)) return;
	busy.value = true;
	error.value = "";
	try {
		await axios.delete(path(`/${encodeURIComponent(token.id)}`));
		if (issuedID.value === token.id) secret.value = "";
		await load();
	} catch {
		error.value = "Unable to revoke the credential.";
	} finally {
		busy.value = false;
	}
}

watch(
	() => props.environment.id,
	() => {
		secret.value = "";
		issuedID.value = "";
		error.value = "";
		tokens.value = [];
		load();
	},
	{ immediate: true },
);
</script>

<template>
	<section class="card shadow-sm mt-4" aria-labelledby="credentials-title">
		<div class="card-body">
			<h2 id="credentials-title" class="h5">Service credentials</h2>
			<p class="text-muted small">Credentials are limited to this environment and expire within 24 hours.</p>
			<div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
			<form class="row g-2 align-items-end mb-3" @submit.prevent="issue">
				<div class="col-md-4">
					<label for="token-name" class="form-label">Name</label
					><input id="token-name" v-model="name" class="form-control" maxlength="100" required />
				</div>
				<div class="col-md-3">
					<label for="token-role" class="form-label">Role</label
					><select id="token-role" v-model="role" class="form-select">
						<option value="developer">Developer</option>
						<option value="viewer">Viewer</option>
					</select>
				</div>
				<div class="col-md-3">
					<label for="token-minutes" class="form-label">Minutes</label
					><input
						id="token-minutes"
						v-model="minutes"
						class="form-control"
						type="number"
						min="1"
						max="1440"
						required
					/>
				</div>
				<div class="col-md-2"><button class="btn btn-primary w-100" :disabled="busy">Issue</button></div>
			</form>
			<div v-if="secret" class="alert alert-warning" role="status">
				<strong>Copy this credential now.</strong> It will not be shown again.
				<label for="token-secret" class="visually-hidden">New service credential</label>
				<textarea
					id="token-secret"
					:value="secret"
					class="form-control font-monospace mt-2"
					rows="2"
					readonly
					@focus="$event.target.select()"
				></textarea>
				<span class="small"
					>For SMTP, use <code>mpt_{{ issuedID }}</code> as the username and this credential as the
					password.</span
				>
			</div>
			<p v-if="!tokens.length" class="text-muted mb-0">No credentials for this environment.</p>
			<ul v-else class="list-group list-group-flush">
				<li
					v-for="token in tokens"
					:key="token.id"
					class="list-group-item d-flex flex-wrap justify-content-between align-items-center gap-2 px-0"
				>
					<span
						><strong>{{ token.name }}</strong
						><span class="small text-muted">
							· {{ token.role }} · expires {{ new Date(token.expires_at).toLocaleString()
							}}{{ token.revoked ? " · revoked" : "" }}</span
						></span
					>
					<button
						v-if="!token.revoked"
						class="btn btn-sm btn-outline-danger"
						:disabled="busy"
						@click="revoke(token)"
					>
						Revoke
					</button>
				</li>
			</ul>
		</div>
	</section>
</template>
