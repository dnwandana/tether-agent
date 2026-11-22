<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { useRouter } from "vue-router"
import { api, ApiError, runCommand } from "../api/client"
import type { ReplyView, SearchHitView, SessionPage, SessionRow } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import StateBlock from "../components/StateBlock.vue"
import { session } from "../stores/session"
import { confirm, toast } from "../stores/ui"

type TableRow = SessionRow & { child?: boolean; actions?: string }

const router = useRouter()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const rows = ref<SessionRow[]>([])
const total = ref(0)
const hits = ref<SearchHitView[]>([])
const search = ref("")
const fullText = ref(false)
const source = ref("")
const kind = ref("chat")
const archived = ref(false)
const offset = ref(0)
const menuFor = ref("")
const renaming = ref("")
const renameText = ref("")

const columns = [
  { key: "title", label: "Session" },
  { key: "source_label", label: "Source" },
  { key: "last_active", label: "Last active" },
  { key: "messages", label: "Messages", align: "right" as const },
  { key: "cost_usd", label: "Cost", align: "right" as const },
  { key: "actions", label: "" },
]

function usd(n: number): string {
  return "$" + n.toFixed(4)
}

function when(s: string): string {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? "—" : d.toLocaleString()
}

// Builds the list query with the six keys in a fixed order.
function listPath(f: {
  source: string
  kind: string
  search: string
  archived: boolean
  offset: number
}): string {
  const q = new URLSearchParams({
    source: f.source,
    kind: f.kind,
    search: f.search,
    archived: f.archived ? "1" : "0",
    limit: "50",
    offset: String(f.offset),
  })
  return "/api/sessions?" + q.toString()
}

