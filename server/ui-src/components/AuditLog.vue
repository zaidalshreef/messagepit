<script setup>
import { ref } from "vue";
import axios from "axios";
import { useCommon } from "../composables/useCommon";

const { resolve } = useCommon();
const events = ref([]);
const error = ref("");
const busy = ref(false);

async function load() {
	busy.value = true;
	error.value = "";
	try {
		const response = await axios.get(resolve("/api/v1/audit"), { params: { limit: 50 } });
		events.value = response.data.events;
	} catch {
		error.value = "Unable to load the audit log.";
	} finally {
		busy.value = false;
	}
}

function toggle(event) {
	if (event.target.open) load();
}
</script>

<template>
	<section class="card shadow-sm mt-4">
		<div class="card-body">
			<details @toggle="toggle">
				<summary class="h5 mb-0">Recent audit events</summary>
				<p class="small text-muted mt-3">
					Recent sign-ins, sign-outs, and management actions across this instance.
				</p>
				<div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
				<button class="btn btn-sm btn-outline-secondary mb-3" :disabled="busy" @click="load">
					Refresh audit log
				</button>
				<p v-if="!busy && !events.length" class="text-muted mb-0">No events found.</p>
				<div v-else class="table-responsive">
					<table class="table table-sm align-middle mb-0">
						<thead>
							<tr>
								<th scope="col">Time</th>
								<th scope="col">Actor</th>
								<th scope="col">Action</th>
								<th scope="col">Status</th>
							</tr>
						</thead>
						<tbody>
							<tr v-for="event in events" :key="event.id">
								<td class="text-nowrap">{{ new Date(event.created_at).toLocaleString() }}</td>
								<td class="text-break">{{ event.subject || event.service_token_id || "Unknown" }}</td>
								<td class="text-break">{{ event.action }}</td>
								<td>{{ event.status }}</td>
							</tr>
						</tbody>
					</table>
				</div>
			</details>
		</div>
	</section>
</template>
