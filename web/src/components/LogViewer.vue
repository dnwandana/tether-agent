<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, useId, watch } from "vue"
import { api } from "../api/client"
import { setFollowLogs } from "../api/stream"
import type { LogEvent, LogLineView, LogView } from "../api/types.gen"
import { onEvent } from "../lib/bus"
import { friendlyError } from "../lib/errors"
import { cn } from "@/lib/utils"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import StateBlock from "./StateBlock.vue"

const levels = [
  { label: "All", value: "" },
  { label: "Debug+", value: "debug" },
  { label: "Info+", value: "info" },
  { label: "Warn+", value: "warn" },
  { label: "Error", value: "error" },
]

const MAX = 2000

const level = ref("")
const q = ref("")
const follow = ref(false)
const wrap = ref(window.matchMedia?.("(max-width: 599px)").matches ?? false)
const lines = ref<LogLineView[]>([])
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const box = ref<HTMLElement | null>(null)

let stopLog: (() => void) | null = null
let filterTimer: ReturnType<typeof setTimeout> | null = null

const pad = (n: number): string => String(n).padStart(2, "0")

// Formats the time of a line as local HH:MM:SS, with the date when the line is not from today.
function when(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ""
  const t = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  const n = new Date()
  const today =
    d.getFullYear() === n.getFullYear() &&
    d.getMonth() === n.getMonth() &&
    d.getDate() === n.getDate()
  if (today) return t
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${t}`
}

let seq = 0

async function load(): Promise<void> {
  const mine = ++seq
  try {
    const qs = new URLSearchParams({ level: level.value, q: q.value, limit: "500" })
    const v = await api<LogView>("GET", "/api/logs?" + qs.toString())
    if (mine !== seq) return
    lines.value = (v.lines ?? []).slice(-MAX)
    state.value = "ready"
    if (follow.value) {
      await nextTick()
      if (box.value) box.value.scrollTop = box.value.scrollHeight
    }
  } catch (e) {
    if (mine !== seq) return
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

type LevelKey = "debug" | "info" | "warn" | "error"

// Maps a Go level (INFO, WARN, ERROR, DEBUG) to one of four level keys.
function lvKey(lv: string): LevelKey {
  const k = lv.toLowerCase()
  if (k.startsWith("warn")) return "warn"
  if (k.startsWith("err")) return "error"
  if (k.startsWith("debug") || k === "dbg") return "debug"
  return "info"
}

const TONE: Record<LevelKey, string> = {
  debug: "text-muted-foreground",
  info: "text-info",
  warn: "text-warning",
  error: "text-destructive",
}

const RANK: Record<LevelKey, number> = { debug: 0, info: 1, warn: 2, error: 3 }

// Returns the rank of a level: debug 0, info 1, warn 2, error 3.
function rank(lv: string): number {
  return RANK[lvKey(lv)]
}

// Applies the level floor and the text filter to a followed line, like the server does.
function passes(l: LogLineView): boolean {
  if (level.value !== "" && rank(l.level) < rank(level.value)) return false
  const t = q.value.trim().toLowerCase()
  if (t === "") return true
  return l.msg.toLowerCase().includes(t) || l.attrs.toLowerCase().includes(t)
}

// Adds the lines of a log event, and keeps the view at the end when it was at the end.
async function append(data: unknown): Promise<void> {
  const add = ((data as LogEvent)?.lines ?? []).filter(passes)
  if (add.length === 0) return
  const el = box.value
  const atBottom = !el || el.scrollHeight - el.scrollTop - el.clientHeight < 8
  lines.value = [...lines.value, ...add].slice(-MAX)
  await nextTick()
  if (atBottom && box.value) box.value.scrollTop = box.value.scrollHeight
}

// Starts or stops the follow of the log events.
function setFollow(on: boolean): void {
  stopLog?.()
  stopLog = null
  if (on) {
    setFollowLogs(true)
    stopLog = onEvent("log", (d) => void append(d))
  } else {
    setFollowLogs(false)
  }
}

function pick(v: string): void {
  level.value = v
  void load()
}

// Reka sends an empty value when the owner taps the pressed item. Keep the level then.
function onLevel(v: unknown): void {
  if (v === undefined || v === null || v === "") return
  pick(v === "all" ? "" : String(v))
}

const followId = useId()
const wrapId = useId()

watch(follow, setFollow)
watch(q, () => {
  if (filterTimer !== null) clearTimeout(filterTimer)
  filterTimer = setTimeout(() => {
    filterTimer = null
    void load()
  }, 250)
})

onMounted(load)
onBeforeUnmount(() => {
  if (follow.value) setFollow(false)
  if (filterTimer !== null) clearTimeout(filterTimer)
})
</script>

<template>
  <div class="flex flex-col gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <ToggleGroup
        type="single"
        variant="outline"
        size="sm"
        aria-label="Level"
        :model-value="level || 'all'"
        @update:model-value="onLevel"
      >
        <ToggleGroupItem v-for="l in levels" :key="l.value" :value="l.value || 'all'">
          {{ l.label }}
        </ToggleGroupItem>
      </ToggleGroup>
      <Input
        v-model="q"
        type="search"
        placeholder="Filter"
        aria-label="Filter"
        class="w-auto min-w-40 flex-1"
      />
      <div class="flex items-center gap-2">
        <Checkbox :id="followId" v-model="follow" />
        <Label :for="followId">Follow</Label>
      </div>
      <div class="flex items-center gap-2">
        <Checkbox :id="wrapId" v-model="wrap" />
        <Label :for="wrapId">Wrap lines</Label>
      </div>
    </div>
    <p class="text-xs text-muted-foreground">The log shows the lines since the gateway started.</p>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="load"
    />
    <div
      v-else
      ref="box"
      role="region"
      aria-label="Log lines"
      tabindex="0"
      :data-wrap="wrap || undefined"
      :class="
        cn(
          'max-h-[60dvh] overflow-auto rounded-md border bg-muted/30 p-2 font-mono text-xs',
          wrap ? 'whitespace-pre-wrap break-all' : 'whitespace-pre',
        )
      "
    >
      <div v-if="lines.length === 0" class="text-muted-foreground">No lines match.</div>
      <div v-for="(l, i) in lines" :key="i" data-line>
        <span class="text-muted-foreground">{{ when(l.time) }}</span>
        <span :data-level="lvKey(l.level)" :class="TONE[lvKey(l.level)]">
          {{ l.level.toUpperCase() }}</span
        >
        {{ l.msg }}
        <span class="text-muted-foreground">{{ l.attrs }}</span>
      </div>
    </div>
  </div>
</template>
