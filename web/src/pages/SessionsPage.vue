<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue"
import { useRoute, useRouter } from "vue-router"
import { api } from "../api/client"
import type { SearchHitView, SessionPage, SessionRow } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import RowMenu from "../components/RowMenu.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useCommand } from "../lib/command"
import { useLiveData } from "../lib/live"
import { fullTime, shortTime } from "../lib/time"
import { session } from "../stores/session"
import { toast, type ToastKind } from "../stores/ui"

type TableRow = SessionRow & { child?: boolean; actions?: string; toggle?: boolean; count?: number }

const router = useRouter()
const route = useRoute()
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const rows = ref<SessionRow[]>([])
const total = ref(0)
const hits = ref<SearchHitView[]>([])
const q0 = route.query
const str = (v: unknown): string => (typeof v === "string" ? v : "")
const off0 = Number(str(q0.off))
const search = ref(str(q0.q))
const fullText = ref(false)
const source = ref(str(q0.src))
const kind = ref(typeof q0.kind === "string" ? q0.kind : "chat")
const archived = ref(str(q0.arch) === "1")
const offset = ref(Number.isInteger(off0) && off0 >= 0 ? off0 : 0)
const hitsMore = ref(false)
const searchError = ref("")
const activeTotal = ref(0)
const archivedTotal = ref(0)
const renaming = ref("")
const renameText = ref("")

const allColumns = [
  { key: "title", label: "Session" },
  { key: "kind", label: "Kind" },
  { key: "source_label", label: "Source" },
  { key: "started", label: "Started" },
  { key: "last_active", label: "Last active" },
  { key: "messages", label: "Messages", align: "right" as const },
  { key: "cost_usd", label: "Cost", align: "right" as const },
  { key: "actions", label: "" },
]

// The Kind column says nothing while the filter shows only chats.
const columns = computed(() =>
  kind.value === "chat" ? allColumns.filter((c) => c.key !== "kind") : allColumns,
)

function usd(n: number): string {
  return "$" + n.toFixed(4)
}

// Builds the list query with the six keys in a fixed order.
function listPath(f: {
  source: string
  kind: string
  search: string
  archived: boolean
  offset: number
  limit: number
}): string {
  const q = new URLSearchParams({
    source: f.source,
    kind: f.kind,
    search: f.search,
    archived: f.archived ? "1" : "0",
    limit: String(f.limit),
    offset: String(f.offset),
  })
  return "/api/sessions?" + q.toString()
}

// The route query holds the filters. A key with its default value is left out.
function routeQuery(): Record<string, string> {
  const out: Record<string, string> = {}
  if (search.value !== "") out.q = search.value
  if (source.value !== "") out.src = source.value
  if (kind.value !== "chat") out.kind = kind.value
  if (archived.value) out.arch = "1"
  if (offset.value > 0) out.off = String(offset.value)
  return out
}

function syncRoute(): void {
  void router.replace({ query: routeQuery() })
}

// Builds the search query with the six keys in a fixed order.
function searchPath(off: number): string {
  const q = new URLSearchParams({
    q: search.value.trim(),
    limit: "20",
    offset: String(off),
    source: source.value,
    kind: kind.value,
    archived: archived.value ? "1" : "0",
  })
  return "/api/search?" + q.toString()
}

// Counts the loads. A reply that is older than the newest request is dropped.
let seq = 0

const searching = computed(() => fullText.value && search.value.trim() !== "")

