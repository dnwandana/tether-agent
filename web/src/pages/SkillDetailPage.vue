<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError } from "../api/client"
import type { SkillDetailView, TaskView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import StateBlock from "../components/StateBlock.vue"
import { renderMarkdown } from "../lib/markdown"
import { confirm, toast } from "../stores/ui"

type Action = "update" | "uninstall" | "reset"

const route = useRoute()
const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const view = ref<SkillDetailView | null>(null)
let alive = true

const name = computed(() => String(route.params.name))
const html = computed(() => renderMarkdown(view.value?.body ?? ""))
const hasLock = computed(
  () => !!view.value && (view.value.skill.source !== "" || view.value.installed_at !== ""),
)
const scanned = computed(() => !!view.value && view.value.skill.trust !== "builtin")

const findingColumns = [
  { key: "description", label: "Finding" },
  { key: "category", label: "Category" },
  { key: "where", label: "File" },
  { key: "severity", label: "Severity" },
  { key: "match", label: "Match" },
]
type Finding = SkillDetailView["findings"][number]
const f = (x: unknown) => x as Finding
const findingRows = computed(() =>
  (view.value?.findings ?? []).map((x, i) => ({
    ...x,
    key: String(i),
    where: `${x.file}:${x.line}`,
  })),
)

function verdictClass(v: string): string {
  if (v === "safe") return "chip-ok"
  if (v === "dangerous") return "chip-bad"
  return "chip-warn"
}

function when(s: string): string {
  const d = new Date(s)
  if (!s || Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "—"
  return d.toLocaleString()
}

async function load(): Promise<void> {
  try {
    view.value = await api<SkillDetailView>("GET", `/api/skills/${name.value}`)
    state.value = "ready"
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// Polls a task every 2 s for 2 min at most. An error shows its text. A done task loads the detail again.
async function follow(id: string, doneText: string): Promise<boolean> {
  for (let i = 0; i < 60; i++) {
    await sleep(2000)
    if (!alive) return false
    let t: TaskView
    try {
      t = await api<TaskView>("GET", `/api/tasks/${id}`)
    } catch {
      continue
    }
    if (t.state === "error") {
      toast(t.text, "error")
      return false
    }
    if (t.state === "done") {
      await load()
      toast(doneText)
      return true
    }
  }
  return false
}

const verbs: Record<Action, [string, string]> = {
  update: ["Updating", "Updated"],
  uninstall: ["Uninstalling", "Uninstalled"],
  reset: ["Resetting", "Reset"],
}

// Posts one skill action and follows it with the task rule.
async function skillAction(action: Action): Promise<void> {
  const n = name.value
  if (action === "reset") {
    const ok = await confirm({
      title: `Reset /${n}?`,
      body: "The bundled version replaces your local changes.",
      confirmLabel: "Reset",
    })
    if (!ok) return
  }
  if (action === "uninstall") {
    const ok = await confirm({
      title: `Uninstall /${n}?`,
      body: "The skill folder is removed. Cron jobs that use it lose it.",
      confirmLabel: "Uninstall",
      danger: true,
    })
    if (!ok) return
  }
  let id = ""
  try {
    id = (await api<{ task_id: string }>("POST", `/api/skills/${n}/${action}`)).task_id
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
    return
  }
  toast(`${verbs[action][0]} /${n}…`, "info")
  await follow(id, `${verbs[action][1]} /${n}. It loads in new sessions.`)
  if (action === "uninstall" && state.value === "error") await router.push({ name: "skills" })
}

onMounted(load)
onBeforeUnmount(() => {
  alive = false
})
</script>

<template>
  <div class="page">
    <router-link class="back" :to="{ name: 'skills' }">‹ Skills</router-link>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @retry="load"
    />
    <template v-else-if="view">
      <div class="page-h">
        <h1 class="t-20">/{{ view.skill.name }}</h1>
        <span class="chip chip-plain">{{ view.skill.trust }}</span>
        <button
          v-if="view.skill.actions.includes('update') && !view.skill.pinned"
          class="btn btn-secondary"
          type="button"
          @click="skillAction('update')"
        >
          Update
        </button>
        <button
          v-if="view.skill.actions.includes('reset')"
          class="btn btn-secondary"
          type="button"
          @click="skillAction('reset')"
        >
          Reset
        </button>
        <button
          v-if="view.skill.actions.includes('uninstall')"
          class="btn btn-danger-soft"
          type="button"
          @click="skillAction('uninstall')"
        >
          Uninstall
        </button>
      </div>

      <div class="grid-2">
        <div class="card">
          <div class="card-h"><h3>SKILL.md</h3></div>
          <div class="card-b">
            <div class="md" v-html="html" />
            <template v-if="view.files.length">
              <h4 class="t-12 c-3">Files</h4>
              <div v-for="file in view.files" :key="file" class="mono">{{ file }}</div>
            </template>
          </div>
        </div>
        <div class="card">
          <div class="card-h"><h3>Lockfile</h3></div>
          <div class="card-b">
            <dl v-if="hasLock" class="kv">
              <dt>Source</dt>
              <dd class="mono">{{ view.skill.source || "—" }}</dd>
              <dt>Ref</dt>
              <dd class="mono">{{ view.ref || "—" }}</dd>
              <dt>Commit</dt>
              <dd class="mono">{{ view.skill.commit || "—" }}</dd>
              <dt>Installed</dt>
              <dd>{{ when(view.installed_at) }}</dd>
              <dt>Updated</dt>
              <dd>{{ when(view.updated_at) }}</dd>
              <dt>Content hash</dt>
              <dd class="mono">{{ view.content_hash || "—" }}</dd>
            </dl>
            <p v-else class="c-3">Not installed from a source.</p>
          </div>
        </div>
      </div>

      <template v-if="scanned">
        <div class="section-h">
          <h2>Guard scan</h2>
          <span v-if="view.skill.verdict" class="chip" :class="verdictClass(view.skill.verdict)">{{
            view.skill.verdict
          }}</span>
          <span class="c-3 t-12"
            >{{ view.findings.length }} {{ view.findings.length === 1 ? "finding" : "findings" }} ·
            {{ view.unscanned.length }} not scanned</span
          >
        </div>
        <StateBlock v-if="view.findings.length === 0" state="empty" text="No findings" />
        <DataTable
          v-else
          :columns="findingColumns"
          :rows="findingRows as unknown as Record<string, unknown>[]"
          row-key="key"
        >
          <template #cell-where="{ row }"
            ><span class="mono">{{ f(row).file }}:{{ f(row).line }}</span></template
          >
          <template #cell-severity="{ row }"
            ><span
              class="chip"
              :class="
                f(row).severity === 'high' || f(row).severity === 'critical'
                  ? 'chip-bad'
                  : 'chip-warn'
              "
              >{{ f(row).severity }}</span
            ></template
          >
          <template #cell-match="{ row }"
            ><code>{{ f(row).match }}</code></template
          >
        </DataTable>
      </template>
    </template>
  </div>
</template>
