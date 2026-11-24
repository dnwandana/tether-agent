<script lang="ts">
// The time when this page first saw each open card. Card has no creation time.
const firstSeen = new Map<string, number>()
</script>

<script setup lang="ts">
import { computed, defineComponent, h, onBeforeUnmount, reactive, ref, toRef, watch } from "vue"
import { useRouter } from "vue-router"
import type { Card, CardAnswer } from "../api/types.gen"
import { useOverlay } from "../lib/overlay"
import { answerCard, bulkAnswer, cards } from "../stores/cards"
import { confirm, toast, ui } from "../stores/ui"
import ApprovalCard from "./cards/ApprovalCard.vue"
import ClarifyCard from "./cards/ClarifyCard.vue"
import SecretCard from "./cards/SecretCard.vue"

const router = useRouter()

const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null

// The 1 s timer runs only while the tray is open.
watch(
  () => ui.trayOpen,
  (open) => {
    if (timer) clearInterval(timer)
    timer = null
    if (open) {
      now.value = Date.now()
      timer = setInterval(() => (now.value = Date.now()), 1000)
    }
  },
  { immediate: true },
)
onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})

function close(): void {
  ui.trayOpen = false
}

const trayEl = ref<HTMLElement | null>(null)
useOverlay(toRef(ui, "trayOpen"), trayEl, { onClose: close, modal: true })

// Fills the first-seen map and drops the cards that are gone.
watch(
  () => cards.open.map((c) => c.id),
  (ids) => {
    for (const id of ids) if (!firstSeen.has(id)) firstSeen.set(id, Date.now())
    for (const id of firstSeen.keys()) if (!ids.includes(id)) firstSeen.delete(id)
  },
  { immediate: true },
)

const list = computed(() => cards.open)
const first = computed(() => list.value[0])
const rest = computed(() => list.value.slice(1))
const bulkable = computed(() => list.value.some((c) => c.kind === "approval" || c.kind === "pin"))

// Holds the ids of the rows that show their full card.
const opened = reactive(new Set<string>())

function toggle(id: string): void {
  if (opened.has(id)) opened.delete(id)
  else opened.add(id)
}

function isSendWithFindings(c: Card): boolean {
  return c.header.startsWith("📤") && c.findings.length > 0
}

const humanOnlyCount = computed(
  () =>
    list.value.filter((c) => (c.kind === "approval" || c.kind === "pin") && c.human_only).length,
)

function icon(c: Card): string {
  if (c.kind === "approval" && c.human_only) return "🔒"
  if (c.header.startsWith("📤")) return "📤"
  if (c.kind === "approval") return "🛡"
  if (c.kind === "clarify") return "❓"
  return "🔑"
}

function label(c: Card): string {
  return c.header.replace(/^\s*\p{Extended_Pictographic}️?\s*/u, "")
}

function detail(c: Card): string {
  if (c.kind === "signin") return `${c.device} · ${c.ip} · check code ${c.code}`
  if (c.kind === "secret") return c.secret_name
  return c.command || c.summary
}

// Returns "now", "N min", or "N h" for the time since the page first saw the card.
function age(c: Card): string {
  const s = Math.max(0, Math.floor((now.value - (firstSeen.get(c.id) ?? now.value)) / 1000))
  if (s < 60) return "now"
  if (s < 3600) return `${Math.floor(s / 60)} min`
  return `${Math.floor(s / 3600)} h`
}

// Returns the CardAnswer for a button answer.
function plain(choice: string): CardAnswer {
  return { answer: choice, choices: [], text: "", value: "" }
}

function fail(e: unknown): void {
  toast(e instanceof Error ? e.message : String(e), "error")
}

// Holds the ids of the sign-in cards whose answer is sent. A second tap must not send a second post.
const busy = reactive(new Set<string>())

async function answer(c: Card, a: string): Promise<void> {
  if (busy.has(c.id)) return
  busy.add(c.id)
  try {
    await answerCard(c.id, plain(a))
  } catch (e) {
    fail(e)
  } finally {
    busy.delete(c.id)
  }
}

// Renders a card in full. The full card and an opened row use this one renderer.
const TrayCard = defineComponent({
  props: { card: { type: Object as () => Card, required: true } },
  setup(p) {
    return () => {
      const c = p.card
      const id = "tray-card-" + c.id
      if (c.kind === "approval" || c.kind === "pin") return h(ApprovalCard, { card: c, id })
      if (c.kind === "clarify") return h(ClarifyCard, { card: c, id })
      if (c.kind === "secret") return h(SecretCard, { card: c, id })
      return h("div", { class: "tray-signin", id }, [
        h("b", label(c)),
        h("span", { class: "truncate" }, detail(c)),
        h("div", { class: "row" }, [
          h(
            "button",
            {
              type: "button",
              class: "btn btn-secondary btn-sm",
              disabled: busy.has(c.id),
              onClick: () => answer(c, "deny"),
            },
            "❌ Deny",
          ),
        ]),
        h("div", { class: "row" }, [
          h(
            "button",
            {
              type: "button",
              class: "btn btn-primary btn-sm",
              disabled: busy.has(c.id),
              onClick: () => answer(c, "allow"),
            },
            "✅ Allow",
          ),
        ]),
      ])
    }
  },
})

