<template>
  <div v-if="open" class="ov scrim pal-ov is-open" @click.self="emit('close')">
    <div class="pal" role="dialog" aria-modal="true" aria-label="Command palette">
      <div class="pal-in">
        <span class="pal-glass">
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.75"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path :d="ICON.search" />
          </svg>
        </span>
        <input
          ref="input"
          v-model="query"
          placeholder="Type a command or search…"
          autocomplete="off"
          spellcheck="false"
          @keydown="onKey"
        />
        <button class="pal-esc" type="button" aria-label="Close" @click="emit('close')">esc</button>
      </div>
      <div ref="list" class="pal-list" role="listbox">
        <template v-if="groups.length">
          <template v-for="g in groups" :key="g.name">
            <div class="pal-group">{{ g.name }}</div>
            <button
              v-for="r in g.rows"
              :key="r.row.group + r.row.label"
              type="button"
              class="pal-row"
              :class="{ 'is-hi': r.index === hi }"
              role="option"
              :aria-selected="r.index === hi"
              @mousemove="hi = r.index"
              @click="run(r.row)"
            >
              <span class="pi-ico">
                <svg
                  v-if="r.row.page"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.75"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  aria-hidden="true"
                >
                  <path :d="ICON[r.row.page]" />
                </svg>
                <span v-else class="slash">/</span>
              </span>
              <span class="pl">{{ r.row.label }}</span>
              <span class="pd">{{ r.row.desc }}</span>
              <span class="pk"><span class="kbd pk-enter">↵</span></span>
            </button>
          </template>
        </template>
        <div v-else class="pal-empty">
          <b>No match for “{{ query.trim() }}”</b>
          <span class="t-12 c-3">Try a page name, or a command such as /usage.</span>
        </div>
      </div>
      <div class="pal-foot">
        <span><span class="kbd">↑</span><span class="kbd">↓</span> to move</span>
        <span><span class="kbd">↵</span> to run</span>
        <span><span class="kbd">esc</span> to close</span>
        <span class="grow"></span>
        <span>{{ hits.length }} results</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import { ICON } from "../lib/icons"

interface Row {
  group: "Pages" | "Commands" | "Skills"
  label: string
  desc: string
  page?: string
}
interface CommandRow {
  name: string
  description: string
}

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()

const pageRows: Row[] = [
  { group: "Pages", page: "chat", label: "Chat", desc: "Talk to the agent" },
  { group: "Pages", page: "sessions", label: "Sessions", desc: "Past conversations" },
  { group: "Pages", page: "jobs", label: "Jobs", desc: "Scheduled and watch jobs" },
  { group: "Pages", page: "goals", label: "Goals", desc: "Standing goals and their state" },
  {
    group: "Pages",
    page: "security",
    label: "Approvals and security",
    desc: "Modes, grants, and the decision log",
  },
  { group: "Pages", page: "skills", label: "Skills", desc: "Installed skills and the catalog" },
  { group: "Pages", page: "memory", label: "Memory", desc: "Topics the agent remembers" },
  { group: "Pages", page: "usage", label: "Usage", desc: "Tokens and cost over time" },
  { group: "Pages", page: "models", label: "Models", desc: "Provider entries and health" },
  { group: "Pages", page: "system", label: "System", desc: "Doctor, servers, and runtime" },
  { group: "Pages", page: "access", label: "Access", desc: "Owners and pairing" },
  { group: "Pages", page: "start", label: "Getting started", desc: "Setup steps" },
]

const query = ref("")
const hi = ref(0)
const input = ref<HTMLInputElement | null>(null)
const list = ref<HTMLElement | null>(null)
const extra = ref<Row[]>([])
let loaded = false

// Loads the commands and the skills from /api/bootstrap once for this component.
async function loadBootstrap(): Promise<void> {
  if (loaded) return
  loaded = true
  try {
    const b = await api<{ commands: CommandRow[]; skills: CommandRow[] }>("GET", "/api/bootstrap")
    extra.value = [
      ...(b.commands ?? []).map((c) => ({
        group: "Commands" as const,
        label: c.name,
        desc: c.description,
      })),
      ...(b.skills ?? []).map((c) => ({
        group: "Skills" as const,
        label: c.name,
        desc: c.description,
      })),
    ]
  } catch {
    // A load error leaves the Pages group only.
    extra.value = []
  }
}

const hits = computed(() => {
  const q = query.value.trim().toLowerCase()
  return [...pageRows, ...extra.value].filter(
    (r) => !q || (r.label + " " + r.desc).toLowerCase().includes(q),
  )
})

const groups = computed(() => {
  const max = query.value.trim() ? 8 : 12
  const out: { name: string; rows: { row: Row; index: number }[] }[] = []
  let index = 0
  for (const name of ["Pages", "Commands", "Skills"]) {
    const rows = hits.value.filter((r) => r.group === name).slice(0, max)
    if (rows.length) out.push({ name, rows: rows.map((row) => ({ row, index: index++ })) })
  }
  return out
})

const shown = computed(() => groups.value.flatMap((g) => g.rows.map((r) => r.row)))

watch(query, () => {
  hi.value = 0
})

watch(
  () => props.open,
  (o) => {
    if (!o) return
    query.value = ""
    hi.value = 0
    void loadBootstrap()
    void nextTick(() => input.value?.focus())
  },
  { immediate: true },
)

// Runs the row: goes to its page, or puts its command in the composer draft.
function run(row: Row): void {
  if (row.page) void router.push({ name: row.page })
  else void router.push({ name: "chat", query: { draft: row.label + " " } })
  emit("close")
}

function move(d: number): void {
  const n = shown.value.length
  if (!n) return
  hi.value = (hi.value + d + n) % n
  void nextTick(() => {
    const el = list.value?.querySelectorAll(".pal-row")[hi.value] as HTMLElement | undefined
    if (el && typeof el.scrollIntoView === "function") el.scrollIntoView({ block: "nearest" })
  })
}

function onKey(e: KeyboardEvent): void {
  if (e.key === "ArrowDown") {
    e.preventDefault()
    move(1)
  } else if (e.key === "ArrowUp") {
    e.preventDefault()
    move(-1)
  } else if (e.key === "Enter") {
    e.preventDefault()
    const row = shown.value[hi.value]
    if (row) run(row)
  } else if (e.key === "Escape") {
    e.preventDefault()
    emit("close")
  }
}
</script>
