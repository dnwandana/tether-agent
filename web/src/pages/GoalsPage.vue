<script setup lang="ts">
import { onMounted, ref } from "vue"
import { api, ApiError, runCommand } from "../api/client"
import type { Button, GoalRowView, ReplyView } from "../api/types.gen"
import { session } from "../stores/session"
import { confirm, toast } from "../stores/ui"
import StateBlock from "../components/StateBlock.vue"

const finished = ref(false)
const rows = ref<GoalRowView[]>([])
const state = ref<"loading" | "error" | "ready">("loading")
const errText = ref("")

const SOURCES: Record<string, string> = { dashboard: "Dashboard", telegram: "Telegram", cli: "CLI" }
function sourceLabel(s: string): string {
  return SOURCES[s] ?? s
}

// True for the goal of the active dashboard session while it can still change.
function mine(g: GoalRowView): boolean {
  return (
    g.session_id !== "" &&
    g.session_id === session.id &&
    (g.status === "active" || g.status === "paused")
  )
}

// Returns the state shown in the chip.
function chipState(g: GoalRowView): string {
  return g.status === "active" && g.waiting ? "parked" : g.status
}
const CHIP: Record<string, string> = {
  active: "chip-acc",
  parked: "chip-info",
  paused: "chip-warn",
  done: "chip-ok",
  cleared: "chip-plain",
}

function line(g: GoalRowView): string {
  let t = `${sourceLabel(g.source)} · “${g.title}” · ${g.turns_used}/${g.max_turns} turns`
  if (g.status === "paused" && g.paused_reason) t += ` · paused: ${g.paused_reason}`
  if (g.waiting) t += ` · waits on ${g.wait_target}`
  return t
}

function isConfirm(b: Button): boolean {
  return b.data.endsWith("confirm") || b.data.startsWith("/goal gate ok ")
}

async function act(text: string): Promise<void> {
  try {
    const r = await runCommand(text)
    if (!("text" in r)) {
      toast("The command runs. The reply comes later.", "info")
    } else {
      const reply = r as ReplyView
      const all = (reply.buttons ?? []).flat()
      const yes = all.find(isConfirm)
      if (yes) {
        const nl = reply.text.indexOf("\n")
        const title = nl < 0 ? reply.text : reply.text.slice(0, nl)
        const body = nl < 0 ? "" : reply.text.slice(nl + 1)
        if (await confirm({ title, body, confirmLabel: yes.text, danger: true })) {
          await act(yes.data)
          return
        }
        const no = all.find((b) => b.data.startsWith("/goal gate no "))
        if (no) await act(no.data)
      } else {
        toast(reply.text, "ok")
      }
    }
  } catch (e) {
    toast(e instanceof Error ? e.message : String(e), "error")
  }
  await load()
}

async function clearGoal(): Promise<void> {
  const ok = await confirm({
    title: "Clear the goal?",
    body: "The goal stops, and its budget, gates, and subgoals are cleared.",
    confirmLabel: "Clear the goal",
    danger: true,
  })
  if (ok) await act("/goal clear")
}

async function load(): Promise<void> {
  try {
    rows.value = await api<GoalRowView[]>(
      "GET",
      finished.value ? "/api/goals?finished=1" : "/api/goals",
    )
    state.value = "ready"
  } catch (e) {
    errText.value = e instanceof ApiError || e instanceof Error ? e.message : String(e)
    state.value = "error"
  }
}

async function pick(f: boolean): Promise<void> {
  finished.value = f
  state.value = "loading"
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-h">
      <h1 class="t-20">Goals</h1>
      <div class="seg">
        <button type="button" :class="{ 'is-on': !finished }" @click="pick(false)">
          Active · parked · paused
        </button>
        <button type="button" :class="{ 'is-on': finished }" @click="pick(true)">
          Finished · 30 days
        </button>
      </div>
    </div>
    <p v-if="finished" class="pill-note">Last goal of each session</p>
    <StateBlock v-if="state === 'loading'" state="loading" />
    <StateBlock v-else-if="state === 'error'" state="error" :text="errText" @retry="load" />
    <StateBlock
      v-else-if="rows.length === 0"
      state="empty"
      text="No goals. Send /goal <text> in Chat to start one."
    />
    <div v-else class="list card">
      <div v-for="g in rows" :key="g.session_id" class="li top">
        <span class="chip" :class="CHIP[chipState(g)]">{{ chipState(g) }}</span>
        <div class="grow">
          <b
            ><router-link :to="{ name: 'goal', params: { session: g.session_id } }">{{
              g.goal
            }}</router-link></b
          >
          <div class="t-12 c-3">{{ line(g) }}</div>
        </div>
        <template v-if="mine(g)">
          <button
            v-if="g.status === 'paused'"
            class="btn btn-secondary btn-sm"
            type="button"
            @click="act('/goal resume')"
          >
            ▶ Resume
          </button>
          <button v-else class="btn btn-secondary btn-sm" type="button" @click="act('/goal pause')">
            ⏸ Pause
          </button>
          <button class="btn btn-danger-soft btn-sm" type="button" @click="clearGoal">Clear</button>
        </template>
        <template v-else>
          <span class="c-3">read-only</span>
          <router-link
            class="btn btn-secondary btn-sm"
            :to="{ name: 'transcript', params: { id: g.session_id } }"
            >Open session</router-link
          >
        </template>
      </div>
    </div>
    <p class="pill-note">
      Actions work on the goal of the dashboard session. Goals of Telegram and CLI sessions are
      read-only here.
    </p>
  </div>
</template>
