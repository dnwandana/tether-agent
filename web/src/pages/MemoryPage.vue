<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue"
import { api, ApiError } from "../api/client"
import type {
  MemoryEntry,
  MemoryHit,
  MemorySearchView,
  MemoryView,
  TopicRow,
  TopicView,
} from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import SidePanel from "../components/SidePanel.vue"
import StateBlock from "../components/StateBlock.vue"
import { toast } from "../stores/ui"

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
  return l > 0 ? Math.min(100, (used(target) / l) * 100) : 0
}
const num = (n: number) => n.toLocaleString("en-US")

function when(s: string): string {
  const d = new Date(s)
  return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? "—" : d.toLocaleString()
}

async function load(): Promise<void> {
  try {
    view.value = await api<MemoryView>("GET", "/api/memory")
    state.value = "ready"
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

// Tells if the server masked a secret in the text. A save of such text would write the mask.
const MASKED = /«[^»\n]+»|\*\*\*|\[REDACTED[^\]]*\]|\w{6}\.\.\.\w{4}/
function masked(e: MemoryEntry): boolean {
  return e.blocked || MASKED.test(e.text)
}

function startEdit(key: string, text: string): void {
  editing.value = key
  draft.value = text
  fieldError.value = ""
}
function cancel(): void {
  editing.value = ""
  draft.value = ""
  fieldError.value = ""
}

// Posts one memory tool call with all the keys. An edit or a remove names the entry by its id.
async function save(
  target: Target,
  action: "add" | "replace" | "remove",
  content: string,
  oldText: string,
  id = "",
): Promise<void> {
  fieldError.value = ""
  try {
    await api("POST", "/api/memory", { action, target, content, old_text: oldText, id })
  } catch (e) {
    const msg = e instanceof ApiError ? e.message : String(e)
    if (action === "remove") toast(msg, "error")
    else fieldError.value = msg
    return
  }
  toast(
    action === "remove" ? "Removed. Applies to new sessions." : "Saved. Applies to new sessions.",
  )
  cancel()
  await load()
}

function remove(target: Target, e: MemoryEntry): Promise<void> {
  return save(target, "remove", "", e.blocked ? e.id : e.text, e.id)
}

function runSearch(): void {
  clearTimeout(timer)
  const q = query.value.trim()
  if (!q) {
    hits.value = null
    return
  }
  timer = setTimeout(async () => {
    // A late reply for an old query must not replace the hits of the current field.
    try {
      const r = await api<MemorySearchView>("GET", `/api/memory/search?q=${encodeURIComponent(q)}`)
      if (query.value.trim() !== q) return
      hits.value = r.hits
    } catch (e) {
      if (query.value.trim() !== q) return
      hits.value = []
      toast(e instanceof ApiError ? e.message : String(e), "error")
    }
  }, 250)
}
watch(query, runSearch)

async function openTopic(name: string): Promise<void> {
  try {
    const r = await api<TopicView>("GET", `/api/memory/topics/${encodeURIComponent(name)}`)
    topicName.value = name
    topicText.value = r.text
    topicOpen.value = true
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
}

onMounted(load)
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Memory</h1>
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
      @retry="load"
    />
    <template v-else-if="view">
      <div v-if="hits" class="card">
        <div class="card-h"><h3>Search hits</h3></div>
        <div v-if="hits.length === 0" class="li c-3">No hits</div>
        <div v-for="(h, i) in hits" :key="i" class="li">
          <span class="mono">{{ h.file }}</span>
          <span class="grow">{{ h.entry }}</span>
          <span class="c-3 t-12">{{ h.match }}</span>
        </div>
      </div>

      <div class="warn-banner">
        An edit applies to new sessions. A running session keeps its prompt as it is.
      </div>

      <div class="grid-2">
        <div v-for="fl in files" :key="fl.target" class="card">
          <div class="card-h">
            <h3>{{ fl.title }}</h3>
            <span class="grow" />
            <span class="c-3 t-12"
              >{{ num(used(fl.target)) }} / {{ num(limit(fl.target)) }} chars</span
            >
          </div>
          <div class="card-b">
            <div class="meter" :class="{ 'meter-warn': pct(fl.target) > 85 }">
              <span :style="{ width: pct(fl.target) + '%' }" />
            </div>
          </div>
          <div v-for="e in entriesOf(fl.target)" :key="e.id" class="li">
            <template v-if="e.blocked">
              <span class="grow"
                ><b>[BLOCKED {{ e.id }}]</b>
                <span class="c-3">the memory scan blocked this entry</span></span
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
                <textarea v-model="draft" class="input" rows="4" aria-label="Entry text" />
                <div v-if="fieldError" class="t-12 c-bad">{{ fieldError }}</div>
              </div>
              <button class="btn btn-secondary btn-sm" type="button" @click="cancel">Cancel</button>
              <button
                class="btn btn-primary btn-sm"
                type="button"
                @click="save(fl.target, 'replace', draft, e.text, e.id)"
              >
                Save
              </button>
            </template>
            <template v-else>
              <span class="grow">{{ e.text }}</span>
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
          <div v-if="editing === `${fl.target}:new`" class="li">
            <div class="grow">
              <textarea v-model="draft" class="input" rows="4" aria-label="New entry text" />
              <div v-if="fieldError" class="t-12 c-bad">{{ fieldError }}</div>
            </div>
            <button class="btn btn-secondary btn-sm" type="button" @click="cancel">Cancel</button>
            <button
              class="btn btn-primary btn-sm"
              type="button"
              @click="save(fl.target, 'add', draft, '')"
            >
              Save
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
          ><span class="mono">{{ t(row).name }}</span></template
        >
        <template #cell-modified="{ row }">{{ when(t(row).modified) }}</template>
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
      <div class="code-block">{{ topicText }}</div>
    </SidePanel>
  </div>
</template>
