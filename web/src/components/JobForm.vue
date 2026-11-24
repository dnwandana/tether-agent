<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue"
import { api, runCommand } from "../api/client"
import type { JobDetail } from "../api/types.gen"
import { replyKind } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { useDirty } from "../lib/dirty"
import { shortTime } from "../lib/time"
import { toast } from "../stores/ui"

const props = defineProps<{ job: JobDetail | null }>()
const emit = defineEmits<{ saved: []; cancel: []; dirty: [on: boolean] }>()

type Form = {
  name: string
  schedule: string
  prompt: string
  script: string
  noAgent: boolean
  repeat: number
  deliver: string
  model: string // "" or "<entry>:<model>"
  effort: string
  watch: boolean
  draftOnly: boolean
  continuity: boolean
  skills: string[]
  toolsets: string[]
  workdir: string
  enabled: boolean
}

// Returns the form values of a job, or the empty values for a new job.
function formOf(j: JobDetail | null): Form {
  return {
    name: j?.job.name ?? "",
    schedule: j?.job.schedule ?? "",
    prompt: j?.prompt ?? "",
    script: j?.script ?? "",
    noAgent: j?.no_agent ?? false,
    repeat: j?.repeat_left ?? 0,
    deliver: j?.deliver ?? "",
    model: j?.model ?? "",
    effort: j?.effort ?? "",
    watch: j?.job.watch ?? false,
    draftOnly: j?.job.draft_only ?? false,
    continuity: j?.continuity ?? false,
    skills: [...(j?.skills ?? [])],
    toolsets: [...(j?.toolsets ?? [])],
    workdir: j?.workdir ?? "",
    enabled: j?.job.enabled ?? true,
  }
}

// The form reads the prop once, so a live reload of the page never moves the baseline.
const base = Object.freeze(formOf(props.job))
const f = reactive(formOf(props.job))

const errors = reactive({ name: "", schedule: "", prompt: "" })
const note = ref("")
const error = ref("")
const busy = ref(false)

const models = ref<string[]>([])
const skillNames = ref<string[]>([])
const toolsetNames = ref<string[]>([])
const defaultDeliver = ref("")
const skillQuery = ref("")

// Returns the skills whose name holds the search text, in any letter case.
const shownSkills = computed(() => {
  const q = skillQuery.value.trim().toLowerCase()
  return q ? skillNames.value.filter((s) => s.toLowerCase().includes(q)) : skillNames.value
})

// Removes one skill from the selected skills.
function removeSkill(s: string): void {
  f.skills = f.skills.filter((x) => x !== s)
}

const efforts = ["none", "minimal", "low", "medium", "high", "xhigh", "max"]

// Returns the model text without the "default" word.
function modelText(m: string): string {
  const t = m.trim()
  return t === "default" ? "" : t
}

// Returns the cronjob arguments of the fields that differ from the baseline. A cleared field
// gives an explicit empty value, so the tool clears it.
function changes(): Record<string, unknown> {
  const c: Record<string, unknown> = {}
  const text = (k: "name" | "schedule" | "deliver" | "effort" | "script" | "workdir", key = k) => {
    if (f[k].trim() !== base[k].trim()) c[key] = f[k].trim()
  }
  text("name")
  text("schedule")
  if (f.prompt !== base.prompt) c.prompt = f.prompt
  text("script")
  if (f.noAgent !== base.noAgent) c.no_agent = f.noAgent
  const rep = Number(f.repeat) || 0
  if (rep !== base.repeat) c.repeat = rep
  text("deliver")
  text("effort")
  if (f.watch !== base.watch) c.watch = f.watch
  if (f.draftOnly !== base.draftOnly) c.draft_only = f.draftOnly
  if (f.continuity !== base.continuity) c.continuity = f.continuity
  if (f.skills.join(",") !== base.skills.join(",")) c.skills = [...f.skills]
  if (f.toolsets.join(",") !== base.toolsets.join(",")) c.toolsets = [...f.toolsets]
  text("workdir")
  const m = modelText(f.model)
  if (m !== modelText(base.model)) {
    const i = m.indexOf(":")
    c.model =
      m === ""
        ? { provider: "", model: "" }
        : i < 0
          ? { provider: m, model: "" }
          : { provider: m.slice(0, i), model: m.slice(i + 1) }
  }
  return c
}

const dirty = computed(() => Object.keys(changes()).length > 0 || f.enabled !== base.enabled)
useDirty(dirty)
watch(dirty, (v) => emit("dirty", v))

// Sets the field errors. Returns true when all fields are good.
function check(): boolean {
  errors.name = f.name.trim() === "" ? "Enter a name." : ""
  errors.schedule = f.schedule.trim() === "" ? "Enter a schedule." : ""
  errors.prompt = f.prompt.trim() === "" && !f.noAgent ? "Enter a prompt, or turn on No agent." : ""
  return !errors.name && !errors.schedule && !errors.prompt
}

