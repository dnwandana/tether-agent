<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue"
import { api } from "../api/client"
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

type Target = "memory" | "user"

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
const undo = ref<{ target: Target; text: string } | null>(null)
const saving = ref(false)
const searching = ref(false)
const pulsing = ref("")
// The text that the open editor started with. A draft that differs from it is dirty.
const original = ref("")
let timer: ReturnType<typeof setTimeout> | undefined

const files: { target: Target; title: string }[] = [
  { target: "memory", title: "Agent memory · memory.md" },
  { target: "user", title: "User profile · user_profile.md" },
]
const topicColumns = [
  { key: "name", label: "Topic" },
  { key: "type", label: "Type" },
  { key: "entries", label: "Entries", align: "right" as const },
  { key: "modified", label: "Modified" },
  { key: "actions", label: "" },
]
const t = (x: unknown) => x as TopicRow

function entriesOf(target: Target): MemoryEntry[] {
  return view.value ? view.value[target] : []
}
function used(target: Target): number {
  return view.value ? (target === "memory" ? view.value.memory_used : view.value.user_used) : 0
}
function limit(target: Target): number {
  return view.value ? (target === "memory" ? view.value.memory_limit : view.value.user_limit) : 0
}
function pct(target: Target): number {
  const l = limit(target)
  return l > 0 ? (used(target) / l) * 100 : 0
}
function meterClass(target: Target): string {
  const p = pct(target)
  return p >= 100 ? "meter-bad" : p > 85 ? "meter-warn" : ""
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
  const file = target === "memory" ? "memory.md" : "user_profile.md"
  const ok = await confirm({
    title: `Remove the entry from ${file}?`,
    body: "New sessions do not get this entry.",
    confirmLabel: "Remove",
    danger: true,
    items: [e.blocked ? `[BLOCKED ${e.id}]` : cut(e.text)],
  })
  if (!ok) return
  await save(target, "remove", "", e.blocked ? e.id : e.text, e.id)
  if (lastOk && !masked(e)) undo.value = { target, text: e.text }
}

