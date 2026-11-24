<script lang="ts">
// The ids of the task results that the owner closed. A closed result does not come back.
const dismissed = new Set<string>()
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api } from "../api/client"
import type { CatalogRow, SkillRow, TaskView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import ResultNote from "../components/ResultNote.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import Tabs from "../components/Tabs.vue"
import { friendlyError } from "../lib/errors"
import { followTask, useLiveData } from "../lib/live"
import type { ResultNoteData } from "../lib/command"
import { confirm, isSecure, toast, ui } from "../stores/ui"

type Action = "install" | "update" | "uninstall" | "reset"
type Running = { id: string; name: string; action: Action | "check"; waiting: boolean }
type Manual = { name: string; action: "install" | "update"; findings: string }

const stops = new Set<() => void>()
const PAGE_SIZE = 20
const LOST = "The gateway restarted. The result of the task is lost."

const route = useRoute()
const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const rows = ref<SkillRow[]>([])
const catalog = ref<CatalogRow[]>([])
const catalogState = ref<"idle" | "loading" | "ready" | "error">("idle")
const catalogError = ref("")
const search = ref("")
const installedQuery = ref("")
const trust = ref<"all" | "trusted" | "community" | "local">("all")
const inst = ref<"all" | "yes" | "no">("all")
const page = ref(0)
const note = ref<ResultNoteData | null>(null)
const noteId = ref("")
const running = ref<Running | null>(null)
const forceOffer = ref<{ name: string; action: "install" | "update" } | null>(null)
const manual = ref<Manual | null>(null)
const busy = computed(() => running.value !== null)
let restored = false

const tab = computed(() => (route.query.tab === "catalog" ? "catalog" : "installed"))
const tabList = computed(() => [
  { id: "installed", label: `Installed (${rows.value.length})` },
  { id: "catalog", label: "Catalog" },
])
const tabModel = computed({
  get: () => tab.value,
  set: (v: string) => setTab(v === "catalog" ? "catalog" : "installed"),
})

const columns = [
  { key: "name", label: "Skill" },
  { key: "trust", label: "Trust" },
  { key: "source", label: "Source" },
  { key: "env", label: "Env" },
  { key: "status", label: "Status" },
  { key: "actions", label: "" },
]
const catalogColumns = [
  { key: "name", label: "Skill" },
  { key: "url", label: "Source" },
  { key: "checks", label: "Trust" },
  { key: "actions", label: "" },
]

const s = (x: unknown) => x as SkillRow
const c = (x: unknown) => x as CatalogRow

const shownRows = computed(() => {
  const q = installedQuery.value.trim().toLowerCase()
  if (!q) return rows.value
  return rows.value.filter(
    (r) => r.name.toLowerCase().includes(q) || r.description.toLowerCase().includes(q),
  )
})

const LOCAL_TRUST = ["builtin", "agent-created", "external"]
const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return catalog.value.filter((r) => {
    if (q && !r.name.toLowerCase().includes(q) && !r.description.toLowerCase().includes(q))
      return false
    if (trust.value === "local" && !LOCAL_TRUST.includes(r.source_trust)) return false
    if (
      (trust.value === "trusted" || trust.value === "community") &&
      r.source_trust !== trust.value
    )
      return false
    if (inst.value === "yes" && !r.installed) return false
    if (inst.value === "no" && r.installed) return false
    return true
  })
})
const pageCount = computed(() => Math.max(1, Math.ceil(filtered.value.length / PAGE_SIZE)))
const pageRows = computed(() =>
  filtered.value.slice(page.value * PAGE_SIZE, (page.value + 1) * PAGE_SIZE),
)
watch([search, trust, inst], () => {
  page.value = 0
})

function trustClass(t: string): string {
  if (t === "trusted") return "chip-ok"
  if (t === "community") return "chip-warn"
  if (t === "agent-created") return "chip-info"
  return "chip-plain"
}

// Returns the status chips of one skill as { text, cls } pairs.
function chips(r: SkillRow): { text: string; cls: string }[] {
  const out: { text: string; cls: string }[] = []
  if (r.update || r.check.includes("update available"))
    out.push({ text: "update available", cls: "chip-info" })
  if (r.pinned || r.check.includes("pinned")) out.push({ text: "pinned", cls: "chip-plain" })
  if (r.check.includes("modified locally")) out.push({ text: "modified locally", cls: "chip-warn" })
  if (r.check.includes("cannot check")) out.push({ text: "cannot check", cls: "chip-plain" })
  if (r.check.includes("source changed")) out.push({ text: "source changed", cls: "chip-warn" })
  return out
}

