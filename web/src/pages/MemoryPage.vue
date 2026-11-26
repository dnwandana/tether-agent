<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue"
import { cn } from "@/lib/utils"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Field, FieldError } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Item, ItemActions, ItemContent, ItemGroup } from "@/components/ui/item"
import { Textarea } from "@/components/ui/textarea"
import { api } from "../api/client"
import { useRoutePage } from "../lib/routePage"
import type { Column } from "../lib/table"
import type {
  MemoryEntry,
  MemoryHit,
  MemorySearchView,
  MemoryView,
  TopicRow,
  TopicView,
} from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import ResultNote from "../components/ResultNote.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import type { ResultNoteData } from "../lib/command"
import { useDirty } from "../lib/dirty"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { fullTime, shortTime } from "../lib/time"
import { confirm, toast } from "../stores/ui"

type Target = "rule" | "memory" | "user"

const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const view = ref<MemoryView | null>(null)
const query = ref("")
const hits = ref<MemoryHit[] | null>(null)
// Which entry is in edit mode: "<target>:<id>" for an edit, "<target>:new" for an add.
const editing = ref("")
const draft = ref("")
const fieldError = ref("")
const topicOpen = ref(false)
const topicName = ref("")
const topicText = ref("")
const topicType = ref("memory")
const note = ref<ResultNoteData | null>(null)
const undo = ref<{ target: Target; editId: number } | null>(null)
const saving = ref(false)
const searching = ref(false)
const pulsing = ref("")
const { page, setPage } = useRoutePage("memory")
// The text that the open editor started with. A draft that differs from it is dirty.
const original = ref("")
let timer: ReturnType<typeof setTimeout> | undefined

const files: { target: Target; title: string }[] = [
  { target: "rule", title: "Owner rules · rules.md" },
  { target: "memory", title: "Agent memory · memory.md" },
  { target: "user", title: "User profile · user_profile.md" },
]
const topicColumns: Column[] = [
  { key: "name", label: "Topic", type: "text" },
  { key: "type", label: "Type", type: "badge" },
  { key: "entries", label: "Entries", type: "num" },
  { key: "modified", label: "Modified", type: "date" },
  { key: "actions", label: "", type: "act", width: "6rem" },
]
const t = (x: unknown) => x as TopicRow

// One row for each target, so a new target cannot fall into the wrong branch.
const FILE_NAMES: Record<Target, string> = {
  rule: "rules.md",
  memory: "memory.md",
  user: "user_profile.md",
}
const USED_KEYS = { rule: "rule_used", memory: "memory_used", user: "user_used" } as const
const LIMIT_KEYS = { rule: "rule_limit", memory: "memory_limit", user: "user_limit" } as const

function entriesOf(target: Target): MemoryEntry[] {
  return view.value ? view.value[target] : []
}
function used(target: Target): number {
  return view.value ? view.value[USED_KEYS[target]] : 0
}
function limit(target: Target): number {
  return view.value ? view.value[LIMIT_KEYS[target]] : 0
}
function pct(target: Target): number {
  const l = limit(target)
  return l > 0 ? (used(target) / l) * 100 : 0
}
// Returns "bad" at the limit, "warn" above 85 %, else "".
function meterLevel(target: Target): "bad" | "warn" | "" {
  const p = pct(target)
  return p >= 100 ? "bad" : p > 85 ? "warn" : ""
}
// The inbox note name in the reply of the memory tool.
const INBOX = /\bnote (inbox-[a-z0-9-]+)/
// Cuts a text to n characters and adds an ellipsis.
function cut(text: string, n = 200): string {
  return text.length > n ? text.slice(0, n) + "…" : text
}
const SEP = 3 // the length of "\n§\n" between two entries
// Returns the size of the file after the open editor saves.
function afterSave(target: Target): number {
  const entries = entriesOf(target)
  if (editing.value === `${target}:new`) {
    return used(target) + (entries.length ? SEP : 0) + draft.value.length
  }
  const id = editing.value.slice(target.length + 1)
  const old = entries.find((e) => e.id === id)?.text ?? ""
  return used(target) - old.length + draft.value.length
}
const dirty = computed(() => editing.value !== "" && draft.value !== original.value)
useDirty(dirty)
const num = (n: number) => n.toLocaleString("en-US")

function when(s: string): string {
  return shortTime(s) || "—"
}