// Puts a removed entry back. The mask never reaches here, because a masked entry has no Undo.
async function undoRemove(): Promise<void> {
  const u = undo.value
  if (!u) return
  undo.value = null
  try {
    await api("POST", "/api/memory", {
      action: "add",
      target: u.target,
      content: u.text,
      old_text: "",
      id: "",
    })
  } catch (e) {
    toast(friendlyError(e, "/api/memory"), "error")
    return
  }
  await load()
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
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Memory</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <div class="search">
        <input
          v-model="query"
          class="input"
          type="search"
          placeholder="Search memory and notes"
          aria-label="Search memory and notes"
        />
      </div>
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
      <div v-if="hits || searching" class="card hits">
        <div class="card-h"><h3>Search hits</h3></div>
        <div v-if="searching" class="li c-3">Searching…</div>
        <template v-if="hits">
          <div v-if="hits.length === 0 && !searching" class="li c-3">No hits</div>
          <div v-for="(h, i) in hits" :key="i" class="li">
            <span class="mono">{{ h.file }}</span>
            <button class="link grow" type="button" @click="openHit(h)">{{ h.entry }}</button>
            <span class="c-3 t-12">{{ h.match }}</span>
          </div>
        </template>
      </div>

      <div class="warn-banner">
        An edit applies to new sessions. A running session keeps its prompt as it is.
      </div>
      <ResultNote :note="note" @close="note = null" />

      <div class="grid-2">
        <div v-for="fl in files" :key="fl.target" class="card">
          <div class="card-h">
            <h3>{{ fl.title }}</h3>
          </div>
          <div class="card-b">
            <div
              class="meter"
              :class="meterClass(fl.target)"
              role="meter"
              aria-valuemin="0"
              :aria-valuenow="used(fl.target)"
              :aria-valuemax="limit(fl.target)"
              :aria-label="fl.title + ' size'"
            >
              <span :style="{ width: Math.min(100, pct(fl.target)) + '%' }" />
            </div>
            <div class="t-12 c-3">
              {{ num(used(fl.target)) }} / {{ num(limit(fl.target)) }} chars ·
              {{ Math.round(pct(fl.target)) }}% of the limit
            </div>
            <div v-if="pct(fl.target) >= 100" class="t-12 c-bad">
              The next add goes to the inbox.
            </div>
          </div>
          <StateBlock
            v-if="entriesOf(fl.target).length === 0 && editing !== `${fl.target}:new`"
            state="empty"
            text="No entries yet."
            action="+ Add entry"
            @action="startEdit(`${fl.target}:new`, '')"
          />
          <div
            v-for="e in entriesOf(fl.target)"
            :key="e.id"
            :ref="(el) => setEntryEl(`${fl.target}:${e.id}`, el)"
            class="li"
            :class="{ pulse: pulsing === `${fl.target}:${e.id}` }"
            :data-entry="`${fl.target}:${e.id}`"
          >
            <template v-if="e.blocked">
              <span class="grow"
                ><b>[BLOCKED {{ e.id }}]</b> {{ " "
                }}<span class="c-3">the memory scan blocked this entry</span></span
              >
              <button
                class="btn btn-danger-soft btn-sm"
                type="button"
                @click="remove(fl.target, e)"
              >
                Remove
              </button>
            </template>
            <template v-else-if="editing === `${fl.target}:${e.id}`">
              <div class="grow">
                <textarea
                  v-model="draft"
                  class="input"
                  rows="4"
                  :maxlength="view.entry_limit"
                  aria-label="Entry text"
                />
                <div class="t-12 c-3">
                  After save: {{ num(afterSave(fl.target)) }} / {{ num(limit(fl.target)) }}
                </div>
                <div v-if="fieldError" class="t-12 c-bad">{{ fieldError }}</div>
              </div>
              <button class="btn btn-secondary btn-sm" type="button" @click="cancel">Cancel</button>
              <button
                class="btn btn-primary btn-sm"
                type="button"
                :disabled="saving"
                @click="save(fl.target, 'replace', draft, e.text, e.id)"
              >
                {{ saving ? "Saving…" : "Save" }}
              </button>
            </template>
            <template v-else>
              <span class="grow note-text">{{ e.text }}</span>
              <span v-if="masked(e)" class="t-12 c-3"
                >Masked secret. Edit it with the memory tool.</span
              >
              <button
                class="btn btn-secondary btn-sm"
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
              </button>
              <button
                class="btn btn-danger-soft btn-sm"
                type="button"
                @click="remove(fl.target, e)"
              >
                Remove
              </button>
            </template>
          </div>
          <div v-if="undo?.target === fl.target" class="li">
            <span class="grow">Removed an entry.</span>
            <button class="btn btn-secondary btn-sm" type="button" @click="undoRemove">
              ↩️ Undo
            </button>
          </div>
          <div v-if="editing === `${fl.target}:new`" class="li">
            <div class="grow">
              <textarea
                v-model="draft"
                class="input"
                rows="4"
                :maxlength="view.entry_limit"
                aria-label="New entry text"
              />
              <div class="t-12 c-3">
                After save: {{ num(afterSave(fl.target)) }} / {{ num(limit(fl.target)) }}
              </div>
              <div v-if="fieldError" class="t-12 c-bad">{{ fieldError }}</div>
            </div>
            <button class="btn btn-secondary btn-sm" type="button" @click="cancel">Cancel</button>
            <button
              class="btn btn-primary btn-sm"
              type="button"
              :disabled="saving"
              @click="save(fl.target, 'add', draft, '')"
            >
              {{ saving ? "Saving…" : "Save" }}
            </button>
          </div>
          <div class="card-f">
            <button
              class="btn btn-secondary btn-sm"
              type="button"
              @click="startEdit(`${fl.target}:new`, '')"
            >
              + Add entry
            </button>
          </div>
        </div>
      </div>

      <div class="section-h">
        <h2>Topic notes</h2>
        <span class="c-3 t-12 mono">memory/notes/</span>
      </div>
      <StateBlock v-if="view.topics.length === 0" state="empty" text="No topic notes" />
      <DataTable
        v-else
        :columns="topicColumns"
        :rows="view.topics as unknown as Record<string, unknown>[]"
        row-key="name"
      >
        <template #cell-name="{ row }"
          ><span class="mono">{{ t(row).name }}</span> {{ " "
          }}<span v-if="t(row).name.startsWith('inbox-')" class="chip chip-warn"
            >inbox</span
          ></template
        >
        <template #cell-modified="{ row }"
          ><span
            data-test="modified"
            style="white-space: nowrap"
            :title="fullTime(t(row).modified)"
            >{{ when(t(row).modified) }}</span
          ></template
        >
        <template #cell-actions="{ row }">
          <button
            class="btn btn-secondary btn-sm"
            type="button"
            @click.stop="openTopic(t(row).name)"
          >
            Open
          </button>
        </template>
      </DataTable>
    </template>

    <SidePanel :open="topicOpen" :title="`memory/notes/${topicName}.md`" @close="topicOpen = false">
      <template v-if="topicList.length > 0">
        <div v-for="(text, i) in topicList" :key="i" class="li topic-entry">
          <span class="grow note-text">{{ text }}</span>
          <span v-if="MASKED.test(text)" class="t-12 c-3"
            >Masked secret. Edit it with the memory tool.</span
          >
          <button
            class="btn btn-danger-soft btn-sm"
            type="button"
            :disabled="MASKED.test(text)"
            @click="removeTopicEntry(text)"
          >
            Remove
          </button>
        </div>
      </template>
      <div v-else class="code-block">{{ topicText }}</div>
    </SidePanel>
  </div>
</template>