function canUpdate(r: SkillRow): boolean {
  return (
    r.actions.includes("update") && !r.pinned && (r.update || r.check.includes("update available"))
  )
}

async function loadRows(): Promise<void> {
  try {
    rows.value = await api<SkillRow[]>("GET", "/api/skills")
    state.value = "ready"
  } catch (e) {
    // Keep the data on screen after a failed reload. Only the first load shows the error state.
    if (state.value === "ready") {
      toast(friendlyError(e), "error")
      return
    }
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

async function loadCatalog(): Promise<void> {
  catalogState.value = "loading"
  try {
    catalog.value = await api<CatalogRow[]>("GET", "/api/skills/catalog")
    catalogState.value = "ready"
  } catch (e) {
    catalogError.value = friendlyError(e)
    catalogState.value = "error"
  }
}

// Reads the installed list, then the catalog when it was read once. The first call also
// shows the newest skills task again.
async function load(): Promise<void> {
  await loadRows()
  if (catalogState.value === "ready" || (catalogState.value === "idle" && tab.value === "catalog"))
    await loadCatalog()
  if (!restored) {
    restored = true
    await restore()
  }
}

function setTab(t: "installed" | "catalog"): void {
  void router.replace({ query: t === "catalog" ? { tab: "catalog" } : {} })
  if (t === "catalog" && catalogState.value === "idle") void loadCatalog()
}

// Returns true for the end of a task of the kind skills.
function isEnd(_name: string, data: unknown): boolean {
  const t = data as Partial<TaskView> | null
  return !!t && t.kind === "skills" && (t.state === "done" || t.state === "error")
}

// Returns the chip class of a guard verdict.
function verdictClass(v: string): string {
  if (v === "safe") return "chip-ok"
  if (v === "dangerous") return "chip-bad"
  return "chip-warn"
}

const verbs: Record<Action, [string, string]> = {
  install: ["Installing", "Installed"],
  update: ["Updating", "Updated"],
  uninstall: ["Uninstalling", "Uninstalled"],
  reset: ["Resetting", "Reset"],
}

// Shows the end of a task. A live end also toasts a done task.
function settle(t: TaskView, name: string, action: Action | "check" | null, live: boolean): void {
  running.value = null
  noteId.value = t.id
  const isCheck = action === "check" || (action === null && name === "")
  const title = isCheck ? "Check for updates" : `/${name}`
  const a: "install" | "update" =
    action === "install" || action === "update"
      ? action
      : rows.value.some((r) => r.name === name)
        ? "update"
        : "install"
  const doneText = isCheck
    ? "Checked the skill sources."
    : `${verbs[action ?? a][1]} /${name}. It loads in new sessions.`
  if (t.state === "lost") {
    note.value = { title, at: new Date(), text: LOST, kind: "error" }
  } else if (t.state === "done") {
    note.value = { title, at: new Date(), text: t.text || doneText, kind: "info" }
    if (live) toast(doneText, "ok")
  } else if (t.code === "need_force") {
    note.value = { title, at: new Date(), text: t.text, kind: "warn" }
    forceOffer.value = { name, action: a }
  } else if (t.code === "need_override") {
    manual.value = { name, action: a, findings: t.text }
  } else if (action === "uninstall" && /(^|\n)Cancelled\.(\n|$)/.test(t.text)) {
    toast("Not removed.", "info")
  } else {
    note.value = { title, at: new Date(), text: t.text, kind: "error" }
  }
}

// Follows one task. Each end of the task goes to settle.
function track(id: string, name: string, action: Action | "check"): void {
  running.value = { id, name, action, waiting: false }
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
    settle(t, name, action, true)
  })
  if (ended) stop()
  else stops.add(stop)
}

// Reads the newest skills task. A running task is followed. A finished task shows again.
async function restore(): Promise<void> {
  let list: TaskView[]
  try {
    list = await api<TaskView[]>("GET", "/api/tasks?kind=skills")
  } catch {
    return
  }
  const t = list[0]
  if (!t) return
  if (t.state === "running") {
    track(t.id, t.subject, "update")
    if (running.value) running.value.waiting = t.waiting
  } else if (!dismissed.has(t.id)) {
    settle(t, t.subject, null, false)
  }
}

onBeforeUnmount(() => {
  stops.forEach((f) => f())
  stops.clear()
})

function closeNote(): void {
  dismissed.add(noteId.value)
  note.value = null
}

async function start(name: string, action: Action, force?: boolean): Promise<void> {
  let id = ""
  try {
    const path = `/api/skills/${name}/${action}`
    const r = force
      ? await api<{ task_id: string }>("POST", path, { force: true })
      : await api<{ task_id: string }>("POST", path)
    id = r.task_id
  } catch (e) {
    toast(friendlyError(e), "error")
    return
  }
  toast(`${verbs[action][0]} /${name}…`, "info")
  track(id, name, action)
}

