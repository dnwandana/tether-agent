<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api, ApiError } from "../api/client"
import type { CatalogRow, SkillRow, TaskView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { confirm, isSecure, toast } from "../stores/ui"

type Action = "install" | "update" | "uninstall" | "reset"

const route = useRoute()
const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const rows = ref<SkillRow[]>([])
const catalog = ref<CatalogRow[]>([])
const catalogState = ref<"idle" | "loading" | "ready" | "error">("idle")
const catalogError = ref("")
const search = ref("")
const manual = ref<CatalogRow | null>(null)
let alive = true

const tab = computed(() => (route.query.tab === "catalog" ? "catalog" : "installed"))

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

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return catalog.value
  return catalog.value.filter(
    (r) => r.name.toLowerCase().includes(q) || r.description.toLowerCase().includes(q),
  )
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

async function load(): Promise<void> {
  try {
    rows.value = await api<SkillRow[]>("GET", "/api/skills")
    state.value = "ready"
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

async function loadCatalog(): Promise<void> {
  catalogState.value = "loading"
  try {
    catalog.value = await api<CatalogRow[]>("GET", "/api/skills/catalog")
    catalogState.value = "ready"
  } catch (e) {
    catalogError.value = e instanceof Error ? e.message : String(e)
    catalogState.value = "error"
  }
}

function setTab(t: "installed" | "catalog"): void {
  void router.replace({ query: t === "catalog" ? { tab: "catalog" } : {} })
  if (t === "catalog" && catalogState.value === "idle") void loadCatalog()
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// Returns the chip class of a guard verdict.
function verdictClass(v: string): string {
  if (v === "safe") return "chip-ok"
  if (v === "dangerous") return "chip-bad"
  return "chip-warn"
}

// Polls a task every 2 s for 2 min at most. It returns the finished view, or null on a timeout.
async function pollTask(id: string): Promise<TaskView | null> {
  for (let i = 0; i < 60; i++) {
    await sleep(2000)
    if (!alive) return null
    try {
      const t = await api<TaskView>("GET", `/api/tasks/${id}`)
      if (t.state === "done" || t.state === "error") return t
    } catch {
      // Keep the poll. The next try can work.
    }
  }
  return null
}

// Follows one task. An error shows its text. A done task loads the lists again.
async function follow(id: string, doneText: string): Promise<void> {
  const t = await pollTask(id)
  if (!t) return
  if (t.state === "error") {
    toast(t.text, "error")
    return
  }
  await load()
  if (catalogState.value === "ready") await loadCatalog()
  toast(doneText)
}

const verbs: Record<Action, [string, string]> = {
  install: ["Installing", "Installed"],
  update: ["Updating", "Updated"],
  uninstall: ["Uninstalling", "Uninstalled"],
  reset: ["Resetting", "Reset"],
}

// Posts one skill action and follows it with the task rule.
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
      title: `Uninstall /${name}?`,
      body: "The skill folder is removed. Cron jobs that use it lose it.",
      confirmLabel: "Uninstall",
      danger: true,
    })
    if (!ok) return
  }
  let id = ""
  try {
    id = (await api<{ task_id: string }>("POST", `/api/skills/${name}/${action}`)).task_id
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
    return
  }
  toast(`${verbs[action][0]} /${name}…`, "info")
  await follow(id, `${verbs[action][1]} /${name}. It loads in new sessions.`)
}

async function check(): Promise<void> {
  let id = ""
  try {
    id = (await api<{ task_id: string }>("POST", "/api/skills/check")).task_id
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
    return
  }
  toast("Checking the skill sources…", "info")
  await follow(id, "Checked the skill sources.")
}

// A dangerous skill does not install from the dashboard. The terminal command shows instead.
function install(r: CatalogRow): void {
  if (r.verdict === "dangerous") manual.value = r
  else void skillAction(r.name, "install")
}

function installCommand(r: CatalogRow): string {
  return `tether skills install ${r.url}`
}

async function copy(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text)
    toast("Copied.")
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}

