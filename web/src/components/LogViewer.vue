<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { api } from "../api/client"
import { setFollowLogs } from "../api/stream"
import type { LogEvent, LogLineView, LogView } from "../api/types.gen"
import { onEvent } from "../lib/bus"
import { friendlyError } from "../lib/errors"
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

// Maps a Go level (INFO, WARN, ERROR, DEBUG) to the CSS class of the log.
function lvClass(lv: string): string {
  const k = lv.toLowerCase()
  if (k.startsWith("warn")) return "lv-warn"
  if (k.startsWith("err")) return "lv-err"
  if (k.startsWith("debug") || k === "dbg") return "lv-dbg"
  return "lv-info"
}

// Returns the rank of a level: debug 0, info 1, warn 2, error 3.
function rank(lv: string): number {
  const c = lvClass(lv)
  if (c === "lv-dbg") return 0
  if (c === "lv-warn") return 2
  if (c === "lv-err") return 3
  return 1
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
  <div>
    <div class="toolbar">
      <div class="seg">
        <button
          v-for="l in levels"
          :key="l.value"
          type="button"
          :class="{ 'is-on': level === l.value }"
          :aria-pressed="level === l.value"
          @click="pick(l.value)"
        >
          {{ l.label }}
        </button>
      </div>
      <div class="search">
        <input v-model="q" class="input" type="search" placeholder="Filter" aria-label="Filter" />
      </div>
      <label class="check"><input v-model="follow" type="checkbox" /> Follow</label>
      <label class="check"><input v-model="wrap" type="checkbox" /> Wrap lines</label>
    </div>
    <p class="t-12 c-3">The log shows the lines since the gateway started.</p>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="load"
    />
    <div v-else ref="box" class="log log-view" :class="{ 'is-wrap': wrap }">
      <div v-if="lines.length === 0" class="c-3">No lines match.</div>
      <div v-for="(l, i) in lines" :key="i">
        <span class="c-3">{{ when(l.time) }}</span>
        <span :class="lvClass(l.level)"> {{ l.level.toUpperCase() }}</span>
        {{ l.msg }}
        <span class="c-3">{{ l.attrs }}</span>
      </div>
    </div>
  </div>
</template>
