<script setup lang="ts">
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
  <div class="devices">
    <div class="page-h">
      <span
        >A new device signs in when you tap ✅ Allow in Telegram or on a signed-in device. A sign-in
        lasts dashboard.session_hours.</span
      >
      <button
        class="btn btn-danger-soft"
        type="button"
        :disabled="others === 0"
        @click="revokeOthers"
      >
        Sign out all other devices…
      </button>
    </div>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="cause" @action="load" />
    <DataTable v-else :columns="columns" :rows="rows" row-key="id" :cards-below="1024">
      <template #cell-label="{ row }">
        <svg
          viewBox="0 0 24 24"
          width="16"
          height="16"
          fill="none"
          stroke="currentColor"
          stroke-width="1.75"
          stroke-linecap="round"
          aria-hidden="true"
        >
          <rect x="3" y="5" width="18" height="12" rx="2" />
          <path d="M8 21h8M12 17v4" />
        </svg>
        <b>{{ (row as unknown as DeviceRow).label }}</b
        >{{ " "
        }}<span v-if="(row as unknown as DeviceRow).this_device" class="chip chip-acc"
          >This device</span
        >
        <details>
          <summary>{{ deviceLabel((row as unknown as DeviceRow).browser) }}</summary>
          <span class="mono t-12">{{ (row as unknown as DeviceRow).browser }}</span>
        </details>
      </template>
      <template #cell-ip="{ row }"
        ><span class="mono">{{ (row as unknown as DeviceRow).ip }}</span></template
      >
      <template #cell-created="{ row }"
        ><span
          style="white-space: nowrap"
          :title="fullTime((row as unknown as DeviceRow).created)"
          >{{ shortTime((row as unknown as DeviceRow).created) || "—" }}</span
        ></template
      >
      <template #cell-last_seen="{ row }">{{
        ago((row as unknown as DeviceRow).last_seen, now)
      }}</template>
      <template #cell-expires="{ row }">{{
        until((row as unknown as DeviceRow).expires, now)
      }}</template>
      <template #cell-actions="{ row }">
        <button
          class="btn btn-danger-soft btn-sm"
          type="button"
          @click="revoke(row as unknown as DeviceRow)"
        >
          Sign out
        </button>
      </template>
    </DataTable>
  </div>
</template>
