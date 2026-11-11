<script setup>
import { computed, onMounted, ref } from "vue";
import axios from "axios";
import AppLayout from "../components/AppLayout.vue";
import AuditLog from "../components/AuditLog.vue";
import EnvironmentCredentials from "../components/EnvironmentCredentials.vue";
import EnvironmentRetention from "../components/EnvironmentRetention.vue";
import ProjectInbox from "../components/ProjectInbox.vue";
import { useCommon } from "../composables/useCommon";
import mimLogo from "../assets/mim-logo.png";

const { resolve } = useCommon();
const mimLogoURL = resolve(`/dist/${mimLogo.replace(/^\.\//, "")}`);
const user = ref(null);
const groups = ref([]);
const groupID = ref("");
const groupMembers = ref([]);
const projects = ref([]);
const project = ref(null);
const projectMembers = ref([]);
const environments = ref([]);
const environmentID = ref("");
const projectName = ref("");
const environmentName = ref("");
const memberSubject = ref("");
const memberRole = ref("viewer");
const busy = ref(false);
const error = ref("");

const environment = computed(() => environments.value.find((item) => item.id === environmentID.value));
const canManage = computed(
	() =>
		user.value?.admin ||
		projectMembers.value.some(
			(member) =>
				member.issuer === user.value?.issuer &&
				member.subject === user.value?.subject &&
				member.role === "admin",
		),
);
const canWrite = computed(
	() =>
		user.value?.admin ||
		projectMembers.value.some(
			(member) =>
				member.issuer === user.value?.issuer &&
				member.subject === user.value?.subject &&
				(member.role === "admin" || member.role === "developer"),
		),
);

function showError(err) {
	const message = err.response?.data;
	error.value = typeof message === "string" ? message.trim() : "Unable to complete this request. Please try again.";
}

async function selectGroup() {
	project.value = null;
	projects.value = [];
	groupMembers.value = [];
	environments.value = [];
	environmentID.value = "";
	error.value = "";
	if (!groupID.value) return;
	const selected = groupID.value;
	busy.value = true;
	try {
		const [projectResult, memberResult] = await Promise.all([
			axios.get(resolve(`/api/v1/groups/${encodeURIComponent(selected)}/projects`)),
			axios.get(resolve(`/api/v1/groups/${encodeURIComponent(selected)}/members`)),
		]);
		if (groupID.value !== selected) return;
		projects.value = projectResult.data.projects;
		groupMembers.value = memberResult.data.members;
	} catch (err) {
		if (groupID.value === selected) showError(err);
	} finally {
		busy.value = false;
	}
}

async function selectProject(item) {
	project.value = item;
	projectMembers.value = [];
	environments.value = [];
	environmentID.value = "";
	error.value = "";
	busy.value = true;
	try {
		const [memberResult, environmentResult] = await Promise.all([
			axios.get(resolve(`/api/v1/projects/${encodeURIComponent(item.id)}/members`)),
			axios.get(resolve(`/api/v1/projects/${encodeURIComponent(item.id)}/environments`)),
		]);
		if (project.value?.id !== item.id) return;
		projectMembers.value = memberResult.data.members;
		environments.value = environmentResult.data.environments;
		environmentID.value = environments.value[0]?.id || "";
	} catch (err) {
		if (project.value?.id === item.id) showError(err);
	} finally {
		busy.value = false;
	}
}

