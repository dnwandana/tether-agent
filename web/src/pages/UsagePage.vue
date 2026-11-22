<script setup lang="ts">
import { computed, onMounted, ref } from "vue"
import { api } from "../api/client"
import type { CronUsageView, DailyView, UsageModelRow, UsageView } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import StateBlock from "../components/StateBlock.vue"

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

function usd(n: number, digits = 4): string {
  return "$" + n.toFixed(digits)
}

async function load(r: Range): Promise<void> {
  range.value = r
  state.value = "loading"
  try {
    const [u, d, c] = await Promise.all([
      api<UsageView>("GET", `/api/usage?range=${r}`),
      api<DailyView>("GET", `/api/usage/daily?range=${r}`),
      api<CronUsageView>("GET", `/api/usage/cron?range=${r}`),
    ])
    // A newer tap has won: drop the late answer.
    if (range.value !== r) return
    usage.value = u
    daily.value = d
    cron.value = c
    state.value = "ready"
  } catch (e) {
    if (range.value !== r) return
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
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
const cronRows = computed(() =>
  cron.value.jobs.map((j) => {
    const n = cron.value.flagged.filter((f) => f.job_id === j.job_id).length
    return {
      job_id: j.job_id,
      name: j.job_name || j.job_id,
      cost: usd(j.usd),
      flagged: n > 0 ? `💸 ${n}` : "—",
    }
  }),
)

// Groups the daily rows by day, oldest first, and gives each model its color class.
const bars = computed(() => {
  const totals = new Map<string, number>()
  for (const d of daily.value.days) totals.set(d.model, (totals.get(d.model) ?? 0) + d.usd)
  const order = [...totals.entries()].sort((a, b) => b[1] - a[1]).map(([m]) => m)
  const cls = (m: string) => `s${(order.indexOf(m) % 4) + 1}`
  const byDay = new Map<
    string,
    { total: number; unpriced: boolean; parts: { model: string; cls: string; usd: number }[] }
  >()
  for (const d of daily.value.days) {
    const g = byDay.get(d.day) ?? { total: 0, unpriced: false, parts: [] }
    g.total += d.usd
    g.unpriced = g.unpriced || d.unpriced
    g.parts.push({ model: d.model, cls: cls(d.model), usd: d.usd })
    byDay.set(d.day, g)
  }
  const max = Math.max(0, ...[...byDay.values()].map((g) => g.total))
  return [...byDay.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([day, g]) => ({
      day,
      total: g.total,
      unpriced: g.unpriced,
      parts: g.parts.map((p) => ({
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
  return [...seen.entries()].map(([model, cls]) => ({ model, cls }))
})
const maxDay = computed(() => Math.max(0, ...bars.value.map((b) => b.total)))

onMounted(() => load(range.value))
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Usage</h1>
      <div class="seg">
        <button
          v-for="r in ranges"
          :key="r.id"
          type="button"
          :class="{ 'is-on': range === r.id }"
          @click="load(r.id)"
        >
          {{ r.label }}
        </button>
      </div>
    </div>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      @retry="load(range)"
    />
    <StateBlock v-else-if="rows.length === 0" state="empty" text="No model calls in this range." />
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

      <div class="card">
        <div class="card-h">
          <h3>Spend per day</h3>
          <span v-for="l in legend" :key="l.model" class="lg"
            ><i :class="l.cls" />{{ l.model }}</span
          >
        </div>
        <div class="card-b">
          <div class="sbars">
            <div class="axis-y">
              <span>{{ usd(maxDay, 2) }}</span
              ><span>{{ usd(maxDay / 2, 2) }}</span
              ><span>$0</span>
            </div>
            <div
              v-for="b in bars"
              :key="b.day"
              class="sbar"
              :title="`${b.day}: ${usd(b.total)}${b.unpriced ? ' (+ unpriced calls)' : ''}`"
            >
              <i
                v-for="p in b.parts"
                :key="p.model"
                :class="p.cls"
                :style="{ height: `${p.pct}%` }"
              />
            </div>
          </div>
        </div>
      </div>

      <DataTable :columns="columns" :rows="tableRows" row-key="key">
        <template #cell-model="{ value }"
          ><span class="mono">{{ value }}</span></template
        >
      </DataTable>

      <div class="section-h"><h2>Cron cost by job</h2></div>
      <DataTable :columns="cronColumns" :rows="cronRows" row-key="job_id">
        <template #cell-name="{ row }">
          <router-link :to="{ name: 'job', params: { id: String(row.job_id) } }">{{
            row.name
          }}</router-link>
        </template>
      </DataTable>
    </template>
  </div>
</template>
