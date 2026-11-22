<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { api } from "../api/client"
import type { LogLineView, LogView } from "../api/types.gen"
import StateBlock from "./StateBlock.vue"

const levels = [
  { label: "All", value: "" },
  { label: "Debug", value: "debug" },
  { label: "Info", value: "info" },
  { label: "Warn", value: "warn" },
  { label: "Error", value: "error" },
]

const level = ref("")
const q = ref("")
const follow = ref(false)
const lines = ref<LogLineView[]>([])
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const box = ref<HTMLElement | null>(null)

let followTimer: ReturnType<typeof setInterval> | null = null
let filterTimer: ReturnType<typeof setTimeout> | null = null

const pad = (n: number): string => String(n).padStart(2, "0")

// Formats the time of a line as local HH:MM:SS.
function hms(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ""
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

async function load(): Promise<void> {
  try {
    const qs = new URLSearchParams({ level: level.value, q: q.value, limit: "500" })
    const v = await api<LogView>("GET", "/api/logs?" + qs.toString())
    lines.value = v.lines ?? []
    state.value = "ready"
    if (follow.value) {
      await nextTick()
      if (box.value) box.value.scrollTop = box.value.scrollHeight
    }
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

// Starts or stops the 2 s reload of Follow.
function setFollow(on: boolean): void {
  if (followTimer !== null) {
    clearInterval(followTimer)
    followTimer = null
  }
  if (on) followTimer = setInterval(() => void load(), 2000)
}

// Maps a Go level (INFO, WARN, ERROR, DEBUG) to the CSS class of the log.
function lvClass(lv: string): string {
  const k = lv.toLowerCase()
  if (k.startsWith("warn")) return "lv-warn"
  if (k.startsWith("err")) return "lv-err"
  if (k.startsWith("debug") || k === "dbg") return "lv-dbg"
  return "lv-info"
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
  setFollow(false)
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
        <input v-model="q" type="search" placeholder="Filter" aria-label="Filter" />
      </div>
      <label class="check"><input v-model="follow" type="checkbox" /> Follow</label>
    </div>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @retry="load"
    />
    <div v-else ref="box" class="log">
      <div v-if="lines.length === 0" class="c-3">No lines match.</div>
      <div v-for="(l, i) in lines" :key="i">
        <span class="c-3">{{ hms(l.time) }}</span>
        <span :class="lvClass(l.level)"> {{ l.level.toUpperCase() }}</span>
        {{ l.msg }}
        <span class="c-3">{{ l.attrs }}</span>
      </div>
    </div>
  </div>
</template>
