<script setup lang="ts">
import { onMounted, reactive, ref } from "vue"
import { api, ApiError } from "../api/client"
import type { AccessView, PairingRow } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import StateBlock from "../components/StateBlock.vue"
import { confirm, toast } from "../stores/ui"

const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const pending = ref<PairingRow[]>([])
const approved = ref<PairingRow[]>([])
const triesLeft = ref<number | null>(null)
const codes = reactive<Record<string, string>>({})

const pendingColumns = [
  { key: "name", label: "Person" },
  { key: "since", label: "Asked" },
  { key: "expires", label: "Expires" },
  { key: "code", label: "Code" },
]
const approvedColumns = [
  { key: "name", label: "Person" },
  { key: "role", label: "Role" },
  { key: "since", label: "Since" },
  { key: "actions", label: "" },
]

const rowKey = (r: PairingRow): string => `${r.platform}:${r.user_id}`
const asRow = (r: unknown): PairingRow => r as PairingRow
const asRows = (r: PairingRow[]): Record<string, unknown>[] =>
  r.map((x) => ({ ...x, key: rowKey(x) }))

// Returns a short local time, or a dash for a zero or missing time.
function when(s: string): string {
  const d = new Date(s)
  if (!s || Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "—"
  return d.toLocaleString()
}

function inMinutes(s: string): string {
  const t = new Date(s).getTime()
  if (Number.isNaN(t)) return "—"
  return `in ${Math.max(Math.round((t - Date.now()) / 60000), 0)} min`
}

async function load(): Promise<void> {
  try {
    const v = await api<AccessView>("GET", "/api/pairing")
    pending.value = v.pending ?? []
    approved.value = v.approved ?? []
    triesLeft.value = v.tries_left
    state.value = "ready"
  } catch (e) {
    errorText.value = e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

async function approve(row: PairingRow): Promise<void> {
  const code = (codes[rowKey(row)] ?? "").trim()
  if (code.length !== 8) {
    toast("The code has 8 characters.", "error")
    return
  }
  const ok = await confirm({
    title: `Approve ${row.name}?`,
    body: "This person becomes an owner. They can chat, approve commands, and change settings.",
    confirmLabel: "Approve",
  })
  if (!ok) return
  try {
    await api("POST", "/api/pairing/approve", { platform: row.platform, code: code.toUpperCase() })
    toast(`Approved ${row.name}.`)
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
  await load()
}

async function revoke(row: PairingRow): Promise<void> {
  const ok = await confirm({
    title: `Revoke ${row.name}?`,
    body: "They lose access at once. Their sessions stay.",
    confirmLabel: "Revoke",
    danger: true,
  })
  if (!ok) return
  try {
    await api("POST", "/api/pairing/revoke", { platform: row.platform, user_id: row.user_id })
  } catch (e) {
    toast(e instanceof ApiError ? e.message : String(e), "error")
  }
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Access</h1>
      <router-link class="link" :to="{ name: 'start' }">Getting started</router-link>
    </div>
    <p class="c-2">
      Telegram pairing. An approved person is an owner: they can chat, approve commands, and change
      settings.
    </p>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @retry="load"
    />
    <template v-else>
      <div class="section-h">
        <h2>Pending</h2>
        <span class="badge">{{ pending.length }}</span>
      </div>
      <StateBlock
        v-if="pending.length === 0"
        state="empty"
        text="No pairing requests. A person who sends /start to the bot gets a code. They tell you the code, and you approve it here."
      />
      <DataTable v-else :columns="pendingColumns" :rows="asRows(pending)" row-key="key">
        <template #cell-name="{ row }">
          <b>{{ asRow(row).name }}</b>
          <div class="mono c-3 t-12">{{ asRow(row).platform }} · {{ asRow(row).user_id }}</div>
        </template>
        <template #cell-since="{ row }">{{ when(asRow(row).since) }}</template>
        <template #cell-expires="{ row }">{{ inMinutes(asRow(row).expires) }}</template>
        <template #cell-code="{ row }">
          <input
            v-model="codes[rowKey(asRow(row))]"
            class="input mono"
            maxlength="8"
            placeholder="8-char code"
          />
          <button class="btn btn-primary" type="button" @click="approve(asRow(row))">
            Approve
          </button>
        </template>
      </DataTable>
      <p class="pill-note">
        The person reads the code from the bot. 5 wrong codes lock pairing for 1 hour<template
          v-if="triesLeft !== null"
        >
          ({{ triesLeft }} {{ triesLeft === 1 ? "try" : "tries" }} left)</template
        >.
      </p>

      <div class="section-h"><h2>Approved</h2></div>
      <DataTable :columns="approvedColumns" :rows="asRows(approved)" row-key="key">
        <template #cell-name="{ row }">
          <b>{{ asRow(row).name }}</b>
          <div class="mono c-3 t-12">{{ asRow(row).platform }} · {{ asRow(row).user_id }}</div>
        </template>
        <template #cell-role><span class="chip">owner</span></template>
        <template #cell-since="{ row }">{{ when(asRow(row).since) }}</template>
        <template #cell-actions="{ row }">
          <button class="btn btn-danger-soft" type="button" @click="revoke(asRow(row))">
            Revoke
          </button>
        </template>
      </DataTable>
    </template>
  </div>
</template>
