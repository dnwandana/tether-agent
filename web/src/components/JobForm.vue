<script lang="ts">
// Counts the forms of the page, so each form gets its own element ids.
let formCount = 0
</script>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue"
import { api, runCommand } from "../api/client"
import type { JobDetail } from "../api/types.gen"
import { replyKind } from "../lib/command"
import { friendlyError } from "../lib/errors"
import { useDirty } from "../lib/dirty"
import { shortTime } from "../lib/time"
import { toast } from "../stores/ui"
import { XIcon } from "@lucide/vue"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { NativeSelect } from "@/components/ui/native-select"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"

// Each form instance has its own ids, so two forms on one page do not share a label target.
const uid = ++formCount
const elId = (k: string): string => `jf-${uid}-${k}`

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
    // The server keeps -1 for a job with no end. The form shows 0, which means the same.
    repeat: Math.max(0, j?.repeat_left ?? 0),
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

// Returns a new list with the item added at the end (on) or removed (off).
function toggleIn(list: string[], item: string, on: boolean | "indeterminate"): string[] {
  if (on === true) return list.includes(item) ? [...list] : [...list, item]
  return list.filter((x) => x !== item)
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
        // The reply is Go Markdown with marks, so the toast renders it with its icons.
        if (kind === "error" || kind === "warn") toast(text, kind, { markdown: true })
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
  <form class="flex flex-col gap-4" @submit.prevent="save">
    <FieldGroup>
      <Field :data-invalid="!!errors.name || undefined">
        <FieldLabel :for="elId('name')">Name</FieldLabel>
        <Input
          :id="elId('name')"
          v-model="f.name"
          :aria-invalid="!!errors.name || undefined"
          placeholder="e.g. morning-digest"
        />
        <FieldError v-if="errors.name">{{ errors.name }}</FieldError>
      </Field>
      <Field :data-invalid="!!errors.schedule || undefined">
        <FieldLabel :for="elId('schedule')">Schedule</FieldLabel>
        <Input
          :id="elId('schedule')"
          v-model="f.schedule"
          class="font-mono"
          :aria-invalid="!!errors.schedule || undefined"
        />
        <FieldError v-if="errors.schedule">{{ errors.schedule }}</FieldError>
        <FieldDescription
          >A 5-field cron expression, an interval from 1 minute to 366 days, or one ISO
          time.</FieldDescription
        >
        <FieldDescription v-if="props.job && f.schedule.trim() === base.schedule"
          >Next run: {{ when(props.job.job.next_run) }}</FieldDescription
        >
      </Field>
      <Field :data-invalid="!!errors.prompt || undefined">
        <FieldLabel :for="elId('prompt')">Prompt</FieldLabel>
        <Textarea
          :id="elId('prompt')"
          v-model="f.prompt"
          rows="5"
          :aria-invalid="!!errors.prompt || undefined"
        />
        <FieldError v-if="errors.prompt">{{ errors.prompt }}</FieldError>
        <span
          v-if="props.job?.prompt_masked && f.prompt !== base.prompt"
          class="text-xs text-warning"
          role="status"
          >The prompt holds a masked secret. Type the full prompt again, or the secret is
          lost.</span
        >
      </Field>
      <Field>
        <FieldLabel :for="elId('script')">Script</FieldLabel>
        <Input :id="elId('script')" v-model="f.script" class="font-mono" />
      </Field>
      <Field orientation="horizontal">
        <Checkbox :id="elId('no-agent')" v-model="f.noAgent" />
        <FieldLabel :for="elId('no-agent')" class="font-normal"
          >No agent: run only the script</FieldLabel
        >
      </Field>
      <Field>
        <FieldLabel :for="elId('repeat')">Repeat</FieldLabel>
        <Input :id="elId('repeat')" v-model.number="f.repeat" type="number" min="0" />
        <FieldDescription>0 runs the job with no end.</FieldDescription>
      </Field>
      <Field>
        <FieldLabel :for="elId('deliver')">Deliver to</FieldLabel>
        <Input :id="elId('deliver')" v-model="f.deliver" />
        <FieldDescription v-if="defaultDeliver"
          >Empty sends to {{ defaultDeliver }}.</FieldDescription
        >
      </Field>
      <Field>
        <FieldLabel :for="elId('model')">Model</FieldLabel>
        <Input
          :id="elId('model')"
          v-model="f.model"
          :list="elId('models')"
          placeholder="e.g. default"
        />
        <datalist :id="elId('models')">
          <option v-for="m in models" :key="m" :value="m" />
        </datalist>
        <FieldDescription
          >Leave it empty, or write default, or write &lt;entry&gt;:&lt;model&gt;.</FieldDescription
        >
      </Field>
      <Field>
        <FieldLabel :for="elId('effort')">Effort</FieldLabel>
        <NativeSelect :id="elId('effort')" v-model="f.effort">
          <option value="">default</option>
          <option v-for="e in efforts" :key="e" :value="e">{{ e }}</option>
        </NativeSelect>
      </Field>
      <Field orientation="horizontal">
        <Checkbox :id="elId('watch')" v-model="f.watch" />
        <FieldLabel :for="elId('watch')" class="font-normal">Watch: report only changes</FieldLabel>
      </Field>
      <Field orientation="horizontal">
        <Checkbox :id="elId('draft-only')" v-model="f.draftOnly" />
        <FieldLabel :for="elId('draft-only')" class="font-normal">Draft only</FieldLabel>
      </Field>
      <Field orientation="horizontal">
        <Checkbox :id="elId('continuity')" v-model="f.continuity" />
        <FieldLabel :for="elId('continuity')" class="font-normal">Continuity</FieldLabel>
      </Field>
      <Field orientation="horizontal">
        <Checkbox :id="elId('enabled')" v-model="f.enabled" />
        <FieldLabel :for="elId('enabled')" class="font-normal">Enabled</FieldLabel>
      </Field>
      <FieldSet>
        <FieldLegend variant="label">Skills</FieldLegend>
        <div v-if="f.skills.length" class="flex flex-wrap gap-2">
          <Button
            v-for="s in f.skills"
            :key="s"
            type="button"
            variant="secondary"
            size="xs"
            :aria-label="`Remove ${s}`"
            @click="removeSkill(s)"
          >
            {{ s }} <XIcon />
          </Button>
        </div>
        <Input
          v-if="skillNames.length > 0"
          v-model="skillQuery"
          type="search"
          placeholder="Search skills"
          aria-label="Search skills"
          @keydown.enter.prevent
        />
        <Field v-for="s in shownSkills" :key="s" orientation="horizontal">
          <Checkbox
            :id="elId('skill-' + s)"
            :model-value="f.skills.includes(s)"
            @update:model-value="(on) => (f.skills = toggleIn(f.skills, s, on))"
          />
          <FieldLabel :for="elId('skill-' + s)" class="font-normal">{{ s }}</FieldLabel>
        </Field>
        <FieldDescription v-if="skillNames.length > 0 && shownSkills.length === 0"
          >No skill matches.</FieldDescription
        >
        <FieldDescription v-if="skillNames.length === 0">No skills installed.</FieldDescription>
      </FieldSet>
      <FieldSet>
        <FieldLegend variant="label">Toolsets</FieldLegend>
        <Field v-for="t in toolsetNames" :key="t" orientation="horizontal">
          <Checkbox
            :id="elId('toolset-' + t)"
            :model-value="f.toolsets.includes(t)"
            @update:model-value="(on) => (f.toolsets = toggleIn(f.toolsets, t, on))"
          />
          <FieldLabel :for="elId('toolset-' + t)" class="font-normal">{{ t }}</FieldLabel>
        </Field>
      </FieldSet>
      <Field>
        <FieldLabel :for="elId('workdir')">Workdir</FieldLabel>
        <Input :id="elId('workdir')" v-model="f.workdir" class="font-mono" />
      </Field>
    </FieldGroup>
    <div v-if="note" class="text-xs text-muted-foreground" role="status">{{ note }}</div>
    <div v-if="error" class="text-sm text-destructive" role="alert">{{ error }}</div>
    <div class="flex justify-end gap-2">
      <Button type="button" variant="outline" @click="emit('cancel')">Cancel</Button>
      <Button type="submit" :disabled="busy">
        <template v-if="busy"><Spinner aria-hidden="true" />Saving…</template>
        <template v-else>Save</template>
      </Button>
    </div>
    <p class="text-xs text-muted-foreground">
      The server runs the same cronjob tool as the agent, so the same checks apply.
    </p>
  </form>
</template>
