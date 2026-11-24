<template>
  <div v-if="open" class="ov scrim pal-ov is-open" @click.self="emit('close')">
    <div ref="pal" class="pal" role="dialog" aria-modal="true" aria-label="Command palette">
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
          role="combobox"
          aria-expanded="true"
          aria-autocomplete="list"
          aria-controls="pal-list"
          :aria-activedescendant="shown.length ? 'pal-opt-' + hi : undefined"
          placeholder="Type a command or search…"
          autocomplete="off"
          spellcheck="false"
          @keydown="onKey"
        />
        <button class="pal-esc" type="button" aria-label="Close" @click="emit('close')">esc</button>
        <button
          type="button"
          class="btn btn-secondary btn-touch only-phone pal-close"
          @click="emit('close')"
        >
          Close
        </button>
      </div>
      <div id="pal-list" ref="list" class="pal-list" role="listbox">
        <template v-if="groups.length">
          <template v-for="g in groups" :key="g.name">
            <div class="pal-group">{{ g.label }}</div>
            <button
              v-for="r in g.rows"
              :key="r.row.group + r.row.label"
              type="button"
              :id="'pal-opt-' + r.index"
              class="pal-row"
              :class="{ 'is-hi': r.index === hi, 'pal-all': !!r.row.more }"
              role="option"
              :aria-selected="r.index === hi"
              @mousemove="hi = r.index"
              @click="run(r.row)"
            >
              <template v-if="r.row.more">
                <span class="pl">Show all {{ g.total }}</span>
              </template>
              <template v-else>
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
              </template>
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
        <span><span class="kbd">↵</span> {{ enterHint }}</span>
        <span><span class="kbd">esc</span> to close</span>
        <span class="grow"></span>
        <span>{{ hits.length }} results</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, reactive, ref, toRef, watch } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import { ICON } from "../lib/icons"
import { useOverlay } from "../lib/overlay"
import { ui } from "../stores/ui"

interface Row {
  group: "Pages" | "Commands" | "Skills"
  label: string
  desc: string
  page?: string
  action?: "tray"
  more?: string
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
  {
    group: "Pages",
    page: "waiting",
    action: "tray",
    label: "Waiting for you",
    desc: "Cards that wait for your answer",
  },
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
  {
    group: "Pages",
    page: "ops",
    label: "System operations",
    desc: "Backup, update, restart, log, and config",
  },
  { group: "Pages", page: "access", label: "Access", desc: "Owners and pairing" },
  { group: "Pages", page: "start", label: "Getting started", desc: "Setup steps" },
]

const query = ref("")
const hi = ref(0)
const input = ref<HTMLInputElement | null>(null)
const list = ref<HTMLElement | null>(null)
const pal = ref<HTMLElement | null>(null)
const extra = ref<Row[]>([])
const CAP = 12
// The groups that show all their rows.
const expanded = reactive(new Set<string>())

useOverlay(toRef(props, "open"), pal, { onClose: () => emit("close"), modal: true })

// Loads the commands and the skills from /api/bootstrap on each open.
async function loadBootstrap(): Promise<void> {
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

// A command or skill row also matches when the query starts with its name and a space.
// The row stays after Tab completes it and the owner types the arguments.
const hits = computed(() => {
  const q = query.value.trim().toLowerCase()
  const raw = query.value.toLowerCase()
  return [...pageRows, ...extra.value].filter(
    (r) =>
      !q ||
      (r.label + " " + r.desc).toLowerCase().includes(q) ||
      (r.group !== "Pages" && raw.startsWith(r.label.toLowerCase() + " ")),
  )
})

const groups = computed(() => {
  const out: { name: string; label: string; total: number; rows: { row: Row; index: number }[] }[] =
    []
  let index = 0
  for (const name of ["Pages", "Commands", "Skills"] as const) {
    const all = hits.value.filter((r) => r.group === name)
    if (!all.length) continue
    const capped = all.length > CAP && !expanded.has(name)
    const rows: Row[] = capped ? all.slice(0, CAP) : all
    if (capped) rows.push({ group: name, label: "", desc: "", more: name })
    out.push({
      name,
      label: capped ? `${name} · ${CAP} of ${all.length}` : name,
      total: all.length,
      rows: rows.map((row) => ({ row, index: index++ })),
    })
  }
  return out
})

const shown = computed(() => groups.value.flatMap((g) => g.rows.map((r) => r.row)))

const enterHint = computed(() => {
  const row = shown.value[hi.value]
  if (row?.more) return "to show all"
  if (row && row.group !== "Pages") return "to insert"
  return "to open"
})

watch(query, () => {
  hi.value = 0
})

watch(
  () => props.open,
  (o) => {
    if (!o) return
    query.value = ""
    hi.value = 0
    expanded.clear()
    void loadBootstrap()
    void nextTick(() => input.value?.focus())
  },
  { immediate: true },
)

// Runs the row: shows all rows of a group, opens the tray or a page, or puts its command in
// the composer draft.
function run(row: Row): void {
  if (row.more) {
    expanded.add(row.more)
    return
  }
  if (row.action === "tray") ui.trayOpen = true
  else if (row.page) void router.push({ name: row.page })
  else {
    const q = query.value.trimStart()
    const draft = q.toLowerCase().startsWith(row.label.toLowerCase() + " ") ? q : row.label + " "
    void router.push({ name: "chat", query: { draft } })
  }
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
  } else if (e.key === "Tab") {
    // Tab completes the highlighted row. A command or a skill gets a trailing space.
    e.preventDefault()
    const row = shown.value[hi.value]
    if (row && !row.more) query.value = row.group === "Pages" ? row.label : row.label + " "
  }
}
</script>
