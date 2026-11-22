<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue"
import { useRouter } from "vue-router"
import type { Card, CardAnswer } from "../api/types.gen"
import { answerCard, bulkAnswer, cards } from "../stores/cards"
import { confirm, toast } from "../stores/ui"

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ close: [] }>()
const router = useRouter()

const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | null = null

// The 1 s timer runs only while the tray is open.
watch(
  () => props.open,
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

const list = computed(() => cards.open)
const bulkable = computed(() => list.value.some((c) => c.kind === "approval" || c.kind === "pin"))

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

// Returns the time to the deadline as m:ss, or an empty string when there is no deadline.
function left(c: Card): string {
  const t = Date.parse(c.deadline)
  if (!Number.isFinite(t) || t < 86_400_000) return ""
  const s = Math.max(0, Math.ceil((t - now.value) / 1000))
  return `⌛ ${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`
}

// Returns the CardAnswer for a button answer.
function plain(choice: string): CardAnswer {
  return { answer: choice, choices: [], text: "", value: "" }
}

function canOnce(c: Card): boolean {
  return c.answers.includes("once")
}

function fail(e: unknown): void {
  toast(e instanceof Error ? e.message : String(e), "error")
}

// Holds the ids of the cards whose answer is sent. A second tap must not send a second post.
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

// Approves the shown approval cards once, after a confirm that lists each command.
async function approveAll(): Promise<void> {
  const shown = list.value.filter((c) => c.kind === "approval" && !c.human_only)
  if (shown.length === 0) {
    toast(
      "No approval can be answered with “Approve all”. Human-only actions need their own answer.",
    )
    return
  }
  const ids = shown.map((c) => c.id)
  const ok = await confirm({
    title: `Approve ${plural(ids.length, "command", "commands")} once?`,
    body:
      shown.map((c) => c.command || c.summary).join("\n") +
      "\n\nHuman-only actions stay open. Each answer is “once”.",
    confirmLabel: "✅ Approve all once",
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
  emit("close")
  void router.push({ name: "chat", hash: "#card-" + c.id })
}
</script>

<template>
  <div v-if="open" class="ov scrim is-open tray-ov" @click.self="emit('close')">
    <div class="tray" role="dialog" aria-label="Waiting for you">
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
            ✅ Approve all
          </button>
        </template>
      </div>
      <div class="tray-list">
        <div v-for="c in list" :key="c.id" class="tray-item">
          <span class="k">{{ icon(c) }}</span>
          <div class="grow">
            <b>{{ label(c) }}</b>
            <span v-if="detail(c)" class="truncate">{{ detail(c) }}</span>
            <span v-if="left(c)" class="muted">{{ left(c) }}</span>
            <template v-if="c.kind === 'signin'">
              <div class="row">
                <button
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="busy.has(c.id)"
                  @click="answer(c, 'deny')"
                >
                  ❌ Deny
                </button>
              </div>
              <div class="row">
                <button
                  type="button"
                  class="btn btn-primary btn-sm"
                  :disabled="busy.has(c.id)"
                  @click="answer(c, 'allow')"
                >
                  ✅ Allow
                </button>
              </div>
            </template>
            <div v-else class="row">
              <template v-if="c.kind === 'approval'">
                <button
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="busy.has(c.id)"
                  @click="answer(c, 'deny')"
                >
                  ❌ Deny
                </button>
                <button
                  v-if="canOnce(c)"
                  type="button"
                  class="btn btn-secondary btn-sm"
                  :disabled="busy.has(c.id)"
                  @click="answer(c, 'once')"
                >
                  ✅ Once
                </button>
              </template>
              <button
                type="button"
                class="btn btn-ghost btn-sm"
                data-act="open"
                @click="openCard(c)"
              >
                Open
              </button>
            </div>
          </div>
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
@media (min-width: 600px) {
  .tray-ov {
    align-items: flex-start;
    justify-content: flex-start;
    padding: 48px 12px;
  }
}
</style>
