<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue"
import { api } from "../api/client"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import type { AccessView, PairingRow } from "../api/types.gen"
import DataTable from "../components/DataTable.vue"
import LiveHeader from "../components/LiveHeader.vue"
import StateBlock from "../components/StateBlock.vue"
import { friendlyError } from "../lib/errors"
import { useLiveData } from "../lib/live"
import { fullTime, shortTime } from "../lib/time"
import { confirm, toast } from "../stores/ui"

const state = ref<"loading" | "ready" | "error">("loading")
const errorText = ref("")
const pending = ref<PairingRow[]>([])
const approved = ref<PairingRow[]>([])
const revoked = ref<PairingRow[]>([])
const pairingOn = ref(true)
const lockedUntil = ref<string | null>(null)
const triesLeft = ref<number | null>(null)
const now = ref(Date.now())
const clock = setInterval(() => {
  now.value = Date.now()
}, 60_000)
// Updates now at the end of a lockout, so Approve is not disabled for up to 60 s too long.
let unlock: ReturnType<typeof setTimeout> | null = null
onBeforeUnmount(() => {
  clearInterval(clock)
  if (unlock !== null) clearTimeout(unlock)
})
const busyRows = reactive<Record<string, boolean>>({})

// Runs an action of a row once. A second call of the same row waits until the first ends.
async function guarded(row: PairingRow, fn: () => Promise<void>): Promise<void> {
  const k = rowKey(row)
  if (busyRows[k]) return
  busyRows[k] = true
  try {
    await fn()
  } finally {
    busyRows[k] = false
  }
}
const codes = reactive<Record<string, string>>({})
const locked = computed(
  () => !!lockedUntil.value && new Date(lockedUntil.value).getTime() > now.value,
)

watch(lockedUntil, (v) => {
  if (unlock !== null) clearTimeout(unlock)
  unlock = null
  if (!v) return
  const ms = new Date(v).getTime() - Date.now()
  if (Number.isNaN(ms) || ms <= 0) return
  unlock = setTimeout(
    () => {
      now.value = Date.now()
    },
    Math.min(ms, 2_147_483_647) + 50,
  )
})

// Returns the local time of the lock in the shared format.
function hhmm(s: string | null): string {
  return shortTime(s)
}

const pendingColumns = [
  { key: "name", label: "Person" },
  { key: "since", label: "Asked" },
  { key: "expires", label: "Expires" },
  { key: "code", label: "Code" },
]
const revokedColumns = [
  { key: "name", label: "Person" },
  { key: "since", label: "Since" },
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
  return shortTime(s) || "—"
}

function inMinutes(s: string): string {
  const t = new Date(s).getTime()
  if (Number.isNaN(t)) return "—"
  return `in ${Math.max(Math.round((t - now.value) / 60000), 0)} min`
}

async function load(): Promise<void> {
  try {
    const v = await api<AccessView>("GET", "/api/pairing")
    pending.value = v.pending ?? []
    approved.value = v.approved ?? []
    revoked.value = v.revoked ?? []
    pairingOn.value = v.pairing_on
    lockedUntil.value = v.locked_until
    triesLeft.value = v.tries_left
    state.value = "ready"
  } catch (e) {
    errorText.value = friendlyError(e)
    state.value = "error"
  }
}

const { stale, updatedAt, refresh } = useLiveData(load, {
  events: ["card.done"],
  filter: (_n, d) => (d as { kind?: string }).kind === "signin",
})

function approve(row: PairingRow): Promise<void> {
  return guarded(row, () => approveRow(row))
}

async function approveRow(row: PairingRow): Promise<void> {
  if (locked.value) return
  const code = (codes[rowKey(row)] ?? "").trim()
  if (code.length !== 8) {
    toast("The code has 8 characters.", "error")
    return
  }
  const ok = await confirm({
    title: `Approve ${row.name}?`,
    body: "This person becomes an owner of the agent.",
    confirmLabel: "Approve",
    danger: true,
  })
  if (!ok) return
  try {
    await api("POST", "/api/pairing/approve", { platform: row.platform, code: code.toUpperCase() })
    toast(`Approved ${row.name}.`)
  } catch (e) {
    toast(friendlyError(e), "error")
  }
  await refresh()
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
    toast(`Revoked ${row.name}.`)
  } catch (e) {
    toast(friendlyError(e), "error")
  }
  await refresh()
}

function deny(row: PairingRow): Promise<void> {
  return guarded(row, () => denyRow(row))
}

async function denyRow(row: PairingRow): Promise<void> {
  try {
    await api("POST", "/api/pairing/revoke", { platform: row.platform, user_id: row.user_id })
    toast(`Denied ${row.name}.`)
  } catch (e) {
    toast(friendlyError(e), "error")
  }
  await refresh()
}
</script>