async function load(): Promise<void> {
  const my = ++seq
  const text = search.value.trim()
  if (fullText.value && text !== "") {
    try {
      const res = (await api<SearchHitView[]>("GET", searchPath(0))) ?? []
      if (my !== seq) return
      hits.value = res
      hitsMore.value = res.length === 20
      searchError.value = ""
    } catch (e) {
      if (my !== seq) return
      searchError.value = "Search failed: " + friendlyError(e)
    }
    if (state.value === "loading") state.value = "ready"
    return
  }
  hits.value = []
  hitsMore.value = false
  searchError.value = ""
  try {
    const res = await api<SessionPage>(
      "GET",
      listPath({
        source: source.value,
        kind: kind.value,
        search: fullText.value ? "" : text,
        archived: archived.value,
        offset: offset.value,
        limit: 50,
      }),
    )
    if (my !== seq) return
    // The other state needs only its total. A failure keeps the old count.
    let otherTotal: number | null = null
    try {
      const other = await api<SessionPage>(
        "GET",
        listPath({
          source: source.value,
          kind: kind.value,
          search: fullText.value ? "" : text,
          archived: !archived.value,
          offset: 0,
          limit: 1,
        }),
      )
      otherTotal = other.total
    } catch {
      otherTotal = null
    }
    if (my !== seq) return
    rows.value = res.rows ?? []
    total.value = res.total
    if (archived.value) {
      archivedTotal.value = res.total
      if (otherTotal !== null) activeTotal.value = otherTotal
    } else {
      activeTotal.value = res.total
      if (otherTotal !== null) archivedTotal.value = otherTotal
    }
    state.value = "ready"
  } catch (e) {
    if (my !== seq) return
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

async function loadMoreHits(): Promise<void> {
  const my = ++seq
  try {
    const res = (await api<SearchHitView[]>("GET", searchPath(hits.value.length))) ?? []
    if (my !== seq) return
    hits.value = [...hits.value, ...res]
    hitsMore.value = res.length === 20
    searchError.value = ""
  } catch (e) {
    if (my !== seq) return
    searchError.value = "Search failed: " + friendlyError(e)
  }
}

// Splits a search snippet into text parts. A part between "[" and "]" is a match.
function markParts(s: string): { text: string; mark: boolean }[] {
  const out: { text: string; mark: boolean }[] = []
  let cur = ""
  let open = false
  const flush = (): void => {
    if (cur !== "") out.push({ text: cur, mark: open })
    cur = ""
  }
  for (const ch of s) {
    if (ch === "[" && !open) {
      flush()
      open = true
    } else if (ch === "]" && open) {
      flush()
      open = false
    } else cur += ch
  }
  flush()
  return out
}

const { run } = useCommand()
const { stale, updatedAt, refresh } = useLiveData(load)

// Runs one command, then loads the page again.
async function command(text: string): Promise<ToastKind> {
  const result = await run(text)
  await refresh()
  return result
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
    const kids = r.children ?? []
    if (kids.length === 0) continue
    out.push({
      id: r.id + "#kids",
      parent_id: r.id,
      toggle: true,
      count: kids.length,
    } as unknown as TableRow)
    if (openKids.value.has(r.id))
      for (const c of kids) out.push({ ...c, read_only: true, child: true })
  }
  return out
})

// Holds the ids of the parents whose child rows show. A live reload keeps it.
const openKids = ref(new Set<string>())

function toggleKids(parent: string): void {
  const next = new Set(openKids.value)
  if (next.has(parent)) next.delete(parent)
  else next.add(parent)
  openKids.value = next
}

// Returns the chip and the name of a child row. A child whose title equals its kind
// shows its name once.
function childLabel(r: TableRow): { chip: string; text: string } {
  if (r.title === r.kind)
    return { chip: "", text: r.kind === "review" ? "Background review" : r.title }
  return { chip: r.kind, text: r.title || "New session" }
}

const pageText = computed(
  () => `Page ${Math.floor(offset.value / 50) + 1} of ${Math.max(1, Math.ceil(total.value / 50))}`,
)

const range = computed(() => {
  if (rows.value.length === 0) return "0 of " + total.value
  return `${offset.value + 1}–${offset.value + rows.value.length} of ${total.value}`
})

function view(id: string): void {
  router.push({ name: "transcript", params: { id } })
}

async function resume(id: string): Promise<void> {
  if ((await run("/resume " + id)) === "ok") router.push({ name: "chat" })
  else await refresh()
}

async function newSession(): Promise<void> {
  if ((await run("/new")) === "ok") router.push({ name: "chat" })
  else await refresh()
}

async function fork(id: string): Promise<void> {
  if ((await run("/fork from " + id)) === "ok") router.push({ name: "chat" })
  else await refresh()
}

const vFocus = { mounted: (el: HTMLElement) => el.focus() }

function startRename(r: SessionRow): void {
  renaming.value = r.id
  renameText.value = r.title
}

function cancelRename(): void {
  renaming.value = ""
}

async function finishRename(id: string): Promise<void> {
  const t = renameText.value.trim()
  renaming.value = ""
  if (t !== "") await command(`/rename ${id} ${t}`)
}

function menuItems(r: SessionRow): { label: string; act: string; danger?: boolean }[] {
  const items: { label: string; act: string; danger?: boolean }[] = [
    { label: "Rename", act: "rename" },
    { label: "Fork", act: "fork" },
    { label: "Export", act: "export" },
  ]
  if (!r.archived) items.push({ label: "Archive", act: "archive" })
  items.push({ label: "Delete…", act: "delete", danger: true })
  return items
}

