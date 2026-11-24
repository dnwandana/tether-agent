<script setup lang="ts">
import { MonitorIcon } from "@lucide/vue"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { deviceLabel } from "../lib/browser"
import { computed, onMounted, ref } from "vue"
import { useRouter } from "vue-router"
import { api, setSessionHeader } from "../api/client"
import type { DeviceRow } from "../api/types.gen"
import { friendlyError } from "../lib/errors"
import { fullTime, shortTime } from "../lib/time"
import { confirm, toast } from "../stores/ui"
import DataTable from "./DataTable.vue"
import StateBlock from "./StateBlock.vue"

const router = useRouter()
const devices = ref<DeviceRow[]>([])
const state = ref<"loading" | "error" | "ready">("loading")
const cause = ref("")
const now = ref(Date.now())

const columns = [
  { key: "label", label: "Device" },
  { key: "ip", label: "IP" },
  { key: "created", label: "Signed in" },
  { key: "last_seen", label: "Last seen" },
  { key: "expires", label: "Expires" },
  { key: "actions", label: "", align: "right" as const },
]

const others = computed(() => devices.value.filter((d) => !d.this_device).length)
const rows = computed(() => devices.value as unknown as Record<string, unknown>[])

// Formats a past time as "now", "12 min ago", or "9 h ago".
function ago(iso: string, at: number): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ""
  const min = Math.floor((at - t) / 60000)
  if (min < 1) return "now"
  if (min < 60) return `${min} min ago`
  return `${Math.floor(min / 60)} h ago`
}

// Formats a future time as "in 21 h" or "in 5 min".
function until(iso: string, at: number): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ""
  const min = Math.max(0, Math.floor((t - at) / 60000))
  if (min < 60) return `in ${min} min`
  return `in ${Math.floor(min / 60)} h`
}

function fail(e: unknown): void {
  toast(friendlyError(e), "error")
}

async function load(): Promise<void> {
  try {
    devices.value = await api<DeviceRow[]>("GET", "/api/devices")
    now.value = Date.now()
    state.value = "ready"
  } catch (e) {
    cause.value = friendlyError(e)
    state.value = "error"
  }
}

async function revoke(d: DeviceRow): Promise<void> {
  const body = d.this_device
    ? "This is the device you use now. You go to the sign-in screen."
    : `${deviceLabel(d.browser)} at ${d.ip} must ask for access again.`
  const ok = await confirm({
    title: `Sign out ${d.label}?`,
    body,
    confirmLabel: "Sign out",
    danger: true,
  })
  if (!ok) return
  try {
    await api("POST", `/api/devices/${d.id}/revoke`)
  } catch (e) {
    fail(e)
    return
  }
  if (d.this_device) {
    setSessionHeader(null)
    await router.push({ name: "signin" })
    return
  }
  toast(`Signed out ${d.label}.`)
  await load()
}

async function revokeOthers(): Promise<void> {
  const ok = await confirm({
    title: `Sign out ${others.value} other ${others.value === 1 ? "device" : "devices"}?`,
    body: "Each of them must ask for access again. This device stays signed in.",
    confirmLabel: "Sign out all others",
    danger: true,
  })
  if (!ok) return
  try {
    await api("POST", "/api/devices/revoke-others")
  } catch (e) {
    fail(e)
    return
  }
  toast("Signed out all other devices.")
  await load()
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="text-sm text-muted-foreground">
        A new device signs in when you tap ✅ Allow in Telegram or on a signed-in device. A sign-in
        lasts dashboard.session_hours.
      </p>
      <Button variant="outline" type="button" :disabled="others === 0" @click="revokeOthers">
        Sign out all other devices…
      </Button>
    </div>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="cause" @action="load" />
    <DataTable v-else :columns="columns" :rows="rows" row-key="id" :cards-below="1024">
      <template #cell-label="{ row }">
        <MonitorIcon aria-hidden="true" />
        <b class="font-medium">{{ (row as unknown as DeviceRow).label }}</b
        >{{ " "
        }}<Badge v-if="(row as unknown as DeviceRow).this_device" variant="secondary"
          >This device</Badge
        >
        <details>
          <summary>{{ deviceLabel((row as unknown as DeviceRow).browser) }}</summary>
          <span class="font-mono text-xs break-all">{{
            (row as unknown as DeviceRow).browser
          }}</span>
        </details>
      </template>
      <template #cell-ip="{ row }"
        ><span class="font-mono">{{ (row as unknown as DeviceRow).ip }}</span></template
      >
      <template #cell-created="{ row }"
        ><span class="whitespace-nowrap" :title="fullTime((row as unknown as DeviceRow).created)">{{
          shortTime((row as unknown as DeviceRow).created) || "—"
        }}</span></template
      >
      <template #cell-last_seen="{ row }">{{
        ago((row as unknown as DeviceRow).last_seen, now)
      }}</template>
      <template #cell-expires="{ row }">{{
        until((row as unknown as DeviceRow).expires, now)
      }}</template>
      <template #cell-actions="{ row }">
        <Button
          variant="outline"
          size="sm"
          type="button"
          class="max-tab:h-11"
          @click="revoke(row as unknown as DeviceRow)"
        >
          Sign out
        </Button>
      </template>
    </DataTable>
  </div>
</template>
