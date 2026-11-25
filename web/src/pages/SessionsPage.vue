<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue"
import { useRoute, useRouter, type LocationQuery } from "vue-router"
import { api } from "../api/client"
import type { SearchHitView, SessionPage, SessionRow } from "../api/types.gen"
import { ChevronDownIcon, ChevronRightIcon, CornerDownRightIcon } from "@lucide/vue"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import { Field, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { NativeSelect } from "@/components/ui/native-select"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"
import DataTable from "../components/DataTable.vue"
import type { Column } from "../lib/table"
import LiveHeader from "../components/LiveHeader.vue"
import RowMenu from "../components/RowMenu.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useCommand } from "../lib/command"
import { useLiveData } from "../lib/live"
import { fullTime, shortTime } from "../lib/time"
import { session } from "../stores/session"
import { clampPage, parsePage, readSize, writeSize, type PageSize } from "../lib/pager"
import { rememberListQuery, toast, type ToastKind } from "../stores/ui"

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
const search = ref(str(q0.q))
const fullText = ref(false)
const source = ref(str(q0.src))
const kind = ref(typeof q0.kind === "string" ? q0.kind : "chat")
const archived = ref(str(q0.arch) === "1")
// The page reads the stored size before its first load, because DataTable mounts after it.
const size = ref<PageSize>(readSize("sessions"))
// True from the request of a list page to its reply. It disables the pager.
const loading = ref(false)
// An old link has ?off=N. It opens the page that holds row N.
function firstPage(): number {
  if (q0.page !== undefined) return parsePage(q0.page)
  const off = Number(str(q0.off))
  return Number.isInteger(off) && off >= 0 ? Math.floor(off / size.value) + 1 : 1
}
const pageNo = ref(firstPage())
const hitsMore = ref(false)
const searchError = ref("")
const activeTotal = ref(0)
const archivedTotal = ref(0)
const renaming = ref("")
const renameText = ref("")

// The actions column fits the widest button set of the rows of the filter. Archived rows show
// Unarchive, Resume, and the row menu. The other rows show one button and the row menu.
const ACT_WIDTH = "8.5rem"
const ACT_WIDTH_ARCHIVED = "14.5rem"

// The Kind column says nothing while the filter shows only chats.
const columns = computed<Column[]>(() => {
  const all: Column[] = [
    { key: "title", label: "Session", type: "text" },
    { key: "kind", label: "Kind", type: "badge", optional: true },
    { key: "source_label", label: "Source", type: "text" },
    { key: "started", label: "Started", type: "date", optional: true },
    { key: "last_active", label: "Last active", type: "date" },
    { key: "messages", label: "Messages", type: "num", align: "right", optional: true },
    { key: "cost_usd", label: "Cost", type: "num", align: "right" },
    {
      key: "actions",
      label: "",
      type: "act",
      width: archived.value ? ACT_WIDTH_ARCHIVED : ACT_WIDTH,
    },
  ]
  return kind.value === "chat" ? all.filter((c) => c.key !== "kind") : all
})

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
  if (pageNo.value > 1) out.page = String(pageNo.value)
  return out
}

// Returns true when the two queries hold the same keys and values.
function sameQuery(a: Record<string, string>, b: LocationQuery): boolean {
  const ak = Object.keys(a)
  const bk = Object.keys(b)
  return ak.length === bk.length && ak.every((k) => a[k] === b[k])
}

// Writes the route query only on this route and only when it changes. A write during a
// navigation to another route cancels that navigation. It also keeps the query for the
// back link of the transcript.
function syncRoute(): void {
  if (route.name !== "sessions") return
  const q = routeQuery()
  rememberListQuery("sessions", q)
  if (sameQuery(q, route.query)) return
  void router.replace({ query: q })
}
syncRoute()

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
  loading.value = true
  try {
    const res = await api<SessionPage>(
      "GET",
      listPath({
        source: source.value,
        kind: kind.value,
        search: fullText.value ? "" : text,
        archived: archived.value,
        offset: (pageNo.value - 1) * size.value,
        limit: size.value,
      }),
    )
    if (my !== seq) return
    // A Remove or a live reload can shrink the list. A page beyond the end loads the last page.
    const fixed = clampPage(pageNo.value, res.total, size.value)
    if (res.total > 0 && fixed !== pageNo.value) {
      pageNo.value = fixed
      syncRoute()
      return load()
    }
    // An empty list has only page 1, so the address and the back link drop the old page.
    if (res.total === 0 && pageNo.value > 1) {
      pageNo.value = 1
      syncRoute()
    }
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
    loading.value = false
    state.value = "ready"
  } catch (e) {
    if (my !== seq) return
    loading.value = false
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

// The row menu gives the focus back to its trigger when it closes. A focus at mount
// time loses to that, so the field takes the focus in a timer.
const vFocus = { mounted: (el: HTMLElement) => void setTimeout(() => el.focus(), 0) }

// A blur to the menu trigger comes from the menu, not from the user. The field takes the
// focus again. Any other blur cancels the rename.
function onRenameBlur(e: FocusEvent): void {
  const to = e.relatedTarget
  if (to instanceof HTMLElement && to.dataset.slot === "dropdown-menu-trigger") {
    const el = e.target as HTMLElement
    setTimeout(() => el.focus(), 0)
    return
  }
  cancelRename()
}

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
    pageNo.value = 1
    syncRoute()
    void load()
  }, 250)
})
watch(source, (s) => {
  if (s === "cron") kind.value = ""
})
watch([fullText, source, kind, archived], () => {
  pageNo.value = 1
  syncRoute()
  void load()
})

