<script setup lang="ts">
import { computed, ref } from "vue"
import { api } from "../api/client"
import type { CronUsageView, DailyView, UsageModelRow, UsageView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
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
const daily = ref<DailyView>({ range: "", days: [] })
const cron = ref<CronUsageView>({ range: "", jobs: [], flagged: [] })

// 31200000 → "31.2M", 1100000 → "1.10M".
function millions(n: number): string {
  const m = n / 1e6
  return (m >= 10 ? m.toFixed(1) : m.toFixed(2)) + "M"
}

function pad2(n: number): string {
  return String(n).padStart(2, "0")
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
      cost: usd(j.usd),
      flagged: n > 0 ? `💸 ${n}` : "—",
    }
  })
  const m = cron.value.flagged.length
  out.push({
    job_id: "total",
    name: `Total · ${jobs.length} ${jobs.length === 1 ? "job" : "jobs"}`,
    cost: usd(jobs.reduce((a, j) => a + j.usd, 0)),
    flagged: m > 0 ? `💸 ${m}` : "—",
    total: true,
  })
  return out
})

const table = ref(false)

// The number of days from the first to the last day of the data. It is computed once.
const daySpan = computed(() => {
  const days = daily.value.days
  if (days.length === 0) return 0
  let lo = days[0].day
  let hi = lo
  for (const d of days) {
    if (d.day < lo) lo = d.day
    if (d.day > hi) hi = d.day
  }
  return (Date.parse(hi + "T00:00:00Z") - Date.parse(lo + "T00:00:00Z")) / 86400000
})

// Returns the bar key of a day. Only the range All groups by week or by month.
function bucket(day: string): string {
  if (range.value !== "all") return day
  const span = daySpan.value
  if (span > 365) return day.slice(0, 7)
  if (span > 60) {
    const d = new Date(day + "T00:00:00")
    d.setDate(d.getDate() - ((d.getDay() + 6) % 7))
    return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
  }
  return day
}

// Returns "Nov 2026" for a month key and "Nov 19" for a day key.
function label(key: string): string {
  if (key.length === 7)
    return new Date(key + "-01T00:00:00").toLocaleDateString("en-US", {
      month: "short",
      year: "numeric",
    })
  return new Date(key + "T00:00:00").toLocaleDateString("en-US", { month: "short", day: "numeric" })
}

// Groups the daily rows by bar key, oldest first. The 5 largest models get s1 to s5. Each other
// model joins the series Other (s6).
const bars = computed(() => {
  const totals = new Map<string, number>()
  for (const d of daily.value.days) totals.set(d.model, (totals.get(d.model) ?? 0) + d.usd)
  const order = [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([m]) => m)
  const series = (m: string) => {
    const i = order.indexOf(m)
    return i < 5 ? { name: m, cls: `s${i + 1}` } : { name: "Other", cls: "s6" }
  }
  const flaggedKeys = new Set(cron.value.flagged.map((f) => bucket(f.at.slice(0, 10))))
  type Group = {
    total: number
    unpriced: boolean
    inTok: number
    outTok: number
    top: string
    topUsd: number
    parts: Map<string, { model: string; cls: string; usd: number }>
  }
  const groups = new Map<string, Group>()
  for (const d of daily.value.days) {
    const key = bucket(d.day)
    const g = groups.get(key) ?? {
      total: 0,
      unpriced: false,
      inTok: 0,
      outTok: 0,
      top: "",
      topUsd: -1,
      parts: new Map(),
    }
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
  const max = Math.max(0, ...[...groups.values()].map((g) => g.total))
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
        pct: max > 0 ? (p.usd / max) * 100 : 0,
      })),
    }))
})
const legend = computed(() => {
  const seen = new Map<string, string>()
  for (const b of bars.value)
    for (const p of b.parts) if (!seen.has(p.model)) seen.set(p.model, p.cls)
  return [...seen.entries()]
    .map(([model, cls]) => ({ model, cls }))
    .sort((a, b) => a.cls.localeCompare(b.cls))
})
const maxDay = computed(() => Math.max(0, ...bars.value.map((b) => b.total)))
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

      <div v-else class="card">
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
            <div class="sbars">
              <div class="axis-y">
                <span>{{ usd(maxDay, 2) }}</span
                ><span>{{ usd(maxDay / 2, 2) }}</span
                ><span>$0</span>
              </div>
              <div
                v-for="b in bars"
                :key="b.key"
                class="sbar"
                tabindex="0"
                role="img"
                :aria-label="`${b.label}: ${usd(b.total, 2)}`"
                :title="`${b.key}: ${usd(b.total)}${b.unpriced ? ' (+ unpriced calls)' : ''}`"
              >
                <span v-if="b.spike" class="spike" aria-hidden="true">💸</span>
                <i
                  v-for="p in b.parts"
                  :key="p.model"
                  :class="p.cls"
                  :style="{ height: `${p.pct}%` }"
                />
              </div>
            </div>
            <div class="axis-x">
              <span v-for="b in bars" :key="b.key">{{ b.label }}</span>
            </div>
          </template>
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
