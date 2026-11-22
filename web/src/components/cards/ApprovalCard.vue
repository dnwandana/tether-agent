<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, type Ref } from "vue"
import type { Card } from "../../api/types.gen"
import { answerCard } from "../../stores/cards"

const props = defineProps<{ card: Card }>()

// Returns the text "⌛ m:ss" and the bar width in percent. It ticks once a second and stops on unmount.
function useCountdown(deadline: () => string): {
  text: Ref<string>
  pct: Ref<number>
} {
  const text = ref("")
  const pct = ref(100)
  let total = 0
  let timer: ReturnType<typeof setInterval> | undefined
  function tick(): void {
    const end = Date.parse(deadline())
    // Go sends the zero time for a card with no deadline. It is before 1970 in Unix time.
    if (!deadline() || Number.isNaN(end) || end < 86_400_000) {
      text.value = ""
      return
    }
    const left = Math.max(0, end - Date.now())
    if (total === 0) total = Math.max(left, 1)
    const s = Math.ceil(left / 1000)
    text.value = `⌛ ${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`
    pct.value = Math.min(100, (left / total) * 100)
  }
  tick()
  onMounted(() => {
    timer = setInterval(tick, 1000)
  })
  onUnmounted(() => {
    if (timer) clearInterval(timer)
  })
  return { text, pct }
}

// Gives the label of each answer of row 2, in the fixed order of the table.
const ROW2 = ["redacted", "once", "session", "always", "allow"] as const
const LABELS: Record<string, string> = {
  redacted: "✅ Send redacted",
  once: "✅ Once",
  session: "✅ Session",
  always: "✅ Always",
  allow: "✅ Allow this program",
}

const cd = useCountdown(() => props.card.deadline)
const busy = ref(false)
const error = ref("")
const hasDeny = computed(() => props.card.answers.includes("deny"))
const row2 = computed(() => ROW2.filter((a) => props.card.answers.includes(a)))

// Posts one answer. A failure shows its text under the buttons.
async function send(answer: string): Promise<void> {
  busy.value = true
  error.value = ""
  try {
    await answerCard(props.card.id, {
      answer,
      choices: [],
      text: "",
      value: "",
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div
    :id="'card-' + card.id"
    class="icard icard-approval"
    :class="{ 'icard-human': card.human_only }"
  >
    <div class="icard-h">
      <span>{{ card.header }}</span>
      <span v-if="card.child" class="t-12 c-3">· {{ card.child }}</span>
      <span v-if="cd.text.value" class="cd">{{ cd.text.value }}</span>
    </div>
    <div v-if="cd.text.value" class="countdown">
      <span :style="{ width: cd.pct.value + '%' }"></span>
    </div>
    <div class="icard-b">
      <div v-if="card.kind === 'pin'">
        <dl class="ikv">
          <dt>Program</dt>
          <dd>{{ card.description }}</dd>
          <dt>SHA-256</dt>
          <dd>{{ card.rule }}</dd>
          <dt>Command</dt>
          <dd>{{ card.command }}</dd>
        </dl>
        <p class="t-12 c-3">
          The program gets the value in its environment. The model never sees the value.
        </p>
      </div>
      <template v-else>
        <div v-if="card.summary" class="t-13">{{ card.summary }}</div>
        <div v-if="card.command" class="cmd">{{ card.command }}</div>
        <pre v-if="card.diff" class="code-block">{{ card.diff }}</pre>
        <div v-if="card.rule || card.description" class="rule">
          <span v-if="card.rule">{{ card.rule }}</span>
          <span v-if="card.description"> {{ card.description }}</span>
        </div>
        <div v-for="(f, i) in card.findings" :key="i" class="finding">⚠ {{ f }}</div>
        <dl v-if="card.workdir || card.via || card.secrets.length" class="ikv">
          <template v-if="card.workdir">
            <dt>Workdir</dt>
            <dd>{{ card.workdir }}</dd>
          </template>
          <template v-if="card.via">
            <dt>Via</dt>
            <dd>{{ card.via }}</dd>
          </template>
          <template v-if="card.secrets.length">
            <dt>Secrets</dt>
            <dd>{{ card.secrets.join(", ") }}</dd>
          </template>
        </dl>
      </template>
      <p v-if="card.human_only" class="t-12 c-3">
        A human-only action needs your answer each time. “Approve all” never answers it.
      </p>
    </div>
    <div class="icard-f">
      <div v-if="hasDeny" class="icard-row">
        <button class="btn btn-danger" type="button" :disabled="busy" @click="send('deny')">
          ❌ Deny
        </button>
      </div>
      <div v-if="row2.length" class="icard-row">
        <button
          v-for="a in row2"
          :key="a"
          class="btn btn-primary"
          type="button"
          :disabled="busy"
          @click="send(a)"
        >
          {{ LABELS[a] }}
        </button>
      </div>
      <div v-if="error" class="t-12 c-bad">{{ error }}</div>
    </div>
  </div>
</template>