// Returns the next run text of a job reply, or "" when the job has no next run.
function nextRunOf(text: string): string {
  const m = /next run (.+?)(?: · |\n|$)/.exec(text)
  return m && m[1] !== "none" ? m[1] : ""
}

// Returns the short local time, or a dash for a zero or bad time.
function when(iso: string | undefined): string {
  return shortTime(iso) || "—"
}

async function save(): Promise<void> {
  if (busy.value) return
  note.value = ""
  error.value = ""
  if (!check()) return
  const ch = changes()
  const enabledChanged = f.enabled !== base.enabled
  const hasChange = Object.keys(ch).length > 0
  if (props.job && !hasChange && !enabledChanged) {
    note.value = "No changes."
    return
  }
  busy.value = true
  try {
    let id = props.job?.job.id ?? ""
    let next = ""
    if (hasChange || !props.job) {
      const body = {
        action: props.job ? "update" : "create",
        ...(props.job ? { job_id: props.job.job.id } : {}),
        ...ch,
      }
      const r = await api<{ text?: string }>("POST", "/api/jobs", body)
      const text = r?.text ?? ""
      if (!props.job) id = /^created job (\S+)/.exec(text)?.[1] ?? ""
      next = nextRunOf(text)
    }
    if (enabledChanged && !id) {
      toast(
        "Saved the job. Its Enabled state did not change. Use Pause or Resume on the job.",
        "warn",
      )
    } else if (enabledChanged) {
      // The job is saved at this point, so an error of the pause or resume does not stop the
      // save. Else the form stays open, and a second Save makes a second job.
      try {
        const r = await runCommand(`/jobs ${f.enabled ? "resume" : "pause"} ${id}`)
        // A reply with no text is an ack that comes later. It counts as ok.
        const text = "text" in r ? r.text : ""
        const kind = replyKind(text)
        if (kind === "error" || kind === "warn") toast(text, kind)
        else toast(next ? `Saved the job. Next run: ${next}.` : "Saved the job.", "ok")
      } catch (e) {
        toast(
          `Saved the job. Its Enabled state did not change: ${friendlyError(e, "/api/command")}`,
          "warn",
        )
      }
    } else {
      toast(next ? `Saved the job. Next run: ${next}.` : "Saved the job.", "ok")
    }
    emit("saved")
  } catch (e) {
    error.value = friendlyError(e, "/api/jobs")
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  const [m, s, b, br] = await Promise.allSettled([
    api<{ entries: { name: string; model: string }[] }>("GET", "/api/models"),
    api<{ name: string }[]>("GET", "/api/skills"),
    api<{ toolsets: string[] }>("GET", "/api/bootstrap"),
    api<{ default_deliver: string }>("GET", "/api/brief"),
  ])
  if (m.status === "fulfilled")
    models.value = (m.value?.entries ?? []).map((e) => `${e.name}:${e.model}`)
  if (s.status === "fulfilled") skillNames.value = (s.value ?? []).map((x) => x.name)
  if (b.status === "fulfilled") toolsetNames.value = b.value?.toolsets ?? []
  if (br.status === "fulfilled") defaultDeliver.value = br.value?.default_deliver ?? ""
  for (const x of f.skills) if (!skillNames.value.includes(x)) skillNames.value.push(x)
  for (const x of f.toolsets) if (!toolsetNames.value.includes(x)) toolsetNames.value.push(x)
})
</script>

