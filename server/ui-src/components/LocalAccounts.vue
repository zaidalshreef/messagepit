<script setup>
import { onMounted, ref } from "vue";
import axios from "axios";
import { useCommon } from "../composables/useCommon";

const props = defineProps({ admin: { type: Boolean, default: false } });
const { resolve } = useCommon();
const users = ref([]);
const username = ref("");
const password = ref("");
const currentPassword = ref("");
const newPassword = ref("");
const resetUsername = ref("");
const resetPassword = ref("");
const busy = ref(false);
const error = ref("");
const notice = ref("");

function showError(err) {
	const message = err.response?.data;
	error.value = typeof message === "string" ? message.trim() : "Unable to update the account. Please try again.";
}

async function loadUsers() {
	const response = await axios.get(resolve("/api/v1/local-users"));
	users.value = response.data.users;
}

async function createUser() {
	if (busy.value) return;
	busy.value = true;
	error.value = "";
	notice.value = "";
	try {
		await axios.post(resolve("/api/v1/local-users"), { username: username.value, password: password.value });
		username.value = "";
		await loadUsers();
		notice.value = "User created. Add them to a group and grant project access separately.";
	} catch (err) {
		showError(err);
	} finally {
		password.value = "";
		busy.value = false;
	}
}

async function disableUser(user) {
	if (busy.value || !window.confirm(`Disable ${user.username} and remove their group and project access?`)) return;
	busy.value = true;
	error.value = "";
	notice.value = "";
	try {
		await axios.delete(resolve(`/api/v1/local-users/${encodeURIComponent(user.username)}`));
		if (resetUsername.value === user.username) resetUsername.value = "";
		await loadUsers();
		notice.value = "User disabled and access removed.";
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

async function resetUserPassword() {
	if (busy.value || !resetUsername.value) return;
	busy.value = true;
	error.value = "";
	notice.value = "";
	try {
		await axios.post(resolve(`/api/v1/local-users/${encodeURIComponent(resetUsername.value)}/password`), {
			password: resetPassword.value,
		});
		resetUsername.value = "";
		notice.value = "Password reset. The user must sign in again.";
	} catch (err) {
		showError(err);
	} finally {
		resetPassword.value = "";
		busy.value = false;
	}
}

async function changePassword() {
	if (busy.value) return;
	busy.value = true;
	error.value = "";
	notice.value = "";
	try {
		await axios.post(resolve("/api/v1/local-password"), {
			current_password: currentPassword.value,
			new_password: newPassword.value,
		});
		window.location.assign(resolve("/auth/login"));
	} catch (err) {
		showError(err);
	} finally {
		currentPassword.value = "";
		newPassword.value = "";
		busy.value = false;
	}
}

onMounted(async () => {
	if (!props.admin) return;
	try {
		await loadUsers();
	} catch (err) {
		showError(err);
	}
});
</script>

<template>
	<section id="local-accounts" class="card shadow-sm mb-4" aria-labelledby="local-accounts-title">
		<div class="card-body">
			<h2 id="local-accounts-title" class="h5">Local accounts</h2>
			<div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
			<p v-if="notice" class="text-success" role="status">{{ notice }}</p>
			<details class="mb-3">
				<summary>Change my password</summary>
				<form class="row g-2 mt-1" @submit.prevent="changePassword">
					<div class="col-md-5">
						<label class="form-label" for="current-password">Current password</label>
						<input
							id="current-password"
							v-model="currentPassword"
							class="form-control"
							type="password"
							autocomplete="current-password"
							required
							maxlength="72"
						/>
					</div>
					<div class="col-md-5">
						<label class="form-label" for="new-password">New password</label>
						<input
							id="new-password"
							v-model="newPassword"
							class="form-control"
							type="password"
							autocomplete="new-password"
							required
							maxlength="72"
						/>
					</div>
					<div class="col-md-2 align-self-end">
						<button class="btn btn-outline-primary w-100" :disabled="busy">Change</button>
					</div>
					<p class="small text-muted mb-0">
						Use 12–72 UTF-8 bytes. You will sign in again after changing it.
					</p>
				</form>
			</details>
			<template v-if="admin">
				<h3 class="h6">Create a user</h3>
				<form class="row g-2" @submit.prevent="createUser">
					<div class="col-md-5">
						<label class="form-label" for="local-username">Username</label>
						<input
							id="local-username"
							v-model="username"
							class="form-control"
							autocomplete="off"
							required
							pattern="[a-z0-9][a-z0-9._-]{2,63}"
							maxlength="64"
						/>
					</div>
					<div class="col-md-5">
						<label class="form-label" for="local-user-password">Temporary password</label>
						<input
							id="local-user-password"
							v-model="password"
							class="form-control"
							type="password"
							autocomplete="new-password"
							required
							maxlength="72"
						/>
					</div>
					<div class="col-md-2 align-self-end">
						<button class="btn btn-primary w-100" :disabled="busy">Create user</button>
					</div>
					<p class="small text-muted mb-0">
						Usernames are lowercase; passwords use 12–72 UTF-8 bytes. Share the temporary password securely.
					</p>
				</form>
				<ul class="list-group list-group-flush mt-3">
					<li
						v-for="user in users"
						:key="user.username"
						class="list-group-item d-flex justify-content-between align-items-center gap-2"
					>
						<span class="text-break"
							>{{ user.username }}
							<span v-if="user.admin" class="badge text-bg-secondary">Administrator</span
							><span v-else-if="user.disabled" class="badge text-bg-light">Disabled</span></span
						>
						<div v-if="!user.admin && !user.disabled" class="d-flex gap-2">
							<button
								class="btn btn-sm btn-outline-secondary"
								:disabled="busy"
								:aria-label="`Reset password for ${user.username}`"
								@click="resetUsername = user.username"
							>
								Reset password
							</button>
							<button
								class="btn btn-sm btn-outline-danger"
								:disabled="busy"
								:aria-label="`Disable ${user.username}`"
								@click="disableUser(user)"
							>
								Disable
							</button>
						</div>
					</li>
				</ul>
				<form v-if="resetUsername" class="row g-2 mt-2" @submit.prevent="resetUserPassword">
					<div class="col-md-8">
						<label class="form-label" for="reset-password"
							>New temporary password for {{ resetUsername }}</label
						>
						<input
							id="reset-password"
							v-model="resetPassword"
							class="form-control"
							type="password"
							autocomplete="new-password"
							required
							maxlength="72"
						/>
					</div>
					<div class="col-md-4 align-self-end d-flex gap-2">
						<button class="btn btn-outline-primary" :disabled="busy">Reset</button>
						<button
							type="button"
							class="btn btn-outline-secondary"
							:disabled="busy"
							@click="
								resetUsername = '';
								resetPassword = '';
							"
						>
							Cancel
						</button>
					</div>
				</form>
			</template>
		</div>
	</section>
</template>