async function createProject() {
	if (!projectName.value.trim() || busy.value) return;
	busy.value = true;
	try {
		const response = await axios.post(resolve(`/api/v1/groups/${encodeURIComponent(groupID.value)}/projects`), {
			name: projectName.value.trim(),
		});
		projectName.value = "";
		await selectGroup();
		await selectProject(response.data);
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

async function createEnvironment() {
	if (!environmentName.value.trim() || busy.value) return;
	busy.value = true;
	try {
		const response = await axios.post(
			resolve(`/api/v1/projects/${encodeURIComponent(project.value.id)}/environments`),
			{ name: environmentName.value.trim() },
		);
		environments.value.push(response.data);
		environmentID.value = response.data.id;
		environmentName.value = "";
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

async function grant() {
	if (!memberSubject.value || busy.value) return;
	busy.value = true;
	try {
		await axios.post(resolve(`/api/v1/projects/${encodeURIComponent(project.value.id)}/members`), {
			subject: memberSubject.value,
			role: memberRole.value,
		});
		const response = await axios.get(resolve(`/api/v1/projects/${encodeURIComponent(project.value.id)}/members`));
		projectMembers.value = response.data.members;
		memberSubject.value = "";
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

async function revoke(member) {
	if (busy.value || !window.confirm(`Remove ${member.subject} from this project?`)) return;
	busy.value = true;
	try {
		await axios.delete(resolve(`/api/v1/projects/${encodeURIComponent(project.value.id)}/members`), {
			data: { issuer: member.issuer, subject: member.subject },
		});
		await selectProject(project.value);
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
}

onMounted(async () => {
	busy.value = true;
	try {
		const [identity, groupResult] = await Promise.all([
			axios.get(resolve("/api/v1/me")),
			axios.get(resolve("/api/v1/groups")),
		]);
		user.value = identity.data;
		groups.value = groupResult.data.groups;
		groupID.value = groups.value[0]?.id || "";
		await selectGroup();
	} catch (err) {
		showError(err);
	} finally {
		busy.value = false;
	}
});
</script>

<template>
	<AppLayout active-tab="projects" offcanvas-id="projectsOffcanvas" :show-sidebar="false" :loading="busy ? 1 : 0">
		<main class="container py-4 overflow-y-auto">
			<div class="d-flex flex-wrap align-items-center justify-content-between gap-3 mb-4">
				<div>
					<p class="text-uppercase text-muted small fw-semibold mb-1">Developer sandbox</p>
					<h1 class="h3 mb-1">Projects</h1>
					<p class="text-muted mb-0">Explore the environments shared with you.</p>
				</div>
				<img :src="mimLogoURL" alt="Ministry of Industry and Mineral Resources" class="mim-logo" width="190" />
			</div>
			<div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
			<div class="row g-4">
				<div class="col-lg-4">
					<section class="card shadow-sm">
						<div class="card-body">
							<label for="project-group" class="form-label fw-semibold">Group</label>
							<select
								id="project-group"
								v-model="groupID"
								class="form-select mb-3"
								:disabled="busy || !groups.length"
								@change="selectGroup"
							>
								<option v-if="!groups.length" value="">No groups available</option>
								<option v-for="group in groups" :key="group.id" :value="group.id">
									{{ group.name }}
								</option>
							</select>
							<div class="d-flex justify-content-between align-items-center mb-2">
								<h2 class="h6 mb-0">Projects</h2>
								<span class="badge text-bg-light">{{ projects.length }}</span>
							</div>
							<p v-if="!projects.length" class="text-muted small">
								No projects have been shared with you in this group.
							</p>
							<div v-else class="list-group mb-3">
								<button
									v-for="item in projects"
									:key="item.id"
									class="list-group-item list-group-item-action text-start"
									:class="{ active: project?.id === item.id }"
									@click="selectProject(item)"
								>
									{{ item.name }}
								</button>
							</div>
							<form v-if="user?.admin && groupID" class="d-flex gap-2" @submit.prevent="createProject">
								<label class="visually-hidden" for="new-project">New project</label
								><input
									id="new-project"
									v-model="projectName"
									class="form-control"
									maxlength="100"
									placeholder="Project name"
									required
								/><button class="btn btn-outline-primary text-nowrap" :disabled="busy">Create</button>
							</form>
						</div>
					</section>
				</div>
				<div class="col-lg-8">
					<template v-if="project">
						<section class="card shadow-sm">
							<div class="card-body">
								<h2 class="h5">{{ project.name }}</h2>
								<label for="project-environment" class="form-label">Environment</label>
								<select id="project-environment" v-model="environmentID" class="form-select">
									<option v-for="item in environments" :key="item.id" :value="item.id">
										{{ item.name }}
									</option>
								</select>
								<form v-if="canManage" class="d-flex gap-2 mt-3" @submit.prevent="createEnvironment">
									<label class="visually-hidden" for="new-environment">New environment</label
									><input
										id="new-environment"
										v-model="environmentName"
										class="form-control"
										maxlength="100"
										placeholder="Environment name"
										required
									/><button class="btn btn-outline-primary text-nowrap" :disabled="busy">
										Add environment
									</button>
								</form>
								<details class="mt-3">
									<summary class="fw-semibold">Project access</summary>
									<p class="small text-muted mt-2">Group members need a separate project role.</p>
									<ul class="list-group list-group-flush">
										<li
											v-for="member in projectMembers"
											:key="member.issuer + member.subject"
											class="list-group-item d-flex justify-content-between align-items-center gap-2 px-0"
										>
											<span class="text-break"
												>{{ member.subject }}
												<span class="text-muted small">· {{ member.role }}</span></span
											><button
												v-if="canManage"
												class="btn btn-sm btn-outline-danger"
												:disabled="busy"
												@click="revoke(member)"
											>
												Remove
											</button>
										</li>
									</ul>
									<form v-if="canManage" class="row g-2 mt-1" @submit.prevent="grant">
										<div class="col-md-6">
											<label for="project-member" class="visually-hidden">Group member</label
											><select
												id="project-member"
												v-model="memberSubject"
												class="form-select"
												required
											>
												<option value="">Select a group member</option>
												<option
													v-for="member in groupMembers"
													:key="member.issuer + member.subject"
													:value="member.subject"
												>
													{{ member.subject }}
												</option>
											</select>
										</div>
										<div class="col-md-4">
											<label for="project-role" class="visually-hidden">Project role</label
											><select id="project-role" v-model="memberRole" class="form-select">
												<option value="viewer">Viewer</option>
												<option value="developer">Developer</option>
												<option value="admin">Administrator</option>
											</select>
										</div>
										<div class="col-md-2">
											<button class="btn btn-outline-primary w-100" :disabled="busy">
												Grant
											</button>
										</div>
									</form>
								</details>
							</div>
						</section>
						<ProjectInbox v-if="environment" :environment="environment" :can-delete="canWrite" />
						<EnvironmentCredentials v-if="environment && canManage" :environment="environment" />
						<EnvironmentRetention v-if="environment && canManage" :environment="environment" />
					</template>
					<div v-else class="card shadow-sm">
						<div class="card-body py-5 text-center text-muted">Select a project to open its sandbox.</div>
					</div>
				</div>
			</div>
			<AuditLog v-if="user?.admin" />
		</main>
	</AppLayout>
</template>