<template>
  <form
    class="job-form"
    style="display: flex; flex-direction: column; gap: 14px"
    @submit.prevent="save"
  >
    <div class="field">
      <label for="jf-name">Name</label>
      <input
        id="jf-name"
        v-model="f.name"
        class="input"
        data-field="name"
        placeholder="e.g. morning-digest"
      />
      <span v-if="errors.name" class="t-12 c-bad">{{ errors.name }}</span>
    </div>
    <div class="field">
      <label for="jf-schedule">Schedule</label>
      <input id="jf-schedule" v-model="f.schedule" class="input mono" data-field="schedule" />
      <span v-if="errors.schedule" class="t-12 c-bad">{{ errors.schedule }}</span>
      <span class="t-12 c-3"
        >A 5-field cron expression, an interval from 1 minute to 366 days, or one ISO time.</span
      >
      <span v-if="props.job && f.schedule.trim() === base.schedule" class="t-12 c-3"
        >Next run: {{ when(props.job.job.next_run) }}</span
      >
    </div>
    <div class="field">
      <label for="jf-prompt">Prompt</label>
      <textarea
        id="jf-prompt"
        v-model="f.prompt"
        class="input"
        data-field="prompt"
        rows="5"
        style="height: auto; padding: 8px 10px"
      />
      <span v-if="errors.prompt" class="t-12 c-bad">{{ errors.prompt }}</span>
      <span
        v-if="props.job?.prompt_masked && f.prompt !== base.prompt"
        class="t-12 c-warn"
        role="status"
        >The prompt holds a masked secret. Type the full prompt again, or the secret is lost.</span
      >
    </div>
    <div class="field">
      <label for="jf-script">Script</label>
      <input id="jf-script" v-model="f.script" class="input mono" data-field="script" />
    </div>
    <label class="check"
      ><input v-model="f.noAgent" type="checkbox" data-field="no_agent" />No agent: run only the
      script</label
    >
    <div class="field">
      <label for="jf-repeat">Repeat</label>
      <input
        id="jf-repeat"
        v-model.number="f.repeat"
        class="input"
        type="number"
        min="0"
        data-field="repeat"
      />
      <span class="t-12 c-3">0 runs the job with no end.</span>
    </div>
    <div class="field">
      <label for="jf-deliver">Deliver to</label>
      <input id="jf-deliver" v-model="f.deliver" class="input" data-field="deliver" />
      <span v-if="defaultDeliver" class="t-12 c-3">Empty sends to {{ defaultDeliver }}.</span>
    </div>
    <div class="field">
      <label for="jf-model">Model</label>
      <input
        id="jf-model"
        v-model="f.model"
        class="input"
        data-field="model"
        list="jf-models"
        placeholder="e.g. default"
      />
      <datalist id="jf-models">
        <option v-for="m in models" :key="m" :value="m" />
      </datalist>
      <span class="t-12 c-3"
        >Leave it empty, or write default, or write &lt;entry&gt;:&lt;model&gt;.</span
      >
    </div>
    <div class="field">
      <label for="jf-effort">Effort</label>
      <select id="jf-effort" v-model="f.effort" class="input" data-field="effort">
        <option value="">default</option>
        <option v-for="e in efforts" :key="e" :value="e">{{ e }}</option>
      </select>
    </div>
    <label class="check"
      ><input v-model="f.watch" type="checkbox" data-field="watch" />Watch: report only
      changes</label
    >
    <label class="check"
      ><input v-model="f.draftOnly" type="checkbox" data-field="draft_only" />Draft only</label
    >
    <label class="check"
      ><input v-model="f.continuity" type="checkbox" data-field="continuity" />Continuity</label
    >
    <label class="check"
      ><input v-model="f.enabled" type="checkbox" data-field="enabled" />Enabled</label
    >
    <fieldset class="field" style="border: 0; padding: 0; margin: 0">
      <legend>Skills</legend>
      <div
        v-if="f.skills.length"
        class="skill-chips"
        style="display: flex; flex-wrap: wrap; gap: 6px"
      >
        <button
          v-for="s in f.skills"
          :key="s"
          type="button"
          class="chip chip-plain"
          :aria-label="`Remove ${s}`"
          @click="removeSkill(s)"
        >
          {{ s }} ✕
        </button>
      </div>
      <input
        v-if="skillNames.length > 0"
        v-model="skillQuery"
        class="input"
        type="search"
        data-field="skill-search"
        placeholder="Search skills"
        aria-label="Search skills"
        @keydown.enter.prevent
      />
      <label v-for="s in shownSkills" :key="s" class="check"
        ><input v-model="f.skills" type="checkbox" :value="s" :data-field="'skill-' + s" />{{
          s
        }}</label
      >
      <span v-if="skillNames.length > 0 && shownSkills.length === 0" class="t-12 c-3"
        >No skill matches.</span
      >
      <span v-if="skillNames.length === 0" class="t-12 c-3">No skills installed.</span>
    </fieldset>
    <fieldset class="field" style="border: 0; padding: 0; margin: 0">
      <legend>Toolsets</legend>
      <label v-for="t in toolsetNames" :key="t" class="check"
        ><input v-model="f.toolsets" type="checkbox" :value="t" :data-field="'toolset-' + t" />{{
          t
        }}</label
      >
    </fieldset>
    <div class="field">
      <label for="jf-workdir">Workdir</label>
      <input id="jf-workdir" v-model="f.workdir" class="input mono" data-field="workdir" />
    </div>
    <div v-if="note" class="t-12 c-3" role="status">{{ note }}</div>
    <div v-if="error" class="t-12 c-bad" role="alert">{{ error }}</div>
    <div style="display: flex; gap: 8px; justify-content: flex-end">
      <button class="btn btn-secondary" type="button" @click="emit('cancel')">Cancel</button>
      <button class="btn btn-primary" type="submit" :disabled="busy">
        {{ busy ? "Saving…" : "Save" }}
      </button>
    </div>
    <span class="t-12 c-3"
      >The server runs the same cronjob tool as the agent, so the same checks apply.</span
    >
  </form>
</template>