async function load(): Promise<void> {
  try {
    view.value = await api<MemoryView>("GET", "/api/memory")
    state.value = "ready"
  } catch (e) {
    if (view.value) {
      toast(friendlyError(e, "/api/memory"), "error")
      return
    }
    errorText.value = friendlyError(e, "/api/memory")
    state.value = "error"
  }
}
const { stale, updatedAt, refresh } = useLiveData(load)

// Tells if the server masked a secret in the text. A save of such text would write the mask.
const MASKED = /«[^»\n]+»|\*\*\*|\[REDACTED[^\]]*\]|\w{6}\.\.\.\w{4}/
function masked(e: MemoryEntry): boolean {
  return e.blocked || MASKED.test(e.text)
}

async function startEdit(key: string, text: string): Promise<void> {
  if (dirty.value) {
    const ok = await confirm({
      title: "Drop the draft?",
      body: "The text that you typed is lost.",
      confirmLabel: "Drop",
      danger: true,
    })
    if (!ok) return
  }
  editing.value = key
  draft.value = text
  original.value = text
  fieldError.value = ""
}
function cancel(): void {
  editing.value = ""
  draft.value = ""
  original.value = ""
  fieldError.value = ""
}
let lastOk = false
let lastEditId = 0

// Posts one memory tool call with all the keys. An edit or a remove names the entry by its id.
async function save(
  target: Target,
  action: "add" | "replace" | "remove",
  content: string,
  oldText: string,
  id = "",
): Promise<void> {
  fieldError.value = ""
  undo.value = null
  lastOk = false
  lastEditId = 0
  saving.value = true
  let reply: MemoryView
  try {
    reply = await api<MemoryView>("POST", "/api/memory", {
      action,
      target,
      content,
      old_text: oldText,
      id,
    })
  } catch (e) {
    const msg = friendlyError(e, "/api/memory")
    if (action === "remove") toast(msg, "error")
    else fieldError.value = msg
    return
  } finally {
    saving.value = false
  }
  lastOk = true
  lastEditId = reply?.edit_ids?.[0] ?? 0
  const inbox = INBOX.exec(reply?.result ?? "")
  if (inbox) {
    note.value = {
      title: "Memory",
      at: new Date(),
      kind: "warn",
      text: `The entry did not fit. It is in the note \`${inbox[1]}\`, which does not load into the prompt. Remove an entry, then move it back.`,
    }
  } else {
    toast(
      action === "remove" ? "Removed. Applies to new sessions." : "Saved. Applies to new sessions.",
      "ok",
    )
  }
  // A remove or an Undo must not drop the draft of another open editor.
  if (action !== "remove") cancel()
  await load()
}

async function remove(target: Target, e: MemoryEntry): Promise<void> {
  const file = FILE_NAMES[target]
  const ok = await confirm({
    title: `Remove the entry from ${file}?`,
    body: "New sessions do not get this entry.",
    confirmLabel: "Remove",
    danger: true,
    items: [e.blocked ? `[BLOCKED ${e.id}]` : cut(e.text)],
  })
  if (!ok) return
  await save(target, "remove", "", e.blocked ? e.id : e.text, e.id)
  if (lastOk && lastEditId > 0 && !e.blocked) undo.value = { target, editId: lastEditId }
}

// Puts a removed entry back through the server, so the entry keeps its place.
async function undoRemove(): Promise<void> {
  const u = undo.value
  if (!u) return
  undo.value = null
  try {
    view.value = await api<MemoryView>("POST", "/api/memory/undo", { edit_id: u.editId })
  } catch (e) {
    toast(friendlyError(e, "/api/memory/undo"), "error")
    return
  }
  toast("Restored. Applies to new sessions.", "ok")
}

let searchSeq = 0

function runSearch(): void {
  clearTimeout(timer)
  const q = query.value.trim()
  const mine = ++searchSeq
  if (!q) {
    hits.value = null
    searching.value = false
    return
  }
  timer = setTimeout(async () => {
    searching.value = true
    // A late reply for an old query must not replace the hits of the current field.
    try {
      const r = await api<MemorySearchView>("GET", `/api/memory/search?q=${encodeURIComponent(q)}`)
      if (query.value.trim() !== q) return
      hits.value = r.hits
    } catch (e) {
      if (query.value.trim() !== q) return
      hits.value = []
      toast(friendlyError(e, "/api/memory/search"), "error")
    } finally {
      // Only the newest request resets the flag.
      if (mine === searchSeq) searching.value = false
    }
  }, 250)
}
watch(query, runSearch)