// Posts one skill action. Reset and uninstall ask a confirm first.
async function skillAction(name: string, action: Action): Promise<void> {
  if (action === "reset") {
    const ok = await confirm({
      title: `Reset /${name}?`,
      body: "The bundled version replaces your local changes.",
      confirmLabel: "Reset",
    })
    if (!ok) return
  }
  if (action === "uninstall") {
    const ok = await confirm({
      title: `Remove /${name}?`,
      body: "A confirm card follows in ⏳ Waiting.",
      confirmLabel: "Remove",
      danger: true,
    })
    if (!ok) return
  }
  await start(name, action)
}

async function installAnyway(): Promise<void> {
  const o = forceOffer.value
  if (!o) return
  const verb = o.action === "install" ? "Install" : "Update"
  const same = rows.value.find((r) => r.name === o.name && r.source !== "")
  const ok = await confirm({
    title: `${verb} /${o.name} anyway?`,
    body:
      "The skill guard rated it caution. A confirm card follows in ⏳ Waiting." +
      (same ? ` It replaces the installed /${o.name} from ${same.source}.` : ""),
    confirmLabel: `${verb} anyway`,
    danger: true,
  })
  if (!ok) return
  await start(o.name, o.action, true)
}

async function check(): Promise<void> {
  let id = ""
  try {
    id = (await api<{ task_id: string }>("POST", "/api/skills/check")).task_id
  } catch (e) {
    toast(friendlyError(e), "error")
    return
  }
  toast("Checking the skill sources…", "info")
  track(id, "", "check")
}

// A dangerous skill does not install from the dashboard. The terminal command shows instead.
function install(r: CatalogRow): void {
  if (r.verdict === "dangerous") manual.value = { name: r.name, action: "install", findings: "" }
  else void skillAction(r.name, "install")
}

