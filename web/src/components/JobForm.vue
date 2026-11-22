<script setup lang="ts">
import { ref } from "vue"
import { api, ApiError } from "../api/client"
import type { JobDetail } from "../api/types.gen"
import { toast } from "../stores/ui"

const props = defineProps<{ job: JobDetail | null }>()
const emit = defineEmits<{ saved: []; cancel: [] }>()

const j = props.job
const name = ref(j?.job.name ?? "")
const schedule = ref(j?.job.schedule ?? "")
const prompt = ref(j?.prompt ?? "")
const deliver = ref(j?.deliver ?? "")
const model = ref(j?.model ?? "")
const effort = ref(j?.effort ?? "")
const watch = ref(j?.job.watch ?? false)
const draftOnly = ref(j?.job.draft_only ?? false)
const continuity = ref(j?.continuity ?? false)
const skills = ref((j?.skills ?? []).join(", "))
const toolsets = ref((j?.toolsets ?? []).join(", "))
const workdir = ref(j?.workdir ?? "")
const error = ref("")
const busy = ref(false)

const efforts = ["none", "minimal", "low", "medium", "high", "xhigh", "max"]

// Splits a comma list. Empty items go away.
function list(s: string): string[] {
  return s
    .split(",")
    .map((x) => x.trim())
    .filter((x) => x !== "")
}

// Builds the cronjob arguments. Empty optional fields are left out.
function body(): Record<string, unknown> {
  const b: Record<string, unknown> = {
    action: j ? "update" : "create",
    name: name.value.trim(),
    schedule: schedule.value.trim(),
    prompt: prompt.value,
    effort: effort.value,
    watch: watch.value,
    draft_only: draftOnly.value,
    continuity: continuity.value,
    skills: list(skills.value),
  }
  if (j) b.job_id = j.job.id
  if (deliver.value.trim() !== "") b.deliver = deliver.value.trim()
  const ts = list(toolsets.value)
  if (ts.length > 0) b.toolsets = ts
  if (workdir.value.trim() !== "") b.workdir = workdir.value.trim()
  const m = model.value.trim()
  if (m !== "" && m !== "default") {
    const i = m.indexOf(":")
    b.model =
      i < 0 ? { provider: m, model: "" } : { provider: m.slice(0, i), model: m.slice(i + 1) }
  }
  return b
}

async function save(): Promise<void> {
  error.value = ""
  busy.value = true
  try {
    await api("POST", "/api/jobs", body())
    toast("Saved the job.", "ok")
    emit("saved")
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <form class="job-form" style="display: flex; flex-direction: column; gap: 14px" @submit.prevent>
    <div class="field">
      <label for="jf-name">Name</label>
      <input
        id="jf-name"
        v-model="name"
        class="input"
        data-field="name"
        placeholder="morning-digest"
      />
    </div>
    <div class="field">
      <label for="jf-schedule">Schedule</label>
      <input id="jf-schedule" v-model="schedule" class="input mono" data-field="schedule" />
      <span class="t-12 c-3"
        >A 5-field cron expression, an interval from 1 minute to 366 days, or one ISO time.</span
      >
    </div>
    <div class="field">
      <label for="jf-prompt">Prompt</label>
      <textarea
        id="jf-prompt"
        v-model="prompt"
        class="input"
        data-field="prompt"
        rows="5"
        style="height: auto; padding: 8px 10px"
      />
    </div>
    <div class="field">
      <label for="jf-deliver">Deliver to</label>
      <input
        id="jf-deliver"
        v-model="deliver"
        class="input"
        data-field="deliver"
        placeholder="origin"
      />
    </div>
    <div class="field">
      <label for="jf-model">Model</label>
      <input id="jf-model" v-model="model" class="input" data-field="model" placeholder="default" />
      <span class="t-12 c-3"
        >Leave it empty, or write default, or write &lt;entry&gt;:&lt;model&gt;.</span
      >
    </div>
    <div class="field">
      <label for="jf-effort">Effort</label>
      <select id="jf-effort" v-model="effort" class="input" data-field="effort">
        <option value="">default</option>
        <option v-for="e in efforts" :key="e" :value="e">{{ e }}</option>
      </select>
    </div>
    <label class="check"
      ><input v-model="watch" type="checkbox" data-field="watch" />Watch: report only changes</label
    >
    <label class="check"
      ><input v-model="draftOnly" type="checkbox" data-field="draft_only" />Draft only</label
    >
    <label class="check"
      ><input v-model="continuity" type="checkbox" data-field="continuity" />Continuity</label
    >
    <div class="field">
      <label for="jf-skills">Skills</label>
      <input
        id="jf-skills"
        v-model="skills"
        class="input"
        data-field="skills"
        placeholder="github, notes"
      />
    </div>
    <div class="field">
      <label for="jf-toolsets">Toolsets</label>
      <input
        id="jf-toolsets"
        v-model="toolsets"
        class="input"
        data-field="toolsets"
        placeholder="web, terminal"
      />
    </div>
    <div class="field">
      <label for="jf-workdir">Workdir</label>
      <input id="jf-workdir" v-model="workdir" class="input mono" data-field="workdir" />
    </div>
    <div v-if="error" class="t-12 c-bad" role="alert">{{ error }}</div>
    <div style="display: flex; gap: 8px; justify-content: flex-end">
      <button class="btn btn-secondary" type="button" @click="emit('cancel')">Cancel</button>
      <button class="btn btn-primary" type="button" :disabled="busy" @click="save">Save</button>
    </div>
    <span class="t-12 c-3"
      >The server runs the same cronjob tool as the agent, so the same checks apply.</span
    >
  </form>
</template>
