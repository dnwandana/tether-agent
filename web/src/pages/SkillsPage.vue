<script lang="ts">
// The ids of the task results that the owner closed. A closed result does not come back.
const dismissed = new Set<string>()
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api } from "../api/client"
import type { CatalogRow, SkillRow, TaskView } from "../api/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import ResultNote from "../components/ResultNote.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import Tabs from "../components/Tabs.vue"
import { friendlyError } from "../lib/errors"
import { followTask, useLiveData } from "../lib/live"
import { useRoutePage } from "../lib/routePage"
import { TONE, chips, type Tone } from "../lib/skills"
import type { Column } from "../lib/table"
import type { ResultNoteData } from "../lib/command"
import { confirm, isSecure, rememberListQuery, toast, ui } from "../stores/ui"

type Action = "install" | "update" | "uninstall" | "reset"
type Running = { id: string; name: string; action: Action | "check"; waiting: boolean }
type Manual = { name: string; action: "install" | "update"; findings: string }

const stops = new Set<() => void>()
const LOST = "The gateway restarted. The result of the task is lost."

const route = useRoute()
const router = useRouter()
const { page, setPage } = useRoutePage("skills")
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

const columns: Column[] = [
  { key: "name", label: "Skill", type: "wrap" },
  { key: "trust", label: "Trust", type: "badge" },
  { key: "source", label: "Source", type: "text", optional: true },
  { key: "env", label: "Env", type: "text", optional: true },
  { key: "status", label: "Status", type: "block" },
  { key: "actions", label: "", type: "act", width: "16rem" },
]
const catalogColumns: Column[] = [
  { key: "name", label: "Skill", type: "wrap" },
  { key: "url", label: "Source", type: "text" },
  { key: "checks", label: "Trust", type: "block" },
  { key: "actions", label: "", type: "act", width: "6rem" },
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
// A new filter or search text shows its first page.
watch([search, trust, inst, installedQuery], () => setPage(1))
// The back link of the skill detail returns to this query.
watch(
  () => route.query,
  (q) => {
    if (route.name === "skills") rememberListQuery("skills", q)
  },
  { immediate: true },
)

// Returns the tone of a trust label.
function trustTone(t: string): Tone {
  if (t === "trusted") return "ok"
  if (t === "community") return "warn"
  if (t === "agent-created") return "info"
  return "plain"
}

// Returns the trust label that the page shows. The loader cannot tell an agent write from a
// copy by hand, so "agent-created" shows as "local", like the catalog filter.
function trustLabel(t: string): string {
  return t === "agent-created" ? "local" : t
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

// Returns the tone of a guard verdict.
function verdictTone(v: string): Tone {
  if (v === "safe") return "ok"
  if (v === "dangerous") return "bad"
  return "warn"
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
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">Skills</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <Button variant="outline" :disabled="busy" @click="check">Check for updates</Button>
    </div>
    <ResultNote :note="note" @close="closeNote" />
    <div v-if="forceOffer" class="flex items-center gap-2">
      <Button variant="outline" :disabled="busy" @click="installAnyway">
        {{ forceOffer.action === "install" ? "Install anyway…" : "Update anyway…" }}
      </Button>
    </div>
    <div v-if="running?.waiting" class="flex items-center gap-2">
      <span class="flex-1">Answer the card in ⏳ Waiting.</span>
      <Button variant="outline" size="sm" @click="ui.trayOpen = true">Open Waiting</Button>
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
        <Input
          v-model="installedQuery"
          type="search"
          placeholder="Search the installed skills"
          aria-label="Search the installed skills"
        />
        <StateBlock v-if="shownRows.length === 0" state="empty" text="No skill matches" />
        <DataTable
          v-else
          :columns="columns"
          :rows="shownRows as unknown as Record<string, unknown>[]"
          row-key="name"
          page-key="skills-installed"
          page-label="Installed skills"
          :page="page"
          @update:page="setPage"
        >
          <template #cell-name="{ row }">
            <!-- One box: a phone card puts the children of a cell in one row. -->
            <div class="min-w-0">
              <router-link
                class="font-medium underline-offset-4 hover:underline"
                :to="{ name: 'skill', params: { name: s(row).name } }"
                >/{{ s(row).name }}</router-link
              >
              <div
                class="line-clamp-2 whitespace-normal text-xs text-muted-foreground"
                :title="s(row).description"
              >
                {{ s(row).description }}
              </div>
            </div>
          </template>
          <template #cell-trust="{ row }">
            <Badge
              variant="outline"
              :class="TONE[trustTone(s(row).trust)]"
              :data-tone="trustTone(s(row).trust)"
              >{{ trustLabel(s(row).trust) }}</Badge
            >
          </template>
          <template #cell-source="{ row }"
            ><span class="font-mono text-xs">{{ s(row).source || "—" }}</span></template
          >
          <template #cell-env="{ row }">{{
            s(row).required_env.length ? s(row).required_env.join(", ") : "—"
          }}</template>
          <template #cell-status="{ row }">
            <Badge
              v-if="running && running.name === s(row).name"
              variant="outline"
              :class="TONE.info"
              data-tone="info"
              >Running…</Badge
            >
            <template v-if="chips(s(row)).length">
              <Badge
                v-for="ch in chips(s(row))"
                :key="ch.text"
                variant="outline"
                :class="TONE[ch.tone]"
                :data-tone="ch.tone"
                >{{ ch.text }}</Badge
              >
            </template>
            <span
              v-else-if="!(running && running.name === s(row).name)"
              class="text-muted-foreground"
              >—</span
            >
          </template>
          <template #cell-actions="{ row }">
            <Button
              v-if="canUpdate(s(row))"
              variant="outline"
              size="sm"
              :disabled="busy"
              @click.stop="skillAction(s(row).name, 'update')"
            >
              Update
            </Button>
            <Button
              v-if="s(row).actions.includes('reset')"
              variant="outline"
              size="sm"
              :disabled="busy"
              @click.stop="skillAction(s(row).name, 'reset')"
            >
              Reset
            </Button>
            <Button
              v-if="s(row).actions.includes('uninstall')"
              variant="destructive"
              size="sm"
              :disabled="busy"
              @click.stop="skillAction(s(row).name, 'uninstall')"
            >
              Uninstall
            </Button>
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
        <Input
          v-model="search"
          type="search"
          :placeholder="`Search ${catalog.length} skills`"
          :aria-label="`Search ${catalog.length} skills`"
        />
        <div class="flex flex-wrap gap-2">
          <NativeSelect v-model="trust" aria-label="Trust">
            <NativeSelectOption value="all">All</NativeSelectOption>
            <NativeSelectOption value="trusted">Trusted</NativeSelectOption>
            <NativeSelectOption value="community">Community</NativeSelectOption>
            <NativeSelectOption value="local">Local</NativeSelectOption>
          </NativeSelect>
          <NativeSelect v-model="inst" aria-label="Installed">
            <NativeSelectOption value="all">All</NativeSelectOption>
            <NativeSelectOption value="yes">Installed</NativeSelectOption>
            <NativeSelectOption value="no">Not installed</NativeSelectOption>
          </NativeSelect>
        </div>
        <StateBlock v-if="filtered.length === 0" state="empty" text="No skill matches" />
        <DataTable
          v-else
          :columns="catalogColumns"
          :rows="filtered as unknown as Record<string, unknown>[]"
          row-key="name"
          page-key="skills-catalog"
          page-label="Catalog"
          :page="page"
          @update:page="setPage"
        >
          <template #cell-name="{ row }">
            <!-- One box: a phone card puts the children of a cell in one row. -->
            <div class="min-w-0">
              <b>/{{ c(row).name }}</b>
              <div
                class="line-clamp-2 whitespace-normal text-xs text-muted-foreground"
                :title="c(row).description"
              >
                {{ c(row).description }}
              </div>
            </div>
          </template>
          <template #cell-url="{ row }"
            ><span class="font-mono text-xs">{{ c(row).url }}</span></template
          >
          <template #cell-checks="{ row }">
            <Badge
              v-if="c(row).verdict"
              variant="outline"
              :class="TONE[verdictTone(c(row).verdict)]"
              :data-tone="verdictTone(c(row).verdict)"
              >{{ c(row).verdict }}</Badge
            >
            <Badge
              v-if="c(row).source_trust"
              variant="outline"
              :class="TONE[trustTone(c(row).source_trust)]"
              :data-tone="trustTone(c(row).source_trust)"
              >{{ trustLabel(c(row).source_trust) }}</Badge
            >
            <span v-if="!c(row).verdict && !c(row).source_trust" class="text-muted-foreground"
              >—</span
            >
          </template>
          <template #cell-actions="{ row }">
            <Badge v-if="c(row).installed" variant="secondary">installed</Badge>
            <Button v-else size="sm" :disabled="busy" @click.stop="install(c(row))">
              Install
            </Button>
          </template>
        </DataTable>
      </template>
    </template>

    <SidePanel :open="manual !== null" title="Install from the terminal" @close="manual = null">
      <template v-if="manual">
        <p>
          The skill guard marked /{{ manual.name }} as dangerous. The dashboard does not
          {{ manual.action }} it.
        </p>
        <pre
          class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
          >{{ terminalCommand(manual) }}</pre>
        <p>The terminal asks you to type the name of the skill.</p>
        <Button v-if="isSecure()" variant="outline" @click="copy(terminalCommand(manual))">
          Copy command
        </Button>
        <p v-else class="text-sm text-muted-foreground">Needs HTTPS or localhost</p>
        <pre
          v-if="manual.findings"
          class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
          >{{ manual.findings }}</pre>
      </template>
    </SidePanel>

    <p class="text-sm text-muted-foreground">
      Each skill is a /command in Chat. A new skill loads in new sessions. The agent itself can
      never install a skill.
    </p>
  </div>
</template>
