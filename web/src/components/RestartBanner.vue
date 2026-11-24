<template>
  <div v-if="on" class="restart" role="status" aria-live="polite">
    <b>♻️ The gateway restarts.</b>
    <span class="c-2">{{ progress }}</span>
    <template v-if="late">
      <span>
        The gateway did not come back. Reload the page, or check
        <code>systemctl --user status tether</code>.
      </span>
      <button class="btn btn-secondary btn-sm" type="button" @click="reload">Reload</button>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { api } from "../api/client"
import type { BootstrapView } from "../api/types.gen"
import { onEvent } from "../lib/bus"
import { status } from "../stores/status"

// The drain time that applies when neither the event nor the bootstrap gives one.
const DEFAULT_DRAIN_S = 180
// The extra time that the gateway has to start again after the drain.
const GRACE_S = 60

const on = ref(false)
const late = ref(false)
const p = ref({ elapsed_s: 0, drain_s: 0, turns: 0, goals: 0 })
const drain = ref(0)
let broken = false
let startedAt = 0
let timer: ReturnType<typeof setTimeout> | undefined

const num = (v: unknown): number => (typeof v === "number" && v > 0 ? v : 0)

function arm(): void {
  clearTimeout(timer)
  const left = startedAt + (drain.value + GRACE_S) * 1000 - Date.now()
  timer = setTimeout(
    () => {
      late.value = true
    },
    Math.max(0, left),
  )
}

async function onRestart(data: unknown): Promise<void> {
  const d = (data ?? {}) as Record<string, unknown>
  p.value = {
    elapsed_s: num(d.elapsed_s),
    drain_s: num(d.drain_s),
    turns: num(d.turns),
    goals: num(d.goals),
  }
  if (on.value) {
    if (p.value.drain_s > 0 && p.value.drain_s !== drain.value) {
      drain.value = p.value.drain_s
      arm()
    }
    return
  }
  on.value = true
  late.value = false
  broken = status.stream !== "open"
  startedAt = Date.now()
  if (p.value.drain_s > 0) {
    drain.value = p.value.drain_s
  } else {
    drain.value = DEFAULT_DRAIN_S
    arm()
    try {
      const b = await api<BootstrapView>("GET", "/api/bootstrap")
      if (on.value && b.drain_s > 0) drain.value = b.drain_s
    } catch {
      // Keep the default drain time.
    }
  }
  if (on.value) arm()
}

watch(
  () => status.stream,
  (s) => {
    if (!on.value) return
    if (s !== "open") {
      broken = true
      return
    }
    if (broken) {
      on.value = false
      late.value = false
      broken = false
      clearTimeout(timer)
    }
  },
)

const progress = computed(() => {
  const parts = [`${p.value.elapsed_s} s of ${p.value.drain_s || drain.value} s`]
  const t = p.value.turns
  if (t > 0) parts.push(t === 1 ? "1 turn runs" : `${t} turns run`)
  const g = p.value.goals
  if (g > 0)
    parts.push(g === 1 ? "1 goal resumes after the start" : `${g} goals resume after the start`)
  return parts.join(" · ")
})

function reload(): void {
  location.reload()
}

let stop = () => {}
onMounted(() => {
  stop = onEvent("restart", (d) => void onRestart(d))
})
onBeforeUnmount(() => {
  stop()
  clearTimeout(timer)
})
</script>
