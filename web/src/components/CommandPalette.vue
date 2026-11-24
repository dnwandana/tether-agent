<template>
  <Dialog :open="open" @update:open="(v: boolean) => !v && emit('close')">
    <DialogContent
      class="gap-0 overflow-hidden p-0"
      :show-close-button="false"
      @pointer-down-outside="guardOutside"
      @interact-outside="guardOutside"
      @close-auto-focus="onCloseAutoFocus"
    >
      <DialogHeader class="sr-only">
        <DialogTitle>Command palette</DialogTitle>
        <DialogDescription>Type a command or search…</DialogDescription>
      </DialogHeader>
      <Command @highlight="onHighlight">
        <div
          class="flex items-center gap-2 border-b px-3 py-2 [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-muted-foreground"
          @keydown.capture="onWrapKey"
        >
          <component :is="ICONS.search" />
          <ListboxFilter
            v-model="query"
            auto-focus
            role="combobox"
            aria-expanded="true"
            aria-autocomplete="list"
            aria-controls="pal-list"
            placeholder="Type a command or search…"
            autocomplete="off"
            spellcheck="false"
            class="min-w-0 flex-1 bg-transparent text-base outline-hidden placeholder:text-muted-foreground tab:text-sm"
            @keydown.tab.prevent="complete"
          />
          <Button
            type="button"
            variant="ghost"
            size="sm"
            class="max-tab:hidden"
            aria-label="Close"
            @click="emit('close')"
          >
            esc
          </Button>
          <Button
            type="button"
            variant="secondary"
            class="tab:hidden min-h-11"
            @click="emit('close')"
          >
            Close
          </Button>
        </div>
        <CommandList id="pal-list">
          <template v-if="groups.length">
            <CommandGroup v-for="g in groups" :key="g.name" :heading="g.label">
              <CommandItem
                v-for="r in g.rows"
                :key="keyOf(r.row)"
                :value="keyOf(r.row)"
                @select="run(r.row)"
              >
                <template v-if="r.row.more">
                  <span>Show all {{ g.total }}</span>
                </template>
                <template v-else>
                  <component :is="ICONS[r.row.page]" v-if="r.row.page" />
                  <span v-else class="w-4 shrink-0 text-center text-muted-foreground">/</span>
                  <span data-label class="font-medium">{{ r.row.label }}</span>
                  <span class="truncate text-xs text-muted-foreground">{{ r.row.desc }}</span>
                  <Kbd class="ml-auto">↵</Kbd>
                </template>
              </CommandItem>
            </CommandGroup>
          </template>
          <div v-else class="flex flex-col items-center gap-1 px-4 py-8 text-center">
            <b>No match for “{{ query.trim() }}”</b>
            <span class="text-xs text-muted-foreground">
              Try a page name, or a command such as /usage.
            </span>
          </div>
        </CommandList>
        <div class="flex items-center gap-4 border-t px-3 py-2 text-xs text-muted-foreground">
          <span><Kbd>↑</Kbd><Kbd>↓</Kbd> to move</span>
          <span><Kbd>↵</Kbd> {{ enterHint }}</span>
          <span><Kbd>esc</Kbd> to close</span>
          <span class="ml-auto">{{ hits.length }} results</span>
        </div>
      </Command>
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
import { ListboxFilter } from "reka-ui"
import { computed, reactive, ref, toRef, watch } from "vue"
import { useRouter } from "vue-router"
import { api } from "../api/client"
import { Button } from "@/components/ui/button"
import { Command, CommandGroup, CommandItem, CommandList } from "@/components/ui/command"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Kbd } from "@/components/ui/kbd"
import { guardOutside, rememberOpener, useReturnFocus } from "../lib/layer"
import { ICONS, type IconName } from "../lib/icons"
import { ui } from "../stores/ui"

interface Row {
  group: "Pages" | "Commands" | "Skills"
  label: string
  desc: string
  page?: IconName
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
// The key of the highlighted row.
const hiKey = ref<string | undefined>()
// The element that had the focus when the palette opened.
let openedFrom: HTMLElement | null = null
const extra = ref<Row[]>([])
const CAP = 12
// The groups that show all their rows.
const expanded = reactive(new Set<string>())

const { onCloseAutoFocus } = useReturnFocus(toRef(props, "open"))

// Gives a row a stable value.
function keyOf(row: Row): string {
  return row.more ? "more:" + row.more : row.group + ":" + row.label
}

function onHighlight(p?: { value: unknown }): void {
  hiKey.value = p ? String(p.value) : undefined
}

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
  const row = shown.value.find((r) => keyOf(r) === hiKey.value)
  if (row?.more) return "to show all"
  if (row && row.group !== "Pages") return "to insert"
  return "to open"
})

watch(
  () => props.open,
  (o) => {
    if (!o) return
    openedFrom = document.activeElement as HTMLElement | null
    query.value = ""
    hiKey.value = undefined
    expanded.clear()
    void loadBootstrap()
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
  if (row.action === "tray") {
    rememberOpener(openedFrom)
    ui.trayOpen = true
  } else if (row.page) void router.push({ name: row.page })
  else {
    const q = query.value.trimStart()
    const draft = q.toLowerCase().startsWith(row.label.toLowerCase() + " ") ? q : row.label + " "
    void router.push({ name: "chat", query: { draft } })
  }
  emit("close")
}

// Tab completes the highlighted row. A command or a skill gets a trailing space.
function complete(): void {
  const row = shown.value.find((r) => keyOf(r) === hiKey.value)
  if (row && !row.more) query.value = row.group === "Pages" ? row.label : row.label + " "
}

// Wraps the highlight at the ends. Reka handles the Home and End keys.
function onWrapKey(e: KeyboardEvent): void {
  const list = shown.value
  if (!list.length) return
  const down = e.key === "ArrowDown" && hiKey.value === keyOf(list[list.length - 1])
  const up = e.key === "ArrowUp" && hiKey.value === keyOf(list[0])
  if (!down && !up) return
  e.preventDefault()
  e.stopPropagation()
  e.target?.dispatchEvent(
    new KeyboardEvent("keydown", { key: down ? "Home" : "End", bubbles: true, cancelable: true }),
  )
}
</script>
