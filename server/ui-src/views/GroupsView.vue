<script setup>
import { onMounted, ref } from "vue";
import axios from "axios";
import AppLayout from "../components/AppLayout.vue";
import LocalAccounts from "../components/LocalAccounts.vue";
import { useCommon } from "../composables/useCommon";

const { resolve } = useCommon();
const groups = ref([]);
const members = ref([]);
const selected = ref(null);
const currentUser = ref(null);
const groupName = ref("");
const subject = ref("");
const error = ref("");
const busy = ref(false);

function showError(err) {
	const message = err.response?.data;
	error.value = typeof message === "string" ? message.trim() : "Unable to update groups. Please try again.";
}

async function loadGroups() {
	const response = await axios.get(resolve("/api/v1/groups"));
	groups.value = response.data.groups;
}

async function selectGroup(group) {
	selected.value = group;
	members.value = [];
	error.value = "";
	try {
		const response = await axios.get(resolve(`/api/v1/groups/${group.id}/members`));
		if (selected.value?.id === group.id) members.value = response.data.members;
	} catch (err) {
		showError(err);
	}
}

async function createGroup() {
	if (!groupName.value.trim() || busy.value) return;
	busy.value = true;
	error.value = "";
	try {
		const response = await axios.post(resolve("/api/v1/groups"), { name: groupName.value.trim() });
		groupName.value = "";
		await loadGroups();
		await selectGroup(response.data);
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

async function deleteGroup() {
	if (!selected.value || busy.value || !window.confirm(`Delete the group “${selected.value.name}”?`)) return;
	busy.value = true;
	error.value = "";
	try {
		await axios.delete(resolve(`/api/v1/groups/${selected.value.id}`));
		selected.value = null;
		members.value = [];
		await loadGroups();
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

async function addMember() {
	if (!selected.value || !subject.value.trim() || busy.value) return;
	busy.value = true;
	error.value = "";
	try {
		await axios.post(resolve(`/api/v1/groups/${selected.value.id}/members`), { subject: subject.value.trim() });
		subject.value = "";
		await selectGroup(selected.value);
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

async function removeMember(member) {
	if (!selected.value || busy.value) return;
	busy.value = true;
	error.value = "";
	try {
		await axios.delete(resolve(`/api/v1/groups/${selected.value.id}/members`), {
			data: { issuer: member.issuer, subject: member.subject },
		});
		await selectGroup(selected.value);
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

onMounted(async () => {
	busy.value = true;
	try {
		const response = await axios.get(resolve("/api/v1/me"));
		currentUser.value = response.data;
		await loadGroups();
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
});
</script>

<template>
	<AppLayout
		active-tab="groups"
		offcanvas-id="groupsOffcanvas"
		offcanvas-title="Groups"
		:show-sidebar="false"
		:loading="busy ? 1 : 0"
	>
		<div class="container py-4 overflow-y-auto">
			<div class="d-flex flex-wrap align-items-center justify-content-between gap-2 mb-3">
				<div>
					<h1 class="h3 mb-1">Groups</h1>
					<p class="text-muted mb-0">Project access is granted separately.</p>
				</div>
			</div>
			<div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
			<LocalAccounts v-if="currentUser?.issuer === 'messagepit:local'" :admin="currentUser.admin" />
			<form v-if="currentUser?.admin" class="d-flex gap-2 mb-4" @submit.prevent="createGroup">
				<label class="visually-hidden" for="group-name">Group name</label>
				<input
					id="group-name"
					v-model="groupName"
					class="form-control"
					placeholder="Group name"
					maxlength="100"
				/>
				<button class="btn btn-primary text-nowrap" :disabled="busy || !groupName.trim()">Create group</button>
			</form>
			<div class="row g-4">
				<div class="col-md-4">
					<p v-if="!groups.length" class="text-muted">No groups available.</p>
					<div v-else class="list-group">
						<button
							v-for="group in groups"
							:key="group.id"
							class="list-group-item list-group-item-action"
							:class="{ active: selected?.id === group.id }"
							@click="selectGroup(group)"
						>
							{{ group.name }}
						</button>
					</div>
				</div>
				<div class="col-md-8">
					<div v-if="selected" class="card">
						<div class="card-body">
							<div class="d-flex justify-content-between align-items-center gap-2 mb-3">
								<h2 class="h5 mb-0">{{ selected.name }}</h2>
								<button
									v-if="currentUser?.admin"
									class="btn btn-sm btn-outline-danger"
									:disabled="busy"
									@click="deleteGroup"
								>
									Delete group
								</button>
							</div>
							<form v-if="currentUser?.admin" class="d-flex gap-2 mb-3" @submit.prevent="addMember">
								<label class="visually-hidden" for="member-subject"
									>Username or member subject ID</label
								>
								<input
									id="member-subject"
									v-model="subject"
									class="form-control"
									:placeholder="
										currentUser?.issuer === 'messagepit:local' ? 'Username' : 'Member subject ID'
									"
									maxlength="255"
								/>
								<button class="btn btn-outline-primary text-nowrap" :disabled="busy || !subject.trim()">
									Add member
								</button>
							</form>
							<p v-if="!members.length" class="text-muted mb-0">No members in this group.</p>
							<ul v-else class="list-group list-group-flush">
								<li
									v-for="member in members"
									:key="member.issuer + member.subject"
									class="list-group-item d-flex justify-content-between align-items-center gap-2"
								>
									<span class="text-break">{{ member.subject }}</span>
									<button
										v-if="currentUser?.admin"
										class="btn btn-sm btn-outline-danger"
										:disabled="busy"
										:aria-label="`Remove ${member.subject}`"
										@click="removeMember(member)"
									>
										Remove
									</button>
								</li>
							</ul>
						</div>
					</div>
					<p v-else class="text-muted">Select a group to see its members.</p>
				</div>
			</div>
		</div>
	</AppLayout>
</template>