async function openTopic(name: string): Promise<void> {
  try {
    const r = await api<TopicView>("GET", `/api/memory/topics/${encodeURIComponent(name)}`)
    topicName.value = name
    topicText.value = r.text
    topicType.value = view.value?.topics.find((x) => x.name === name)?.type ?? "memory"
    topicOpen.value = true
  } catch (e) {
    toast(friendlyError(e, "/api/memory/topics"), "error")
  }
}

// Splits the text of a note into its entries. The first line is the note header.
function topicEntries(text: string): string[] {
  const out: string[] = []
  const lines = text.split("\n").slice(1)
  let n = 1
  for (const line of lines) {
    if (line.startsWith(`${n}. `)) {
      out.push(line.slice(`${n}. `.length))
      n++
    } else if (out.length > 0) {
      out[out.length - 1] += "\n" + line
    }
  }
  return out
}
const topicList = computed(() => topicEntries(topicText.value))

async function removeTopicEntry(text: string): Promise<void> {
  const ok = await confirm({
    title: `Remove the entry from the note ${topicName.value}?`,
    body: "New sessions do not get this entry.",
    confirmLabel: "Remove",
    danger: true,
    items: [cut(text)],
  })
  if (!ok) return
  try {
    await api("POST", "/api/memory", {
      action: "remove",
      target: topicType.value,
      topic: topicName.value,
      content: "",
      old_text: text,
      id: "",
    })
    toast("Removed. Applies to new sessions.", "ok")
    await openTopic(topicName.value)
    await load()
  } catch (e) {
    toast(friendlyError(e, "/api/memory"), "error")
  }
}

// Scrolls to the entry of a hit, or opens the note of a hit.
async function openHit(h: MemoryHit): Promise<void> {
  if (h.file !== "memory.md" && h.file !== "user_profile.md") {
    await openTopic(h.file)
    return
  }
  const target: Target = h.file === "memory.md" ? "memory" : "user"
  const e = entriesOf(target).find((x) => x.text === h.entry)
  if (!e) return
  const key = `${target}:${e.id}`
  const reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches
  entryEls.get(key)?.scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "center" })
  pulsing.value = key
  clearTimeout(pulseTimer)
  pulseTimer = setTimeout(() => (pulsing.value = ""), 1300)
}
const entryEls = new Map<string, Element>()
function setEntryEl(key: string, el: unknown): void {
  if (el instanceof Element) entryEls.set(key, el)
  else entryEls.delete(key)
}
let pulseTimer: ReturnType<typeof setTimeout> | undefined