<template>
  <div class="mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 md:p-6">
    <div class="flex flex-wrap items-center gap-3">
      <h1 class="text-xl font-semibold tracking-tight">Access</h1>
      <LiveHeader :updated-at="updatedAt" :stale="stale" @refresh="refresh" />
      <RouterLink :to="{ name: 'start' }" class="text-sm underline-offset-4 hover:underline"
        >Getting started</RouterLink
      >
    </div>
    <p class="text-muted-foreground">
      Telegram pairing. An approved person is an owner: they can chat, approve commands, and change
      settings.
    </p>

    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock
      v-else-if="state === 'error'"
      state="error"
      :text="errorText"
      action="Retry"
      @action="refresh"
    />
    <Alert v-else-if="!pairingOn" role="status">
      <AlertDescription>
        Pairing needs Telegram. Set <code>telegram.enabled: true</code>, then restart the gateway.
      </AlertDescription>
    </Alert>
    <template v-else>
      <h2 class="flex items-center gap-2 text-base font-semibold">
        Pending <Badge variant="secondary">{{ pending.length }}</Badge>
      </h2>
      <Alert v-if="locked" variant="destructive" role="status">
        <AlertDescription>
          Pairing is locked until {{ hhmm(lockedUntil) }} after too many wrong codes.
        </AlertDescription>
      </Alert>
      <StateBlock
        v-if="pending.length === 0"
        state="empty"
        title="No pairing requests."
        text="A person who sends /start to the bot gets a code. They tell you the code, and you approve it here."
      />
      <DataTable v-else :columns="pendingColumns" :rows="asRows(pending)" row-key="key">
        <template #cell-name="{ row }">
          <b class="font-medium">{{ asRow(row).name }}</b>
          <div class="font-mono text-xs text-muted-foreground">
            {{ asRow(row).platform }} · {{ asRow(row).user_id }}
          </div>
        </template>
        <template #cell-since="{ row }">
          <span class="whitespace-nowrap" :title="fullTime(asRow(row).since)">{{
            when(asRow(row).since)
          }}</span>
        </template>
        <template #cell-expires="{ row }">{{ inMinutes(asRow(row).expires) }}</template>
        <template #cell-code="{ row }">
          <div class="flex flex-wrap items-center gap-2">
            <Label class="sr-only" :for="'code-' + rowKey(asRow(row))"
              >Code of {{ asRow(row).name }}</Label
            >
            <Input
              :id="'code-' + rowKey(asRow(row))"
              v-model="codes[rowKey(asRow(row))]"
              class="w-36 font-mono"
              maxlength="8"
              placeholder="8-char code"
              :aria-label="`Code of ${asRow(row).name}`"
              autocapitalize="characters"
              autocomplete="off"
              spellcheck="false"
              @keydown.enter.prevent="approve(asRow(row))"
            />
            <Button
              type="button"
              :disabled="locked || busyRows[rowKey(asRow(row))]"
              @click="approve(asRow(row))"
            >
              Approve
            </Button>
            <Button
              variant="outline"
              type="button"
              :disabled="busyRows[rowKey(asRow(row))]"
              @click="deny(asRow(row))"
            >
              Deny
            </Button>
          </div>
        </template>
      </DataTable>
      <p class="text-sm text-muted-foreground">
        The person reads the code from the bot. 5 wrong codes lock pairing for 1 hour<template
          v-if="triesLeft !== null"
        >
          ({{ triesLeft }} {{ triesLeft === 1 ? "try" : "tries" }} left)</template
        >.
      </p>

      <h2 class="text-base font-semibold">Approved</h2>
      <DataTable
        v-if="approved.length > 0"
        :columns="approvedColumns"
        :rows="asRows(approved)"
        row-key="key"
      >
        <template #cell-name="{ row }">
          <b class="font-medium">{{ asRow(row).name }}</b>
          <div class="font-mono text-xs text-muted-foreground">
            {{ asRow(row).platform }} · {{ asRow(row).user_id }}
          </div>
        </template>
        <template #cell-role><Badge variant="outline">owner</Badge></template>
        <template #cell-since="{ row }">
          <span class="whitespace-nowrap" :title="fullTime(asRow(row).since)">{{
            when(asRow(row).since)
          }}</span>
        </template>
        <template #cell-actions="{ row }">
          <Button variant="destructive" size="sm" type="button" @click="revoke(asRow(row))">
            Revoke
          </Button>
        </template>
      </DataTable>
      <StateBlock v-else state="empty" text="Nobody is approved yet." />

      <section v-if="revoked.length > 0" aria-labelledby="revoked-h" class="flex flex-col gap-2">
        <h2 id="revoked-h" class="text-base font-semibold">Revoked</h2>
        <p class="text-xs text-muted-foreground">They can send /start again.</p>
        <DataTable :columns="revokedColumns" :rows="asRows(revoked)" row-key="key">
          <template #cell-name="{ row }">
            <b class="font-medium">{{ asRow(row).name }}</b>
            <div class="font-mono text-xs text-muted-foreground">
              {{ asRow(row).platform }} · {{ asRow(row).user_id }}
            </div>
          </template>
          <template #cell-since="{ row }">
            <span class="whitespace-nowrap" :title="fullTime(asRow(row).since)">{{
              when(asRow(row).since)
            }}</span>
          </template>
        </DataTable>
      </section>
    </template>
  </div>
</template>
