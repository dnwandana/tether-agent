<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError } from "../api/client"
import type { SkillDetailView, TaskView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import FileViewer from "../components/FileViewer.vue"
import LiveHeader from "../components/LiveHeader.vue"
import ResultNote from "../components/ResultNote.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { followTask, useLiveData } from "../lib/live"
import { renderMarkdown } from "../lib/markdown"
import type { ResultNoteData } from "../lib/command"
import { confirm, isSecure, toast, ui } from "../stores/ui"

type Action = "update" | "uninstall" | "reset"
type Running = { id: string; action: Action; waiting: boolean }

const LOST = "The gateway restarted. The result of the task is lost."

const route = useRoute()
const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const view = ref<SkillDetailView | null>(null)
const note = ref<ResultNoteData | null>(null)
const running = ref<Running | null>(null)
const forceOffer = ref<"update" | null>(null)
const manual = ref<{ findings: string } | null>(null)
const file = ref<{ path: string; line: number | null } | null>(null)
const busy = computed(() => running.value !== null)

const name = computed(() => String(route.params.name))
const html = computed(() => renderMarkdown(view.value?.body ?? ""))
const hasLock = computed(
  () => !!view.value && (view.value.skill.source !== "" || view.value.installed_at !== ""),
)
const scanned = computed(() => !!view.value && view.value.skill.trust !== "builtin")

const findingColumns = [
  { key: "description", label: "Finding" },
  { key: "category", label: "Category" },
  { key: "pattern", label: "Pattern" },
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

// Returns true when the route can open the file. Other paths are refused by the server.
function canOpen(path: string): boolean {
  return !!view.value && view.value.files.includes(path)
}

function openFile(path: string, line: number | null): void {
  file.value = { path, line }
}

async function load(): Promise<void> {
  try {
    view.value = await api<SkillDetailView>("GET", `/api/skills/${encodeURIComponent(name.value)}`)
    state.value = "ready"
  } catch (e) {
    // A failed reload keeps the shown skill. A 404 means the skill is gone, so it shows the error.
    if (state.value === "ready" && !(e instanceof ApiError && e.status === 404)) {
      toast(friendlyError(e), "error")
      return
    }
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

// Returns true for the end of a task of the kind skills.
function isEnd(_name: string, data: unknown): boolean {
  const t = data as Partial<TaskView> | null
  return !!t && t.kind === "skills" && (t.state === "done" || t.state === "error")
}

const verbs: Record<Action, [string, string]> = {
  update: ["Updating", "Updated"],
  uninstall: ["Uninstalling", "Uninstalled"],
  reset: ["Resetting", "Reset"],
}

// Shows the end of a task. The rules have a fixed order.
function settle(t: TaskView, action: Action): void {
  running.value = null
  const title = `/${name.value}`
  if (t.state === "lost") {
    note.value = { title, at: new Date(), text: LOST, kind: "error" }
  } else if (t.state === "done") {
    if (action === "uninstall") {
      void router.push({ name: "skills" })
      return
    }
    toast(`${verbs[action][1]} /${name.value}. It loads in new sessions.`, "ok")
    void load()
  } else if (t.code === "need_force") {
    note.value = { title, at: new Date(), text: t.text, kind: "warn" }
    forceOffer.value = "update"
  } else if (t.code === "need_override") {
    manual.value = { findings: t.text }
  } else if (action === "uninstall" && /(^|\n)Cancelled\.(\n|$)/.test(t.text)) {
    toast("Not removed.", "info")
  } else {
    note.value = { title, at: new Date(), text: t.text, kind: "error" }
  }
}

const stops = new Set<() => void>()
// True after the page unmounts. An await can end after that.
let gone = false

// Follows one task. Each end of the task goes to settle.
function track(id: string, action: Action): void {
  running.value = { id, action, waiting: false }
  note.value = null
  forceOffer.value = null
  manual.value = null
  let stop: (() => void) | null = null
  let ended = false
  stop = followTask(id, (t) => {
    if (ended) return
    if (t.state === "running") {
      if (running.value && running.value.id === id) running.value.waiting = t.waiting
      return
    }
    ended = true
    if (stop) {
      stop()
      stops.delete(stop)
    }
    settle(t, action)
  })
  // A follow that starts after the page is gone stops at once.
  if (ended || gone) stop()
  else stops.add(stop)
}

onBeforeUnmount(() => {
  gone = true
  stops.forEach((fn) => fn())
  stops.clear()
})

async function start(action: Action, force?: boolean): Promise<void> {
  let id = ""
  try {
    const path = `/api/skills/${encodeURIComponent(name.value)}/${action}`
    const r = force
      ? await api<{ task_id: string }>("POST", path, { force: true })
      : await api<{ task_id: string }>("POST", path)
    id = r.task_id
  } catch (e) {
    toast(friendlyError(e), "error")
    return
  }
  toast(`${verbs[action][0]} /${name.value}…`, "info")
  track(id, action)
}

// Posts one skill action. Reset and uninstall ask a confirm first.
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
      title: `Remove /${n}?`,
      body: "A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Remove",
      danger: true,
    })
    if (!ok) return
  }
  await start(action)
}