onMounted(() => {
  void load()
  if (tab.value === "catalog") void loadCatalog()
})
onBeforeUnmount(() => {
  alive = false
})
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Skills</h1>
      <button class="btn btn-secondary" type="button" @click="check">Check for updates</button>
    </div>
    <div class="tabs">
      <button type="button" :class="{ 'is-on': tab === 'installed' }" @click="setTab('installed')">
        Installed <span class="chip chip-plain">{{ rows.length }}</span>
      </button>
      <button type="button" :class="{ 'is-on': tab === 'catalog' }" @click="setTab('catalog')">
        Catalog
      </button>
    </div>

    <template v-if="tab === 'installed'">
      <StateBlock v-if="state === 'loading'" state="loading" />
      <StateBlock
        v-else-if="state === 'error'"
        state="error"
        :text="errorText"
        action="Retry"
        @retry="load"
      />
      <StateBlock v-else-if="rows.length === 0" state="empty" text="No skills installed" />
      <DataTable
        v-else
        :columns="columns"
        :rows="rows as unknown as Record<string, unknown>[]"
        row-key="name"
      >
        <template #cell-name="{ row }">
          <router-link :to="{ name: 'skill', params: { name: s(row).name } }"
            >/{{ s(row).name }}</router-link
          >
          <div class="c-2 t-12">{{ s(row).description }}</div>
        </template>
        <template #cell-trust="{ row }"
          ><span class="chip" :class="trustClass(s(row).trust)">{{ s(row).trust }}</span></template
        >
        <template #cell-source="{ row }"
          ><span class="mono">{{ s(row).source || "—" }}</span></template
        >
        <template #cell-env="{ row }">{{
          s(row).required_env.length ? s(row).required_env.join(", ") : "—"
        }}</template>
        <template #cell-status="{ row }">
          <template v-if="chips(s(row)).length">
            <span v-for="ch in chips(s(row))" :key="ch.text" class="chip" :class="ch.cls">{{
              ch.text
            }}</span>
          </template>
          <span v-else class="c-3">—</span>
        </template>
        <template #cell-actions="{ row }">
          <button
            v-if="canUpdate(s(row))"
            class="btn btn-secondary btn-sm"
            type="button"
            @click.stop="skillAction(s(row).name, 'update')"
          >
            Update
          </button>
          <button
            v-if="s(row).actions.includes('reset')"
            class="btn btn-secondary btn-sm"
            type="button"
            @click.stop="skillAction(s(row).name, 'reset')"
          >
            Reset
          </button>
          <button
            v-if="s(row).actions.includes('uninstall')"
            class="btn btn-danger-soft btn-sm"
            type="button"
            @click.stop="skillAction(s(row).name, 'uninstall')"
          >
            Uninstall
          </button>
        </template>
      </DataTable>
    </template>

    <template v-else>
      <StateBlock v-if="catalogState === 'idle' || catalogState === 'loading'" state="loading" />
      <StateBlock
        v-else-if="catalogState === 'error'"
        state="error"
        :text="catalogError"
        action="Retry"
        @retry="loadCatalog"
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
        <StateBlock v-if="filtered.length === 0" state="empty" text="No skill matches" />
        <DataTable
          v-else
          :columns="catalogColumns"
          :rows="filtered as unknown as Record<string, unknown>[]"
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
              @click.stop="install(c(row))"
            >
              Install
            </button>
          </template>
        </DataTable>
      </template>
    </template>

    <SidePanel :open="manual !== null" title="Install from the terminal" @close="manual = null">
      <template v-if="manual">
        <p>
          The skill guard marked /{{ manual.name }} as dangerous. The dashboard does not install it.
        </p>
        <p>Install it from a terminal on the host. The terminal asks you to type the name.</p>
        <div class="code-block">{{ installCommand(manual) }}</div>
        <button
          v-if="isSecure()"
          class="btn btn-secondary"
          type="button"
          @click="copy(installCommand(manual))"
        >
          Copy command
        </button>
        <p v-else class="c-3">Needs HTTPS or localhost</p>
      </template>
    </SidePanel>

    <p class="pill-note">
      Each skill is a /command in Chat. A new skill loads in new sessions. The agent itself can
      never install a skill.
    </p>
  </div>
</template>
