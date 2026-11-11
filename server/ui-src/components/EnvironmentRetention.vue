<script setup>
import { ref, watch } from "vue";
import axios from "axios";
import { useCommon } from "../composables/useCommon";

const props = defineProps({ environment: { type: Object, required: true } });
const { resolve } = useCommon();
const hours = ref(0);
const busy = ref(false);
const message = ref("");
const error = ref("");

function path() {
	return resolve(`/api/v1/environments/${encodeURIComponent(props.environment.id)}/retention`);
}

async function load() {
	const environmentID = props.environment.id;
	error.value = "";
	message.value = "";
	try {
		const response = await axios.get(path());
		if (props.environment.id === environmentID) hours.value = response.data.max_age_seconds / 3600;
	} catch {
		if (props.environment.id === environmentID) error.value = "Unable to load retention settings.";
	}
}

async function save() {
	if (busy.value) return;
	const environmentID = props.environment.id;
	busy.value = true;
	message.value = "";
	error.value = "";
	try {
		await axios.put(path(), { max_age_seconds: Math.round(Number(hours.value) * 3600) });
		if (props.environment.id === environmentID) message.value = "Retention saved.";
	} catch {
		if (props.environment.id === environmentID) error.value = "Enter 0 to disable or an age from 1 to 8,760 hours.";
	} finally {
		busy.value = false;
	}
}

watch(() => props.environment.id, load, { immediate: true });
</script>

<template>
	<section class="card shadow-sm mt-4" aria-labelledby="retention-title">
		<div class="card-body">
			<h2 id="retention-title" class="h5">Automatic cleanup</h2>
			<p class="small text-muted">
				Set 0 to disable cleanup, or choose an age from 1 hour to 365 days. Older email, SMS, and webhooks in
				this environment may be deleted within a minute.
			</p>
			<form class="d-flex flex-wrap align-items-end gap-2" @submit.prevent="save">
				<div>
					<label for="retention-hours" class="form-label">Keep records for (hours)</label
					><input
						id="retention-hours"
						v-model="hours"
						class="form-control"
						type="number"
						min="0"
						max="8760"
						step="any"
						required
					/>
				</div>
				<button class="btn btn-outline-primary" :disabled="busy">Save retention</button>
			</form>
			<p v-if="message" class="small text-success mt-2 mb-0" role="status">{{ message }}</p>
			<p v-if="error" class="small text-danger mt-2 mb-0" role="alert">{{ error }}</p>
		</div>
	</section>
</template>