const readOnlyItems = [
  { label: "Fork into dashboard", act: "fork" },
  { label: "Export", act: "export" },
]

function onMenu(r: SessionRow, act: string): void {
  if (act === "rename") startRename(r)
  else if (act === "fork") void fork(r.id)
  else if (act === "export") void download(`/api/sessions/${r.id}/export`, `session-${r.id}.jsonl`)
  else if (act === "archive") void command("/archive " + r.id)
  else if (act === "delete") void command("/delete " + r.id)
}

let timer: ReturnType<typeof setTimeout> | undefined
watch(search, () => {
  clearTimeout(timer)
  timer = setTimeout(() => {
    offset.value = 0
    syncRoute()
    void load()
  }, 250)
})
watch(source, (s) => {
  if (s === "cron") kind.value = ""
})
watch([fullText, source, kind, archived], () => {
  offset.value = 0
  syncRoute()
  void load()
})

function page(delta: number): void {
  offset.value = Math.max(0, offset.value + delta)
  syncRoute()
  void load()
}

const filtered = computed(
  () =>
    search.value.trim() !== "" || source.value !== "" || kind.value !== "chat" || archived.value,
)

function clearFilters(): void {
  search.value = ""
  source.value = ""
  kind.value = "chat"
  archived.value = false
  offset.value = 0
  syncRoute()
  void load()
}

onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Sessions</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
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
      <select v-model="source" class="input" aria-label="Source">
        <option value="">All sources</option>
        <option value="dashboard">Dashboard</option>
        <option value="cli">CLI</option>
        <option value="telegram">Telegram</option>
        <option value="cron">Cron</option>
      </select>
      <select v-model="kind" class="input" aria-label="Kind">
        <option value="chat">Chats</option>
        <option value="">Chats and cron runs</option>
      </select>
      <div class="seg">
        <button type="button" :class="{ 'is-on': !archived }" @click="archived = false">
          Active · {{ activeTotal }}
        </button>
        <button type="button" :class="{ 'is-on': archived }" @click="archived = true">
          Archived · {{ archivedTotal }}
        </button>
      </div>
    </div>

    <p v-if="searchError" class="t-12 c-bad search-err" role="alert">{{ searchError }}</p>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="refresh"
    />
    <template v-else>
      <template v-if="searching">
        <div v-if="hits.length > 0" class="card hits">
          <div class="card-h">
            <h2 class="h">{{ hits.length }} hits</h2>
          </div>
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
              ><b>{{ h.title }}</b
              >{{ " "
              }}<span class="c-2"
                ><template v-for="(p, i) in markParts(h.snippet)" :key="i"
                  ><mark v-if="p.mark">{{ p.text }}</mark
                  ><template v-else>{{ p.text }}</template></template
                ></span
              ></span
            >
            <span class="chip">{{ h.source }}</span>
            <span class="chip">{{ h.kind }}</span>
            <span class="c-3 t-12" style="white-space: nowrap" :title="fullTime(h.date)">{{
              shortTime(h.date) || "—"
            }}</span>
          </router-link>
          <button v-if="hitsMore" class="btn btn-secondary" type="button" @click="loadMoreHits">
            Load more
          </button>
        </div>
        <StateBlock v-else-if="!searchError" state="empty" text="No messages match the search." />
      </template>
      <template v-else>
        <StateBlock
          v-if="tableRows.length === 0"
          state="empty"
          text="No sessions match the filters."
          :action="filtered ? 'Clear filters' : undefined"
          @action="clearFilters"
        />
        <template v-else>
          <div :aria-busy="stale || undefined">
            <DataTable
              :columns="columns"
              :rows="tableRows as unknown as Record<string, unknown>[]"
              row-key="id"
            >
              <template #cell-title="{ row }">
                <button
                  v-if="(row as unknown as TableRow).toggle"
                  class="link kids-toggle"
                  type="button"
                  :aria-expanded="openKids.has((row as unknown as TableRow).parent_id)"
                  @click="toggleKids((row as unknown as TableRow).parent_id)"
                >
                  {{ openKids.has((row as unknown as TableRow).parent_id) ? "▾" : "▸" }}
                  {{ (row as unknown as TableRow).count }}
                  {{
                    (row as unknown as TableRow).count === 1 ? "child session" : "child sessions"
                  }}
                </button>
                <span v-else-if="(row as unknown as TableRow).child" class="cellrow is-child">
                  <span aria-hidden="true">↳</span>
                  <span v-if="childLabel(row as unknown as TableRow).chip" class="chip">{{
                    childLabel(row as unknown as TableRow).chip
                  }}</span>
                  <span class="kid-label">{{ childLabel(row as unknown as TableRow).text }}</span>
                </span>
                <input
                  v-else-if="renaming === (row as unknown as TableRow).id"
                  v-model="renameText"
                  v-focus
                  autofocus
                  class="input"
                  aria-label="New title"
                  @keydown.enter.prevent="finishRename((row as unknown as TableRow).id)"
                  @keydown.esc.prevent="cancelRename"
                  @blur="cancelRename"
                />
                <span v-else class="cellrow">
                  <router-link
                    class="cell-title"
                    :class="{ 'c-3': !(row as unknown as TableRow).title }"
                    :to="{ name: 'transcript', params: { id: (row as unknown as TableRow).id } }"
                    :title="(row as unknown as TableRow).title"
                    ><b>{{ (row as unknown as TableRow).title || "New session" }}</b></router-link
                  >
                  <span v-if="(row as unknown as TableRow).id === session.id" class="chip chip-acc"
                    >active</span
                  >
                </span>
                <template v-if="renaming === (row as unknown as TableRow).id">
                  <button
                    class="btn btn-primary btn-sm"
                    type="button"
                    @mousedown.prevent
                    @click="finishRename((row as unknown as TableRow).id)"
                  >
                    Save
                  </button>
                  <button
                    class="btn btn-ghost btn-sm"
                    type="button"
                    @mousedown.prevent
                    @click="cancelRename"
                  >
                    Cancel
                  </button>
                </template>
              </template>
              <template #cell-source_label="{ row }"
                ><template v-if="!(row as unknown as TableRow).toggle"
                  ><bdi class="cell-title" :title="(row as unknown as TableRow).source_label">{{
                    (row as unknown as TableRow).source_label
                  }}</bdi></template
                ></template
              >
              <template #cell-started="{ row }"
                ><template v-if="!(row as unknown as TableRow).toggle"
                  ><span
                    style="white-space: nowrap"
                    :title="fullTime((row as unknown as TableRow).started)"
                    >{{ shortTime((row as unknown as TableRow).started) || "—" }}</span
                  ></template
                ></template
              >
              <template #cell-last_active="{ row }"
                ><template v-if="!(row as unknown as TableRow).toggle"
                  ><span
                    style="white-space: nowrap"
                    :title="fullTime((row as unknown as TableRow).last_active)"
                    >{{ shortTime((row as unknown as TableRow).last_active) || "—" }}</span
                  ></template
                ></template
              >
              <template #cell-cost_usd="{ row }"
                ><template v-if="!(row as unknown as TableRow).toggle">{{
                  usd((row as unknown as TableRow).cost_usd)
                }}</template></template
              >
              <template #cell-actions="{ row }">
                <template v-for="r in [row as unknown as TableRow]" :key="r.id">
                  <template v-if="!r.toggle">
                    <button v-if="r.child" class="btn btn-ghost" type="button" @click="view(r.id)">
                      View
                    </button>
                    <template v-else-if="r.read_only">
                      <button class="btn btn-secondary" type="button" @click="view(r.id)">
                        View
                      </button>
                      <RowMenu :items="readOnlyItems" @select="(a) => onMenu(r, a)" />
                    </template>
                    <template v-else>
                      <template v-if="r.archived">
                        <button
                          class="btn btn-secondary"
                          type="button"
                          @click="command('/unarchive ' + r.id)"
                        >
                          Unarchive
                        </button>
                        <button class="btn btn-ghost" type="button" @click="resume(r.id)">
                          Resume
                        </button>
                      </template>
                      <button
                        v-else-if="r.id === session.id"
                        class="btn btn-secondary"
                        type="button"
                        @click="router.push({ name: 'chat' })"
                      >
                        Open
                      </button>
                      <button v-else class="btn btn-secondary" type="button" @click="resume(r.id)">
                        Resume
                      </button>
                      <RowMenu :items="menuItems(r)" @select="(a) => onMenu(r, a)" />
                    </template>
                  </template>
                </template>
              </template>
            </DataTable>
          </div>
          <div class="pager">
            <span class="grow">{{ range }}</span>
            <span>{{ pageText }}</span>
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
    </template>

    <p class="pill-note">
      Telegram, CLI, and cron sessions are read-only here. “Fork into dashboard” copies one into a
      new dashboard session.
    </p>
  </div>
</template>