onBeforeUnmount(() => {
  clearTimeout(timer)
  clearTimeout(pulseTimer)
})
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">Memory</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <Input
        v-model="query"
        type="search"
        class="w-full tab:w-64"
        placeholder="Search memory and notes"
        aria-label="Search memory and notes"
      />
    </div>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="load"
    />
    <template v-else-if="view">
      <Card v-if="hits || searching">
        <CardHeader><CardTitle>Search hits</CardTitle></CardHeader>
        <CardContent class="flex flex-col gap-2">
          <p v-if="searching" class="text-sm text-muted-foreground">Searching…</p>
          <template v-if="hits">
            <p v-if="hits.length === 0 && !searching" class="text-sm text-muted-foreground">
              No hits
            </p>
            <div v-for="(h, i) in hits" :key="i" class="flex items-center gap-2">
              <span class="font-mono text-xs">{{ h.file }}</span>
              <Button
                variant="link"
                class="h-auto flex-1 justify-start p-0"
                type="button"
                @click="openHit(h)"
                >{{ h.entry }}</Button
              >
              <span class="text-xs text-muted-foreground">{{ h.match }}</span>
            </div>
          </template>
        </CardContent>
      </Card>

      <Alert>
        <AlertDescription>
          An edit applies to new sessions. A running session keeps its prompt as it is.
        </AlertDescription>
      </Alert>
      <ResultNote :note="note" @close="note = null" />

      <div class="grid gap-4 lg:grid-cols-2">
        <Card
          v-for="fl in files"
          :key="fl.target"
          :class="fl.target === 'rule' ? 'lg:col-span-2' : undefined"
        >
          <CardHeader>
            <CardTitle>{{ fl.title }}</CardTitle>
          </CardHeader>
          <CardContent class="flex flex-col gap-3">
            <div
              class="h-2 overflow-hidden rounded-full bg-muted"
              role="meter"
              :data-level="meterLevel(fl.target)"
              aria-valuemin="0"
              :aria-valuenow="used(fl.target)"
              :aria-valuemax="limit(fl.target)"
              :aria-label="fl.title + ' size'"
            >
              <div
                :class="
                  cn(
                    'h-full bg-primary',
                    meterLevel(fl.target) === 'warn' && 'bg-warning',
                    meterLevel(fl.target) === 'bad' && 'bg-destructive',
                  )
                "
                :style="{ width: Math.min(100, pct(fl.target)) + '%' }"
              />
            </div>
            <div class="text-xs text-muted-foreground">
              {{ num(used(fl.target)) }} / {{ num(limit(fl.target)) }} chars ·
              {{ Math.round(pct(fl.target)) }}% of the limit
            </div>
            <div v-if="pct(fl.target) >= 100" class="text-xs text-destructive">
              The next add goes to the inbox.
            </div>
            <StateBlock
              v-if="entriesOf(fl.target).length === 0 && editing !== `${fl.target}:new`"
              state="empty"
              text="No entries yet."
              action="+ Add entry"
              @action="startEdit(`${fl.target}:new`, '')"
            />
            <ItemGroup>
              <Item
                v-for="e in entriesOf(fl.target)"
                :key="e.id"
                :ref="(el) => setEntryEl(`${fl.target}:${e.id}`, (el as any)?.$el ?? el)"
                role="listitem"
                size="sm"
                :class="{ pulse: pulsing === `${fl.target}:${e.id}` }"
                :data-entry="`${fl.target}:${e.id}`"
              >
                <template v-if="e.blocked">
                  <ItemContent
                    ><span
                      ><b>[BLOCKED {{ e.id }}]</b> {{ " "
                      }}<span class="text-muted-foreground"
                        >the memory scan blocked this entry</span
                      ></span
                    ></ItemContent
                  >
                  <ItemActions>
                    <Button
                      variant="destructive"
                      size="sm"
                      class="max-tab:h-11"
                      type="button"
                      @click="remove(fl.target, e)"
                    >
                      Remove
                    </Button>
                  </ItemActions>
                </template>
                <template v-else-if="editing === `${fl.target}:${e.id}`">
                  <ItemContent>
                    <Field :data-invalid="!!fieldError || undefined">
                      <Textarea
                        v-model="draft"
                        rows="4"
                        :maxlength="view.entry_limit"
                        aria-label="Entry text"
                        :aria-invalid="!!fieldError || undefined"
                      />
                      <div class="text-xs text-muted-foreground">
                        After save: {{ num(afterSave(fl.target)) }} / {{ num(limit(fl.target)) }}
                      </div>
                      <FieldError v-if="fieldError">{{ fieldError }}</FieldError>
                    </Field>
                  </ItemContent>
                  <ItemActions>
                    <Button
                      variant="outline"
                      size="sm"
                      class="max-tab:h-11"
                      type="button"
                      @click="cancel"
                      >Cancel</Button
                    >
                    <Button
                      size="sm"
                      class="max-tab:h-11"
                      type="button"
                      :disabled="saving"
                      @click="save(fl.target, 'replace', draft, e.text, e.id)"
                    >
                      {{ saving ? "Saving…" : "Save" }}
                    </Button>
                  </ItemActions>
                </template>
                <template v-else>
                  <ItemContent class="whitespace-pre-wrap break-words">{{ e.text }}</ItemContent>
                  <span v-if="masked(e)" class="text-xs text-muted-foreground"
                    >Masked secret. Edit it with the memory tool.</span
                  >
                  <ItemActions>
                    <Button
                      variant="outline"
                      size="sm"
                      class="max-tab:h-11"
                      type="button"
                      :disabled="masked(e)"
                      :title="
                        masked(e)
                          ? 'This entry holds a masked secret. Edit it with the memory tool.'
                          : undefined
                      "
                      @click="startEdit(`${fl.target}:${e.id}`, e.text)"
                    >
                      Edit
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      class="max-tab:h-11"
                      type="button"
                      @click="remove(fl.target, e)"
                    >
                      Remove
                    </Button>
                  </ItemActions>
                </template>
              </Item>
              <Item v-if="undo?.target === fl.target" role="listitem" size="sm">
                <ItemContent>Removed an entry.</ItemContent>
                <ItemActions>
                  <Button
                    variant="outline"
                    size="sm"
                    class="max-tab:h-11"
                    type="button"
                    @click="undoRemove"
                  >
                    Undo
                  </Button>
                </ItemActions>
              </Item>
              <Item v-if="editing === `${fl.target}:new`" role="listitem" size="sm">
                <ItemContent>
                  <Field :data-invalid="!!fieldError || undefined">
                    <Textarea
                      v-model="draft"
                      rows="4"
                      :maxlength="view.entry_limit"
                      aria-label="New entry text"
                      :aria-invalid="!!fieldError || undefined"
                    />
                    <div class="text-xs text-muted-foreground">
                      After save: {{ num(afterSave(fl.target)) }} / {{ num(limit(fl.target)) }}
                    </div>
                    <FieldError v-if="fieldError">{{ fieldError }}</FieldError>
                  </Field>
                </ItemContent>
                <ItemActions>
                  <Button
                    variant="outline"
                    size="sm"
                    class="max-tab:h-11"
                    type="button"
                    @click="cancel"
                    >Cancel</Button
                  >
                  <Button
                    size="sm"
                    class="max-tab:h-11"
                    type="button"
                    :disabled="saving"
                    @click="save(fl.target, 'add', draft, '')"
                  >
                    {{ saving ? "Saving…" : "Save" }}
                  </Button>
                </ItemActions>
              </Item>
            </ItemGroup>
          </CardContent>
          <CardFooter>
            <Button
              variant="outline"
              size="sm"
              class="max-tab:h-11"
              type="button"
              @click="startEdit(`${fl.target}:new`, '')"
            >
              + Add entry
            </Button>
          </CardFooter>
        </Card>
      </div>

      <div class="flex items-center gap-2">
        <h2 class="text-lg font-semibold">Topic notes</h2>
        <span class="font-mono text-xs text-muted-foreground">memory/notes/</span>
      </div>
      <StateBlock v-if="view.topics.length === 0" state="empty" text="No topic notes" />
      <DataTable
        v-else
        :columns="topicColumns"
        :rows="view.topics as unknown as Record<string, unknown>[]"
        row-key="name"
        page-key="memory-topics"
        page-label="Topic notes"
        :page="page"
        @update:page="setPage"
      >
        <template #cell-name="{ row }">
          <div class="flex min-w-0 items-center gap-2">
            <span class="min-w-0 truncate font-mono" :title="t(row).name">{{ t(row).name }}</span>
            <Badge
              v-if="t(row).name.startsWith('inbox-')"
              variant="outline"
              class="shrink-0 border-warning/40 text-warning"
              data-tone="warn"
              >inbox</Badge
            >
            <Badge
              v-if="t(row).held"
              variant="outline"
              class="shrink-0 border-warning/40 text-warning"
              data-tone="warn"
              >held, not used by the agent</Badge
            >
          </div>
        </template>
        <template #cell-modified="{ row }"
          ><span
            data-test="modified"
            class="whitespace-nowrap"
            :title="fullTime(t(row).modified)"
            >{{ when(t(row).modified) }}</span
          ></template
        >
        <template #cell-actions="{ row }">
          <Button
            variant="outline"
            size="sm"
            class="max-tab:h-11"
            type="button"
            @click.stop="openTopic(t(row).name)"
          >
            Open
          </Button>
        </template>
      </DataTable>
    </template>

    <SidePanel :open="topicOpen" :title="`memory/notes/${topicName}.md`" @close="topicOpen = false">
      <ItemGroup v-if="topicList.length > 0" aria-label="Entries of the note">
        <Item v-for="(text, i) in topicList" :key="i" role="listitem" size="sm">
          <ItemContent class="whitespace-pre-wrap break-words">{{ text }}</ItemContent>
          <span v-if="MASKED.test(text)" class="text-xs text-muted-foreground"
            >Masked secret. Edit it with the memory tool.</span
          >
          <ItemActions>
            <Button
              variant="destructive"
              size="sm"
              class="max-tab:h-11"
              type="button"
              :disabled="MASKED.test(text)"
              @click="removeTopicEntry(text)"
            >
              Remove
            </Button>
          </ItemActions>
        </Item>
      </ItemGroup>
      <pre
        v-else
        class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
        >{{ topicText }}</pre>
    </SidePanel>
  </div>
</template>