async function updateAnyway(): Promise<void> {
  const ok = await confirm({
    title: `Update /${name.value} anyway?`,
    body: "The skill guard rated it caution. A confirm card follows in ⏳ Waiting.",
    confirmLabel: "Update anyway",
    danger: true,
  })
  if (!ok) return
  await start("update", true)
}

async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    toast("Copied.")
  } catch (e) {
    toast(friendlyError(e), "error")
  }
}

const { stale, updatedAt, refresh } = useLiveData(load, { events: ["task"], filter: isEnd })
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
      @action="load"
    />
    <template v-else-if="view">
      <div class="page-h">
        <h1 class="t-20">/{{ view.skill.name }}</h1>
        <span class="chip chip-plain">{{ view.skill.trust }}</span>
        <span v-if="view.override" class="chip chip-warn">Owner override</span>
        <span v-if="busy" class="chip chip-info">Running…</span>
        <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
        <button
          v-if="view.skill.actions.includes('update') && !view.skill.pinned"
          class="btn btn-secondary"
          type="button"
          :disabled="busy"
          @click="skillAction('update')"
        >
          Update
        </button>
        <button
          v-if="view.skill.actions.includes('reset')"
          class="btn btn-secondary"
          type="button"
          :disabled="busy"
          @click="skillAction('reset')"
        >
          Reset
        </button>
        <button
          v-if="view.skill.actions.includes('uninstall')"
          class="btn btn-danger-soft"
          type="button"
          :disabled="busy"
          @click="skillAction('uninstall')"
        >
          Uninstall
        </button>
      </div>
      <p class="c-2">{{ view.skill.description }}</p>
      <p v-if="view.skill.required_env.length" class="t-12 c-3">
        Needs: {{ view.skill.required_env.join(", ") }}
      </p>
      <p class="t-12">
        Run it in chat: <code>/{{ view.skill.name }}</code>
      </p>
      <ResultNote :note="note" @close="note = null" />
      <div v-if="forceOffer" class="li">
        <button class="btn btn-secondary" type="button" :disabled="busy" @click="updateAnyway">
          Update anyway…
        </button>
      </div>
      <div v-if="running?.waiting" class="li">
        <span class="grow">Answer the card in ⏳ Waiting.</span>
        <button class="btn btn-secondary btn-sm" type="button" @click="ui.trayOpen = true">
          Open Waiting
        </button>
      </div>

      <div class="grid-2">
        <div class="card">
          <div class="card-h"><h3>SKILL.md</h3></div>
          <div class="card-b">
            <div class="md" v-html="html" />
            <template v-if="view.files.length">
              <h4 class="t-12 c-3">Files</h4>
              <div v-for="p in view.files" :key="p">
                <button class="link mono" type="button" @click="openFile(p, null)">{{ p }}</button>
              </div>
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
        <p v-if="view.unscanned.length" class="t-12 c-3">
          Not scanned: {{ view.unscanned.join(", ") }}
        </p>
        <StateBlock v-if="view.findings.length === 0" state="empty" text="No findings" />
        <DataTable
          v-else
          :columns="findingColumns"
          :rows="findingRows as unknown as Record<string, unknown>[]"
          row-key="key"
        >
          <template #cell-pattern="{ row }"
            ><span class="mono">{{ f(row).pattern }}</span></template
          >
          <template #cell-where="{ row }">
            <button
              v-if="canOpen(f(row).file)"
              class="link mono"
              type="button"
              @click="openFile(f(row).file, f(row).line)"
            >
              {{ f(row).file }}:{{ f(row).line }}
            </button>
            <span v-else class="mono">{{ f(row).file }}:{{ f(row).line }}</span>
          </template>
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

      <SidePanel :open="file !== null" :title="file?.path ?? ''" @close="file = null">
        <FileViewer v-if="file" :skill="view.skill.name" :path="file.path" :line="file.line" />
      </SidePanel>
      <SidePanel :open="manual !== null" title="Update from the terminal" @close="manual = null">
        <template v-if="manual">
          <div class="code-block">tether skills update {{ view.skill.name }}</div>
          <p>The terminal asks you to type the name of the skill.</p>
          <button
            v-if="isSecure()"
            class="btn btn-secondary"
            type="button"
            @click="copy(`tether skills update ${view.skill.name}`)"
          >
            Copy command
          </button>
          <p v-else class="c-3">Needs HTTPS or localhost</p>
          <div v-if="manual.findings" class="code-block">{{ manual.findings }}</div>
        </template>
      </SidePanel>
    </template>
  </div>
</template>