// A tap on the pressed item sends an empty value. It changes nothing.
function pickArchived(v: unknown): void {
  if (v === "active" || v === "archived") archived.value = v === "archived"
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
  pageNo.value = 1
  syncRoute()
  void load()
}

function setPage(p: number): void {
  if (p === pageNo.value) return
  pageNo.value = p
  syncRoute()
  void load()
}

function setSize(n: PageSize): void {
  writeSize("sessions", n)
  size.value = n
  pageNo.value = 1
  syncRoute()
  void load()
}

// A NavRail link to /sessions from ?page=3 shows page 1.
watch(
  () => route.query.page,
  (v) => {
    if (route.name !== "sessions") return
    const p = parsePage(v)
    if (p === pageNo.value) return
    pageNo.value = p
    rememberListQuery("sessions", routeQuery())
    void load()
  },
)

onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-2">
      <h1 class="text-xl font-semibold tracking-tight">Sessions</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <Button class="ml-auto" type="button" @click="newSession">+ New session</Button>
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <Input
        v-model="search"
        class="w-auto min-w-48 flex-1"
        type="search"
        :placeholder="fullText ? 'Search all messages' : 'Search titles'"
        :aria-label="fullText ? 'Search all messages' : 'Search titles'"
      />
      <Field orientation="horizontal" class="w-auto">
        <Checkbox id="sessions-full-text" v-model="fullText" />
        <FieldLabel for="sessions-full-text">Full text</FieldLabel>
      </Field>
      <NativeSelect v-model="source" aria-label="Source">
        <option value="">All sources</option>
        <option value="dashboard">Dashboard</option>
        <option value="cli">CLI</option>
        <option value="telegram">Telegram</option>
        <option value="cron">Cron</option>
      </NativeSelect>
      <NativeSelect v-model="kind" aria-label="Kind">
        <option value="chat">Chats</option>
        <option value="">Chats and cron runs</option>
      </NativeSelect>
      <ToggleGroup
        type="single"
        variant="outline"
        :model-value="archived ? 'archived' : 'active'"
        @update:model-value="pickArchived"
      >
        <ToggleGroupItem value="active">Active · {{ activeTotal }}</ToggleGroupItem>
        <ToggleGroupItem value="archived">Archived · {{ archivedTotal }}</ToggleGroupItem>
      </ToggleGroup>
    </div>

    <p v-if="searchError" class="text-sm text-destructive" role="alert">{{ searchError }}</p>

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
        <Card v-if="hits.length > 0">
          <CardHeader>
            <CardTitle>{{ hits.length }} hits</CardTitle>
          </CardHeader>
          <CardContent class="flex flex-col gap-2">
            <RouterLink
              v-for="h in hits"
              :key="h.session_id + ':' + h.message_id"
              class="flex items-center gap-2 rounded-md px-2 py-1.5 text-sm hover:bg-muted"
              :to="{
                name: 'transcript',
                params: { id: h.session_id },
                query: { message: String(h.message_id) },
              }"
            >
              <span class="min-w-0 flex-1"
                ><b>{{ h.title }}</b
                >{{ " "
                }}<span class="text-muted-foreground"
                  ><template v-for="(p, i) in markParts(h.snippet)" :key="i"
                    ><mark v-if="p.mark" class="rounded-sm bg-muted px-0.5 text-foreground">{{
                      p.text
                    }}</mark
                    ><template v-else>{{ p.text }}</template></template
                  ></span
                ></span
              >
              <Badge variant="outline">{{ h.source }}</Badge>
              <Badge variant="outline">{{ h.kind }}</Badge>
              <span
                class="text-xs whitespace-nowrap text-muted-foreground"
                :title="fullTime(h.date)"
                >{{ shortTime(h.date) || "—" }}</span
              >
            </RouterLink>
          </CardContent>
          <CardFooter v-if="hitsMore">
            <Button variant="secondary" type="button" @click="loadMoreHits">Load more</Button>
          </CardFooter>
        </Card>
        <StateBlock v-else-if="!searchError" state="empty" text="No messages match the search." />
      </template>
      <template v-else>
        <StateBlock
          v-if="total === 0"
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
              page-key="sessions"
              page-label="Sessions"
              :total="total"
              :page="pageNo"
              :size="size"
              :loading="loading"
              @update:page="setPage"
              @update:size="setSize"
            >
              <template #cell-title="{ row }">
                <Button
                  v-if="(row as unknown as TableRow).toggle"
                  variant="link"
                  size="sm"
                  type="button"
                  :aria-expanded="openKids.has((row as unknown as TableRow).parent_id)"
                  @click="toggleKids((row as unknown as TableRow).parent_id)"
                >
                  <ChevronDownIcon v-if="openKids.has((row as unknown as TableRow).parent_id)" />
                  <ChevronRightIcon v-else />
                  {{ (row as unknown as TableRow).count }}
                  {{
                    (row as unknown as TableRow).count === 1 ? "child session" : "child sessions"
                  }}
                </Button>
                <span
                  v-else-if="(row as unknown as TableRow).child"
                  class="flex min-w-0 items-center gap-1.5 pl-4"
                >
                  <CornerDownRightIcon class="size-4 shrink-0 text-muted-foreground" />
                  <Badge v-if="childLabel(row as unknown as TableRow).chip" variant="secondary">{{
                    childLabel(row as unknown as TableRow).chip
                  }}</Badge>
                  <span
                    class="min-w-0 truncate"
                    :title="childLabel(row as unknown as TableRow).text"
                    >{{ childLabel(row as unknown as TableRow).text }}</span
                  >
                </span>
                <span
                  v-else-if="renaming === (row as unknown as TableRow).id"
                  class="flex min-w-0 flex-wrap items-center gap-2"
                >
                  <Input
                    v-model="renameText"
                    v-focus
                    autofocus
                    class="min-w-0 flex-1 basis-40"
                    aria-label="New title"
                    @keydown.enter.prevent="finishRename((row as unknown as TableRow).id)"
                    @keydown.esc.prevent="cancelRename"
                    @blur="onRenameBlur"
                  />
                  <Button
                    size="sm"
                    type="button"
                    @mousedown.prevent
                    @click="finishRename((row as unknown as TableRow).id)"
                  >
                    Save
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    type="button"
                    @mousedown.prevent
                    @click="cancelRename"
                  >
                    Cancel
                  </Button>
                </span>
                <span v-else class="flex min-w-0 items-center gap-2">
                  <RouterLink
                    :class="
                      cn(
                        'block min-w-0 truncate font-semibold',
                        !(row as unknown as TableRow).title && 'text-muted-foreground',
                      )
                    "
                    :to="{ name: 'transcript', params: { id: (row as unknown as TableRow).id } }"
                    :title="(row as unknown as TableRow).title"
                    >{{ (row as unknown as TableRow).title || "New session" }}</RouterLink
                  >
                  <Badge v-if="(row as unknown as TableRow).id === session.id" class="shrink-0"
                    >active</Badge
                  >
                </span>
              </template>
              <template #cell-source_label="{ row }"
                ><template v-if="!(row as unknown as TableRow).toggle"
                  ><bdi class="block truncate" :title="(row as unknown as TableRow).source_label">{{
                    (row as unknown as TableRow).source_label
                  }}</bdi></template
                ></template
              >
              <template #cell-started="{ row }"
                ><template v-if="!(row as unknown as TableRow).toggle"
                  ><span
                    class="whitespace-nowrap"
                    :title="fullTime((row as unknown as TableRow).started)"
                    >{{ shortTime((row as unknown as TableRow).started) || "—" }}</span
                  ></template
                ></template
              >
              <template #cell-last_active="{ row }"
                ><template v-if="!(row as unknown as TableRow).toggle"
                  ><span
                    class="whitespace-nowrap"
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
                    <Button
                      v-if="r.child"
                      size="sm"
                      variant="ghost"
                      class="max-tab:min-h-11"
                      type="button"
                      @click="view(r.id)"
                    >
                      View
                    </Button>
                    <template v-else-if="r.read_only">
                      <Button
                        size="sm"
                        variant="secondary"
                        class="max-tab:min-h-11"
                        type="button"
                        @click="view(r.id)"
                      >
                        View
                      </Button>
                      <RowMenu :items="readOnlyItems" @select="(a) => onMenu(r, a)" />
                    </template>
                    <template v-else>
                      <template v-if="r.archived">
                        <Button
                          size="sm"
                          variant="secondary"
                          class="max-tab:min-h-11"
                          type="button"
                          @click="command('/unarchive ' + r.id)"
                        >
                          Unarchive
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          class="max-tab:min-h-11"
                          type="button"
                          @click="resume(r.id)"
                        >
                          Resume
                        </Button>
                      </template>
                      <Button
                        v-else-if="r.id === session.id"
                        size="sm"
                        variant="secondary"
                        class="max-tab:min-h-11"
                        type="button"
                        @click="router.push({ name: 'chat' })"
                      >
                        Open
                      </Button>
                      <Button
                        v-else
                        size="sm"
                        variant="secondary"
                        class="max-tab:min-h-11"
                        type="button"
                        @click="resume(r.id)"
                      >
                        Resume
                      </Button>
                      <RowMenu :items="menuItems(r)" @select="(a) => onMenu(r, a)" />
                    </template>
                  </template>
                </template>
              </template>
            </DataTable>
          </div>
        </template>
      </template>
    </template>

    <p class="text-sm text-muted-foreground">
      Telegram, CLI, and cron sessions are read-only here. “Fork into dashboard” copies one into a
      new dashboard session.
    </p>
  </div>
</template>