function terminalCommand(m: Manual): string {
  return `tether skills ${m.action} ${m.name}`
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
    <div class="page-h">
      <h1 class="t-20">Skills</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <button class="btn btn-secondary" type="button" :disabled="busy" @click="check">
        Check for updates
      </button>
    </div>
    <ResultNote :note="note" @close="closeNote" />
    <div v-if="forceOffer" class="li">
      <button class="btn btn-secondary" type="button" :disabled="busy" @click="installAnyway">
        {{ forceOffer.action === "install" ? "Install anyway…" : "Update anyway…" }}
      </button>
    </div>
    <div v-if="running?.waiting" class="li">
      <span class="grow">Answer the card in ⏳ Waiting.</span>
      <button class="btn btn-secondary btn-sm" type="button" @click="ui.trayOpen = true">
        Open Waiting
      </button>
    </div>
    <Tabs :tabs="tabList" v-model="tabModel" />

    <template v-if="tab === 'installed'">
      <StateBlock v-if="state === 'loading'" state="loading" />
      <StateBlock
        v-else-if="state === 'error'"
        state="error"
        :text="errorText"
        action="Retry"
        @action="load"
      />
      <StateBlock
        v-else-if="rows.length === 0"
        state="empty"
        text="No skills installed."
        action="Open the catalog"
        @action="setTab('catalog')"
      />
      <template v-else>
        <div class="search">
          <input
            v-model="installedQuery"
            class="input"
            type="search"
            placeholder="Search the installed skills"
            aria-label="Search the installed skills"
          />
        </div>
        <StateBlock v-if="shownRows.length === 0" state="empty" text="No skill matches" />
        <DataTable
          v-else
          :columns="columns"
          :rows="shownRows as unknown as Record<string, unknown>[]"
          row-key="name"
        >
          <template #cell-name="{ row }">
            <router-link :to="{ name: 'skill', params: { name: s(row).name } }"
              >/{{ s(row).name }}</router-link
            >
            <div class="c-2 t-12">{{ s(row).description }}</div>
          </template>
          <template #cell-trust="{ row }"
            ><span class="chip" :class="trustClass(s(row).trust)">{{
              s(row).trust
            }}</span></template
          >
          <template #cell-source="{ row }"
            ><span class="mono">{{ s(row).source || "—" }}</span></template
          >
          <template #cell-env="{ row }">{{
            s(row).required_env.length ? s(row).required_env.join(", ") : "—"
          }}</template>
          <template #cell-status="{ row }">
            <span v-if="running && running.name === s(row).name" class="chip chip-info"
              >Running…</span
            >
            <template v-if="chips(s(row)).length">
              <span v-for="ch in chips(s(row))" :key="ch.text" class="chip" :class="ch.cls">{{
                ch.text
              }}</span>
            </template>
            <span v-else-if="!(running && running.name === s(row).name)" class="c-3">—</span>
          </template>
          <template #cell-actions="{ row }">
            <button
              v-if="canUpdate(s(row))"
              class="btn btn-secondary btn-sm"
              type="button"
              :disabled="busy"
              @click.stop="skillAction(s(row).name, 'update')"
            >
              Update
            </button>
            <button
              v-if="s(row).actions.includes('reset')"
              class="btn btn-secondary btn-sm"
              type="button"
              :disabled="busy"
              @click.stop="skillAction(s(row).name, 'reset')"
            >
              Reset
            </button>
            <button
              v-if="s(row).actions.includes('uninstall')"
              class="btn btn-danger-soft btn-sm"
              type="button"
              :disabled="busy"
              @click.stop="skillAction(s(row).name, 'uninstall')"
            >
              Uninstall
            </button>
          </template>
        </DataTable>
      </template>
    </template>

    <template v-else>
      <StateBlock v-if="catalogState === 'idle' || catalogState === 'loading'" state="loading" />
      <StateBlock
        v-else-if="catalogState === 'error'"
        state="error"
        :text="catalogError"
        action="Retry"
        @action="loadCatalog"
      />
      <template v-else>
        <div class="search">
          <input
            v-model="search"
            class="input"
            type="search"
            :placeholder="`Search ${catalog.length} skills`"
            :aria-label="`Search ${catalog.length} skills`"
          />
        </div>
        <div class="li">
          <select v-model="trust" class="input" aria-label="Trust">
            <option value="all">All</option>
            <option value="trusted">Trusted</option>
            <option value="community">Community</option>
            <option value="local">Local</option>
          </select>
          <select v-model="inst" class="input" aria-label="Installed">
            <option value="all">All</option>
            <option value="yes">Installed</option>
            <option value="no">Not installed</option>
          </select>
        </div>
        <StateBlock v-if="filtered.length === 0" state="empty" text="No skill matches" />
        <DataTable
          v-else
          :columns="catalogColumns"
          :rows="pageRows as unknown as Record<string, unknown>[]"
          row-key="name"
        >
          <template #cell-name="{ row }">
            <b>/{{ c(row).name }}</b>
            <div class="c-2 t-12">{{ c(row).description }}</div>
          </template>
          <template #cell-url="{ row }"
            ><span class="mono">{{ c(row).url }}</span></template
          >
          <template #cell-checks="{ row }">
            <span v-if="c(row).verdict" class="chip" :class="verdictClass(c(row).verdict)">{{
              c(row).verdict
            }}</span>
            <span
              v-if="c(row).source_trust"
              class="chip"
              :class="trustClass(c(row).source_trust)"
              >{{ c(row).source_trust }}</span
            >
            <span v-if="!c(row).verdict && !c(row).source_trust" class="c-3">—</span>
          </template>
          <template #cell-actions="{ row }">
            <span v-if="c(row).installed" class="chip chip-plain">installed</span>
            <button
              v-else
              class="btn btn-primary btn-sm"
              type="button"
              :disabled="busy"
              @click.stop="install(c(row))"
            >
              Install
            </button>
          </template>
        </DataTable>
        <div v-if="filtered.length > PAGE_SIZE" class="pager">
          <span class="grow">Page {{ page + 1 }} of {{ pageCount }}</span>
          <button class="btn btn-ghost" type="button" :disabled="page === 0" @click="page--">
            ◀ Prev
          </button>
          <button
            class="btn btn-ghost"
            type="button"
            :disabled="page >= pageCount - 1"
            @click="page++"
          >
            Next ▶
          </button>
        </div>
      </template>
    </template>

    <SidePanel :open="manual !== null" title="Install from the terminal" @close="manual = null">
      <template v-if="manual">
        <p>
          The skill guard marked /{{ manual.name }} as dangerous. The dashboard does not
          {{ manual.action }} it.
        </p>
        <div class="code-block">{{ terminalCommand(manual) }}</div>
        <p>The terminal asks you to type the name of the skill.</p>
        <button
          v-if="isSecure()"
          class="btn btn-secondary"
          type="button"
          @click="copy(terminalCommand(manual))"
        >
          Copy command
        </button>
        <p v-else class="c-3">Needs HTTPS or localhost</p>
        <div v-if="manual.findings" class="code-block">{{ manual.findings }}</div>
      </template>
    </SidePanel>

    <p class="pill-note">
      Each skill is a /command in Chat. A new skill loads in new sessions. The agent itself can
      never install a skill.
    </p>
  </div>
</template>