function plural(n: number, one: string, many: string): string {
  return n === 1 ? `1 ${one}` : `${n} ${many}`
}

// Denies each open approval and pin card that is not human-only.
async function denyAll(): Promise<void> {
  const ids = list.value
    .filter((c) => (c.kind === "approval" || c.kind === "pin") && !c.human_only)
    .map((c) => c.id)
  if (ids.length === 0) {
    toast("No approval can be denied with “Deny all”. Human-only actions need their own answer.")
    return
  }
  try {
    const n = await bulkAnswer(ids, "deny")
    toast(`Denied ${plural(n, "approval", "approvals")}. Questions stay open.`)
  } catch (e) {
    fail(e)
  }
}

// Approves the shown approval cards once, after a confirm that lists each command as text.
async function approveAll(): Promise<void> {
  const shown = list.value.filter(
    (c) => c.kind === "approval" && !c.human_only && !isSendWithFindings(c),
  )
  const sends = list.value.filter((c) => c.kind === "approval" && isSendWithFindings(c)).length
  if (shown.length === 0) {
    toast(
      "No approval can be answered with “Approve all”. Human-only actions need their own answer.",
    )
    return
  }
  const ids = shown.map((c) => c.id)
  let sendLine = ""
  if (sends === 1) sendLine = "\n1 📤 send with findings stays open. Answer it on its card."
  else if (sends > 1)
    sendLine = `\n${sends} 📤 sends with findings stay open. Answer them on their cards.`
  const ok = await confirm({
    title: `Approve ${plural(ids.length, "command", "commands")} once?`,
    body: "Each answer is “once”. Human-only actions stay open." + sendLine,
    items: shown.map((c) => c.command || c.summary),
    confirmLabel: "✅ Approve all once",
    focus: "cancel",
  })
  if (!ok) return
  try {
    const n = await bulkAnswer(ids, "once")
    toast(`Approved ${plural(n, "command", "commands")} once.`, "ok")
  } catch (e) {
    fail(e)
  }
}

function openCard(c: Card): void {
  close()
  void router.push({ name: "chat", hash: "#card-" + c.id })
}
</script>

<template>
  <div v-if="ui.trayOpen" class="ov scrim is-open tray-ov" @click.self="close">
    <div ref="trayEl" class="tray" role="dialog" aria-label="Waiting for you" tabindex="-1">
      <div class="grabber" aria-hidden="true" />
      <div class="tray-h">
        <b>Waiting for you</b>
        <span class="badge" :class="{ 'badge-bad': list.length > 0 }">{{ list.length }}</span>
        <span class="grow" />
        <template v-if="bulkable">
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            data-act="deny-all"
            @click="denyAll"
          >
            ❌ Deny all
          </button>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            data-act="approve-all"
            @click="approveAll"
          >
            ✅ Approve all once…
          </button>
        </template>
      </div>
      <p v-if="humanOnlyCount > 0" class="tray-note t-12 c-3">
        {{ humanOnlyCount }} 🔒 human-only
        {{ humanOnlyCount === 1 ? "approval stays" : "approvals stay" }} open.
      </p>
      <div class="tray-list">
        <div v-if="first" class="tray-full">
          <TrayCard :card="first" />
          <div class="row">
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              data-act="open"
              @click="openCard(first)"
            >
              Open
            </button>
          </div>
        </div>
        <div v-for="c in rest" :key="c.id" class="tray-row">
          <div class="tray-row-h">
            <span class="k">{{ icon(c) }}</span>
            <span class="grow truncate">{{ detail(c) || label(c) }}</span>
            <span class="muted t-12">{{ age(c) }}</span>
            <button type="button" class="btn btn-ghost btn-sm" @click="toggle(c.id)">
              {{ opened.has(c.id) ? "Hide ▴" : "Show ▾" }}
            </button>
          </div>
          <TrayCard v-if="opened.has(c.id)" :card="c" />
        </div>
        <p v-if="list.length === 0" class="empty-hint">
          Nothing waits. A card shows up here when a dashboard turn asks you something, or when a
          new device asks to sign in.
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.muted {
  color: var(--text-3);
}
.tray-full {
  padding-bottom: 8px;
}
.tray-row {
  border-top: 1px solid var(--border);
  padding: 6px 0;
}
.tray-row-h {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.tray-note {
  margin: 0 0 8px;
}
</style>