async function load(): Promise<void> {
  try {
    const text = search.value.trim()
    const res = await api<SessionPage>(
      "GET",
      listPath({
        source: source.value,
        kind: kind.value,
        search: fullText.value ? "" : text,
        archived: archived.value,
        offset: offset.value,
      }),
    )
    rows.value = res.rows ?? []
    total.value = res.total
    hits.value =
      fullText.value && text !== ""
        ? ((await api<SearchHitView[]>("GET", "/api/search?q=" + encodeURIComponent(text))) ?? [])
        : []
    state.value = "ready"
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

async function act(text: string): Promise<boolean> {
  let ok = true
  try {
    const r = await runCommand(text)
    if (!("text" in r) || r.text === undefined) {
      toast("The command runs. The reply comes later.", "info")
    } else {
      const reply = r as ReplyView
      const button = (reply.buttons ?? []).flat().find((b) => b.data.endsWith("confirm"))
      if (button) {
        const [title, ...rest] = reply.text.split("\n")
        const yes = await confirm({
          title,
          body: rest.join("\n").trim(),
          confirmLabel: button.text,
          danger: true,
        })
        if (yes) await act(button.data)
        return yes
      }
      toast(reply.text, "ok")
    }
  } catch (e) {
    ok = false
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
  await load()
  return ok
}

// Downloads a file with the session header. A plain link cannot send it.
async function download(path: string, name: string): Promise<void> {
  try {
    let h: string | null = null
    try {
      h = localStorage.getItem("tether.session")
    } catch {
      h = null
    }
    const r = await fetch(path, {
      headers: h ? { "X-Tether-Session": h } : {},
      credentials: "same-origin",
    })
    if (!r.ok) throw new Error(`HTTP ${r.status}`)
    const url = URL.createObjectURL(await r.blob())
    const a = document.createElement("a")
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    a.remove()
    // Safari on iOS can start the download after click() returns.
    setTimeout(() => URL.revokeObjectURL(url), 30_000)
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
}

// Puts the children of each row after it, marked as child rows.
const tableRows = computed<TableRow[]>(() => {
  const out: TableRow[] = []
  for (const r of rows.value) {
    out.push(r)
    for (const c of r.children ?? []) out.push({ ...c, read_only: true, child: true })
  }
  return out
})

const range = computed(() => {
  if (rows.value.length === 0) return "0 of " + total.value
  return `${offset.value + 1}–${offset.value + rows.value.length} of ${total.value}`
})

function view(id: string): void {
  router.push({ name: "transcript", params: { id } })
}

async function resume(id: string): Promise<void> {
  if (await act("/resume " + id)) router.push({ name: "chat" })
}

async function newSession(): Promise<void> {
  if (await act("/new")) router.push({ name: "chat" })
}

function toggleMenu(id: string): void {
  menuFor.value = menuFor.value === id ? "" : id
}

function startRename(r: SessionRow): void {
  menuFor.value = ""
  renaming.value = r.id
  renameText.value = r.title
}

async function finishRename(id: string): Promise<void> {
  const t = renameText.value.trim()
  renaming.value = ""
  if (t !== "") await act(`/rename ${id} ${t}`)
}

function menuAct(fn: () => unknown): void {
  menuFor.value = ""
  void fn()
}

let timer: ReturnType<typeof setTimeout> | undefined
watch(search, () => {
  clearTimeout(timer)
  timer = setTimeout(() => {
    offset.value = 0
    void load()
  }, 250)
})
watch([fullText, source, kind, archived], () => {
  offset.value = 0
  void load()
})

function page(delta: number): void {
  offset.value = Math.max(0, offset.value + delta)
  void load()
}

onMounted(load)
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Sessions</h1>
      <button class="btn btn-primary" type="button" @click="newSession">+ New session</button>
    </div>

    <div class="toolbar">
      <div class="search">
        <input
          v-model="search"
          class="input"
          type="search"
          :placeholder="fullText ? 'Search all messages' : 'Search titles'"
          :aria-label="fullText ? 'Search all messages' : 'Search titles'"
        />
      </div>
      <label class="check"><input v-model="fullText" type="checkbox" /> Full text</label>
      <select v-model="source" class="select" aria-label="Source">
        <option value="">All sources</option>
        <option value="dashboard">Dashboard</option>
        <option value="cli">CLI</option>
        <option value="telegram">Telegram</option>
        <option value="cron">Cron</option>
      </select>
      <select v-model="kind" class="select" aria-label="Kind">
        <option value="chat">Chats</option>
        <option value="">Chats and cron runs</option>
      </select>
      <div class="seg">
        <button type="button" :class="{ 'is-on': !archived }" @click="archived = false">
          Active
        </button>
        <button type="button" :class="{ 'is-on': archived }" @click="archived = true">
          Archived
        </button>
      </div>
    </div>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @retry="load"
    />
    <template v-else>
      <div v-if="hits.length > 0" class="card">
        <div class="card-h"><h3>Message hits</h3></div>
        <router-link
          v-for="h in hits"
          :key="h.session_id + ':' + h.message_id"
          class="li"
          :to="{
            name: 'transcript',
            params: { id: h.session_id },
            query: { message: String(h.message_id) },
          }"
        >
          <span class="grow"
            ><b>{{ h.title }}</b> <span class="c-2">{{ h.snippet }}</span></span
          >
          <span class="c-3 t-12">{{ when(h.date) }}</span>
        </router-link>
      </div>

      <StateBlock
        v-if="tableRows.length === 0"
        state="empty"
        text="No sessions match the filters."
      />
      <template v-else>
        <DataTable
          :columns="columns"
          :rows="tableRows as unknown as Record<string, unknown>[]"
          row-key="id"
        >
          <template #cell-title="{ row }">
            <span v-if="(row as unknown as TableRow).child" class="cellrow is-child">
              <span aria-hidden="true">↳</span>
              <span class="chip">{{ (row as unknown as TableRow).kind }}</span>
              <span>{{ (row as unknown as TableRow).title }}</span>
            </span>
            <input
              v-else-if="renaming === (row as unknown as TableRow).id"
              v-model="renameText"
              class="input"
              aria-label="New title"
              @keydown.enter.prevent="finishRename((row as unknown as TableRow).id)"
              @keydown.esc.prevent="renaming = ''"
            />
            <b v-else>{{ (row as unknown as TableRow).title }}</b>
          </template>
          <template #cell-last_active="{ row }">{{
            when((row as unknown as TableRow).last_active)
          }}</template>
          <template #cell-cost_usd="{ row }">{{
            usd((row as unknown as TableRow).cost_usd)
          }}</template>
          <template #cell-actions="{ row }">
            <template v-for="r in [row as unknown as TableRow]" :key="r.id">
              <button v-if="r.child" class="btn btn-ghost" type="button" @click="view(r.id)">
                View
              </button>
              <template v-else-if="r.read_only">
                <button class="btn btn-ghost" type="button" @click="view(r.id)">View</button>
                <button class="btn btn-secondary" type="button" @click="act('/fork from ' + r.id)">
                  Fork into dashboard
                </button>
                <button
                  class="btn btn-ghost"
                  type="button"
                  @click="download(`/api/sessions/${r.id}/export`, `session-${r.id}.jsonl`)"
                >
                  Export
                </button>
              </template>
              <template v-else>
                <button
                  v-if="r.id === session.id"
                  class="btn btn-secondary"
                  type="button"
                  @click="router.push({ name: 'chat' })"
                >
                  Open
                </button>
                <button v-else class="btn btn-secondary" type="button" @click="resume(r.id)">
                  Resume
                </button>
                <span style="position: relative; display: inline-block">
                  <button
                    class="btn btn-ghost"
                    type="button"
                    aria-label="More actions"
                    @click="toggleMenu(r.id)"
                  >
                    ⋯
                  </button>
                  <div
                    v-if="menuFor === r.id"
                    class="menu"
                    style="position: absolute; right: 0; z-index: 20"
                  >
                    <button class="menu-item" type="button" @click="startRename(r)">Rename</button>
                    <button
                      class="menu-item"
                      type="button"
                      @click="menuAct(() => act('/fork from ' + r.id))"
                    >
                      Fork
                    </button>
                    <button
                      class="menu-item"
                      type="button"
                      @click="
                        menuAct(() =>
                          download(`/api/sessions/${r.id}/export`, `session-${r.id}.jsonl`),
                        )
                      "
                    >
                      Export
                    </button>
                    <button
                      v-if="r.archived"
                      class="menu-item"
                      type="button"
                      @click="menuAct(() => act('/resume ' + r.id))"
                    >
                      Restore
                    </button>
                    <button
                      v-else
                      class="menu-item"
                      type="button"
                      @click="menuAct(() => act('/archive ' + r.id))"
                    >
                      Archive
                    </button>
                    <button
                      class="menu-item danger"
                      type="button"
                      @click="menuAct(() => act('/delete ' + r.id))"
                    >
                      Delete…
                    </button>
                  </div>
                </span>
              </template>
            </template>
          </template>
        </DataTable>
        <div class="pager">
          <span class="grow">{{ range }}</span>
          <button class="btn btn-ghost" type="button" :disabled="offset === 0" @click="page(-50)">
            ◀ Prev
          </button>
          <button
            class="btn btn-ghost"
            type="button"
            :disabled="offset + rows.length >= total"
            @click="page(50)"
          >
            Next ▶
          </button>
        </div>
      </template>
    </template>

    <p class="pill-note">
      Telegram, CLI, and cron sessions are read-only here. “Fork into dashboard” copies one into a
      new dashboard session.
    </p>
  </div>
</template>
