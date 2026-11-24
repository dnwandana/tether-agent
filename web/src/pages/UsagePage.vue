<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import { api } from "../api/client"
import type { CronUsageView, DailyView, UsageModelRow, UsageView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { dayLabel } from "../lib/time"
import { bucketKeys, bucketOf, formatTick, grainFor, labelStep, niceMax } from "../lib/usagechart"
import { session } from "../stores/session"

type Range = "today" | "7d" | "30d" | "all" | "session"

const ranges: { id: Range; label: string }[] = [
  { id: "today", label: "Today" },
  { id: "7d", label: "7 days" },
  { id: "30d", label: "30 days" },
  { id: "all", label: "All" },
  { id: "session", label: "This session" },
]

const range = ref<Range>("30d")
const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const usage = ref<UsageView | null>(null)
const daily = ref<DailyView>({ range: "", from: "", to: "", days: [] })
const cron = ref<CronUsageView>({ range: "", jobs: [], flagged: [] })

// 31200000 → "31.2M", 1100000 → "1.10M".
function millions(n: number): string {
  const m = n / 1e6
  return (m >= 10 ? m.toFixed(1) : m.toFixed(2)) + "M"
}

function usd(n: number, digits = 4): string {
  return "$" + n.toFixed(digits)
}

let seq = 0

// Loads the three routes for the range that is on now.
async function load(): Promise<void> {
  const mine = ++seq
  try {
    const r = range.value
    const [u, d, c] = await Promise.all([
      api<UsageView>("GET", `/api/usage?range=${r}`),
      api<DailyView>("GET", `/api/usage/daily?range=${r}`),
      api<CronUsageView>("GET", `/api/usage/cron?range=${r}`),
    ])
    // A newer request has won: drop the late answer.
    if (mine !== seq) return
    usage.value = u
    daily.value = d
    cron.value = c
    state.value = "ready"
  } catch (e) {
    if (mine !== seq) return
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

const { stale, updatedAt, refresh } = useLiveData(load)

function pick(r: Range): void {
  range.value = r
  state.value = "loading"
  void refresh()
}

const rows = computed(() => usage.value?.rows ?? [])
const sum = (f: (r: UsageModelRow) => number) => rows.value.reduce((a, r) => a + f(r), 0)
const totalCalls = computed(() => sum((r) => r.calls))
const totalIn = computed(() => sum((r) => r.in))
const totalOut = computed(() => sum((r) => r.out))
const totalCached = computed(() => sum((r) => r.cached))
const unpricedCalls = computed(() =>
  rows.value.filter((r) => !r.priced).reduce((a, r) => a + r.calls, 0),
)
const hit = computed(() =>
  totalIn.value > 0 ? `${Math.round((totalCached.value / totalIn.value) * 100)}%` : "—",
)

const columns = [
  { key: "model", label: "Model" },
  { key: "provider", label: "Provider" },
  { key: "calls", label: "Calls", align: "right" as const },
  { key: "in", label: "In", align: "right" as const },
  { key: "cached", label: "Cached", align: "right" as const },
  { key: "hit", label: "Hit", align: "right" as const },
  { key: "out", label: "Out", align: "right" as const },
  { key: "tok", label: "Tok/s", align: "right" as const },
  { key: "cost", label: "Cost", align: "right" as const },
]
const tableRows = computed(() =>
  rows.value.map((r) => ({
    key: r.model + "\u0000" + r.provider,
    model: r.model,
    provider: r.provider,
    calls: r.calls,
    in: millions(r.in),
    cached: millions(r.cached),
    hit: r.hit_known ? `${r.hit_pct}%` : "—",
    out: millions(r.out),
    tok: r.tok_per_sec ? Math.round(r.tok_per_sec) : "—",
    cost: usd(r.usd) + (r.priced ? "" : "+"),
  })),
)

const cronColumns = [
  { key: "name", label: "Job" },
  { key: "cost", label: "Cost", align: "right" as const },
  { key: "flagged", label: "Flagged", align: "right" as const },
]
const cronRows = computed(() => {
  const jobs = cron.value.jobs
  const out: Record<string, unknown>[] = jobs.map((j) => {
    const n = cron.value.flagged.filter((f) => f.job_id === j.job_id).length
    return {
      job_id: j.job_id,
      name: j.job_name,
      deleted: !j.job_name,
      cost: usd(j.usd) + (j.unpriced ? "+" : ""),
      flagged: n > 0 ? `💸 ${n}` : "—",
    }
  })
  const m = cron.value.flagged.length
  out.push({
    job_id: "total",
    name: `Total · ${jobs.length} ${jobs.length === 1 ? "job" : "jobs"}`,
    cost: usd(jobs.reduce((a, j) => a + j.usd, 0)) + (jobs.some((j) => j.unpriced) ? "+" : ""),
    flagged: m > 0 ? `💸 ${m}` : "—",
    total: true,
  })
  return out
})

const table = ref(false)

// Returns "Nov 2026" for a month key and "Nov 19" for a day key.
function label(key: string): string {
  if (key.length === 7) {
    const [y, m] = key.split("-").map(Number)
    return new Date(Date.UTC(y, m - 1, 1)).toLocaleDateString("en-US", {
      month: "short",
      year: "numeric",
      timeZone: "UTC",
    })
  }
  return dayLabel(key)
}

// The first and the last day of the chart: the range of the answer, else the data.
const span = computed(() => {
  const days = daily.value.days.map((d) => d.day).sort()
  return {
    from: daily.value.from || days[0] || "",
    to: daily.value.to || days[days.length - 1] || "",
  }
})
const grain = computed(() => grainFor(range.value, span.value.from, span.value.to))

type Group = {
  total: number
  unpriced: boolean
  inTok: number
  outTok: number
  top: string
  topUsd: number
  parts: Map<string, { model: string; cls: string; usd: number }>
}

function empty(): Group {
  return {
    total: 0,
    unpriced: false,
    inTok: 0,
    outTok: 0,
    top: "",
    topUsd: -1,
    parts: new Map(),
  }
}

// The largest total of a bucket, before the bars are built.
const top = computed(() => {
  const sums = new Map<string, number>()
  for (const d of daily.value.days) {
    const k = bucketOf(d.day, grain.value)
    sums.set(k, (sums.get(k) ?? 0) + d.usd)
  }
  return niceMax(Math.max(0, ...sums.values()))
})

// Groups the daily rows by bar key, oldest first. An empty bucket stays in the list. The 5 largest models get s1 to s5. Each other
// model joins the series Other (s6).
const bars = computed(() => {
  const totals = new Map<string, number>()
  for (const d of daily.value.days) totals.set(d.model, (totals.get(d.model) ?? 0) + d.usd)
  const order = [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([m]) => m)
  const series = (m: string) => {
    const i = order.indexOf(m)
    return i < 5 ? { name: m, cls: `s${i + 1}` } : { name: "Other", cls: "s6" }
  }
  const flaggedKeys = new Set(
    cron.value.flagged.map((f) => bucketOf(f.at.slice(0, 10), grain.value)),
  )
  const groups = new Map<string, Group>()
  for (const key of bucketKeys(span.value.from, span.value.to, grain.value))
    groups.set(key, empty())
  for (const d of daily.value.days) {
    const key = bucketOf(d.day, grain.value)
    const g = groups.get(key) ?? empty()
    g.total += d.usd
    g.unpriced = g.unpriced || d.unpriced
    g.inTok += d.in
    g.outTok += d.out
    if (d.usd > g.topUsd) {
      g.top = d.model
      g.topUsd = d.usd
    }
    const sr = series(d.model)
    const part = g.parts.get(sr.name) ?? { model: sr.name, cls: sr.cls, usd: 0 }
    part.usd += d.usd
    g.parts.set(sr.name, part)
    groups.set(key, g)
  }
  return [...groups.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([key, g]) => ({
      key,
      label: label(key),
      total: g.total,
      unpriced: g.unpriced,
      inTok: g.inTok,
      outTok: g.outTok,
      top: g.top,
      spike: flaggedKeys.has(key),
      parts: [...g.parts.values()].map((p) => ({
        model: p.model,
        cls: p.cls,
        usd: p.usd,
        pct: (p.usd / top.value) * 100,
      })),
    }))
})

const chartCard = ref<HTMLElement | null>(null)
const sbarsEl = ref<HTMLElement | null>(null)
const width = ref(600)
const chartHeight = ref(170)
const ticks = computed(() => {
  const t = top.value
  return (chartHeight.value < 160 ? [t, 0] : [t, t / 2, 0]).map(formatTick)
})
const step = computed(() => labelStep(bars.value.length, width.value))

// The label width in px that labelStep reserves.
const LABEL_PX = 56
// The first and the last label grow inward by their full width, so a middle label keeps room
// for one and a half labels from each end. The count is in bars.
const endGap = computed(() => {
  const pitch = width.value / Math.max(1, bars.value.length)
  return Math.max(step.value, Math.ceil((1.5 * LABEL_PX) / pitch - 0.5))
})

// Returns true when the bar at index i gets a date label.
function showLabel(i: number): boolean {
  const n = bars.value.length
  if (i === 0 || i === n - 1) return true
  return i % step.value === 0 && i >= endGap.value && n - 1 - i >= endGap.value
}

let observer: ResizeObserver | null = null
function stopObserver(): void {
  observer?.disconnect()
  observer = null
}
watch(chartCard, (el) => {
  stopObserver()
  if (!el || typeof ResizeObserver === "undefined") return
  observer = new ResizeObserver((entries) => {
    // The labels share the bar area, which is narrower than the card by the padding and
    // the tick column. Use the card width only when the bar area has no width.
    const box = sbarsEl.value
    const pad = box ? parseFloat(getComputedStyle(box).paddingLeft) || 0 : 0
    const area = box ? box.clientWidth - pad : 0
    const w = area > 0 ? area : (entries[entries.length - 1]?.contentRect.width ?? 0)
    if (w > 0) width.value = w
    const h = sbarsEl.value?.clientHeight ?? 0
    if (h > 0) chartHeight.value = h
  })
  observer.observe(el)
})

const tipIndex = ref(-1)
const tipLeft = ref(8)
const tipEl = ref<HTMLElement | null>(null)

// Opens the tooltip of bar i. It never closes the tooltip.
function openTip(i: number, bar: HTMLElement): void {
  tipIndex.value = i
  const card = chartCard.value
  if (!card) return
  const tipW = tipEl.value?.offsetWidth || 200
  const c = card.getBoundingClientRect()
  const r = bar.getBoundingClientRect()
  const left = r.left - c.left + r.width / 2 - tipW / 2
  tipLeft.value = Math.max(8, Math.min(left, c.width - tipW - 8))
}

// Keeps the tooltip on the bar under a finger that moves over the bars.
function follow(e: PointerEvent): void {
  if (tipIndex.value < 0) return
  const bar = document.elementFromPoint?.(e.clientX, e.clientY)?.closest<HTMLElement>(".sbar")
  if (bar?.dataset.i !== undefined) openTip(Number(bar.dataset.i), bar)
}

function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape") tipIndex.value = -1
}
function onDown(e: Event): void {
  if (!chartCard.value?.contains(e.target as Node)) tipIndex.value = -1
}
onMounted(() => {
  document.addEventListener("keydown", onKey)
  document.addEventListener("pointerdown", onDown)
})
onBeforeUnmount(() => {
  document.removeEventListener("keydown", onKey)
  document.removeEventListener("pointerdown", onDown)
  stopObserver()
})

const legend = computed(() => {
  const seen = new Map<string, string>()
  for (const b of bars.value)
    for (const p of b.parts) if (!seen.has(p.model)) seen.set(p.model, p.cls)
  return [...seen.entries()]
    .map(([model, cls]) => ({ model, cls }))
    .sort((a, b) => a.cls.localeCompare(b.cls))
})
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Usage</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <div class="seg">
        <button
          v-for="r in ranges"
          :key="r.id"
          type="button"
          :class="{ 'is-on': range === r.id }"
          @click="pick(r.id)"
        >
          {{ r.label }}
        </button>
      </div>
    </div>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="errorText" @action="refresh" />
    <StateBlock
      v-else-if="rows.length === 0"
      state="empty"
      text="No usage in this range."
      action="Show 30 days"
      @action="pick('30d')"
    />
    <template v-else>
      <div class="grid-4">
        <div class="tile">
          <span class="eyebrow">Spend</span>
          <span class="v mono">{{ usd(usage!.total_usd, 2) }}{{ usage!.unpriced ? "+" : "" }}</span>
          <span v-if="unpricedCalls > 0" class="t-12 c-3"
            >{{ unpricedCalls }} calls have no price</span
          >
        </div>
        <div class="tile">
          <span class="eyebrow">Calls</span>
          <span class="v">{{ totalCalls }}</span>
          <span class="t-12 c-3">across {{ rows.length }} models</span>
        </div>
        <div class="tile">
          <span class="eyebrow">Tokens in · out</span>
          <span class="v mono">{{ millions(totalIn) }} · {{ millions(totalOut) }}</span>
        </div>
        <div class="tile">
          <span class="eyebrow">Cache hit</span>
          <span class="v">{{ hit }}</span>
          <span class="t-12 c-3">{{ usage!.compactions }} compactions</span>
        </div>
      </div>

      <p v-if="range === 'session'" class="c-2">
        This session: {{ session.title || "New session" }}
      </p>

      <div v-else ref="chartCard" class="card" style="position: relative">
        <div class="card-h">
          <h3>Spend per day</h3>
          <span v-for="l in legend" :key="l.model" class="lg"
            ><i :class="l.cls" />{{ l.model }}</span
          >
          <button type="button" class="btn btn-ghost btn-sm" @click="table = !table">
            {{ table ? "View as chart" : "View as table" }}
          </button>
        </div>
        <div class="card-b">
          <div v-if="table" class="table-wrap">
            <table class="days">
              <thead>
                <tr>
                  <th>Day</th>
                  <th>Total</th>
                  <th>Tokens in · out</th>
                  <th>Top model</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="b in bars" :key="b.key">
                  <td>{{ b.label }}</td>
                  <td>{{ usd(b.total, 2) }}</td>
                  <td>{{ b.inTok }} · {{ b.outTok }}</td>
                  <td>{{ b.top }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <template v-else>
            <div ref="sbarsEl" class="sbars" @pointermove="follow">
              <div class="axis-y">
                <span v-for="t in ticks" :key="t">{{ t }}</span>
              </div>
              <button
                v-for="(b, i) in bars"
                :key="b.key"
                type="button"
                class="sbar"
                :data-i="i"
                :aria-label="`${b.label}: ${usd(b.total, 2)}`"
                @pointerenter="openTip(i, $event.currentTarget as HTMLElement)"
                @click="openTip(i, $event.currentTarget as HTMLElement)"
              >
                <span v-if="b.spike" class="spike" aria-hidden="true">💸</span>
                <i
                  v-for="p in b.parts"
                  :key="p.model"
                  :class="p.cls"
                  :style="{ height: `${p.pct}%` }"
                />
              </button>
            </div>
            <div class="axis-x">
              <span v-for="(b, i) in bars" :key="b.key">{{ showLabel(i) ? b.label : "" }}</span>
            </div>
          </template>
        </div>
        <div
          v-if="tipIndex >= 0 && bars[tipIndex]"
          ref="tipEl"
          class="popover usage-tip"
          role="tooltip"
          :style="{
            position: 'absolute',
            left: tipLeft + 'px',
            top: '48px',
            zIndex: 5,
            padding: '8px 10px',
            minWidth: '160px',
          }"
        >
          <b>{{ bars[tipIndex].label }}</b>
          <div v-for="p in bars[tipIndex].parts" :key="p.model" class="t-12">
            {{ p.model }} {{ usd(p.usd) }}
          </div>
          <div class="t-12">
            <b>Total {{ usd(bars[tipIndex].total) }}{{ bars[tipIndex].unpriced ? "+" : "" }}</b>
          </div>
        </div>
      </div>

      <DataTable :columns="columns" :rows="tableRows" row-key="key">
        <template #cell-model="{ value }"
          ><span class="mono">{{ value }}</span></template
        >
      </DataTable>

      <template v-if="range !== 'session'">
        <div class="section-h"><h2>Cron cost by job</h2></div>
        <DataTable class="cron-cost" :columns="cronColumns" :rows="cronRows" row-key="job_id">
          <template #cell-name="{ row }">
            <b v-if="row.total">{{ row.name }}</b>
            <span v-else-if="row.deleted" class="c-3">Deleted job</span>
            <router-link v-else :to="{ name: 'job', params: { id: String(row.job_id) } }">{{
              row.name
            }}</router-link>
          </template>
        </DataTable>

        <div v-if="cron.flagged.length > 0" class="card flagged">
          <div class="card-h"><h3>Flagged runs</h3></div>
          <div class="li" v-for="(f, i) in cron.flagged" :key="i">
            <router-link v-if="f.job_name" :to="{ name: 'job', params: { id: f.job_id } }">{{
              f.job_name
            }}</router-link>
            <span v-else class="c-3">Deleted job</span>
            <span class="grow c-2"
              >{{ f.at.slice(0, 10) }} · {{ usd(f.usd) }} · median {{ usd(f.median) }}</span
            >
          </div>
        </div>
      </template>
    </template>
  </div>
</template>
