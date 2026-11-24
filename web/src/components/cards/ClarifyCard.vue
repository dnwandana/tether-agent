<script setup lang="ts">
import { ref, onMounted, onUnmounted, useId, type Ref } from "vue"
import type { Card } from "../../api/types.gen"
import { answerCard } from "../../stores/cards"

const props = defineProps<{ card: Card }>()

// Returns the text "⌛ m:ss" and the bar width in percent. It ticks once a second and stops on unmount.
function useCountdown(deadline: () => string): {
  text: Ref<string>
  pct: Ref<number>
  aria: Ref<string>
} {
  const text = ref("")
  const pct = ref(100)
  const aria = ref("")
  let ticks = 0
  let total = 0
  let timer: ReturnType<typeof setInterval> | undefined
  function tick(): void {
    const end = Date.parse(deadline())
    // Go sends the zero time for a card with no deadline. It is before 1970 in Unix time.
    if (!deadline() || Number.isNaN(end) || end < 86_400_000) {
      text.value = ""
      aria.value = ""
      return
    }
    const left = Math.max(0, end - Date.now())
    if (total === 0) total = Math.max(left, 1)
    const s = Math.ceil(left / 1000)
    const clock = `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`
    text.value = `⌛ ${clock}`
    // A screen reader hears the time on the first tick and on each 15th tick after it.
    if (ticks % 15 === 0) aria.value = `Time left: ${clock}`
    ticks++
    pct.value = Math.min(100, (left / total) * 100)
  }
  tick()
  onMounted(() => {
    timer = setInterval(tick, 1000)
  })
  onUnmounted(() => {
    if (timer) clearInterval(timer)
  })
  return { text, pct, aria }
}

const cd = useCountdown(() => props.card.deadline)
const answerId = useId()
const busy = ref(false)
const error = ref("")
const picked = ref<string[]>([])
const text = ref("")

async function post(a: { choices?: string[]; text?: string }): Promise<void> {
  busy.value = true
  error.value = ""
  try {
    await answerCard(props.card.id, {
      answer: "",
      choices: a.choices ?? [],
      text: a.text ?? "",
      value: "",
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

function choose(c: string): void {
  if (!props.card.multi) {
    void post({ choices: [c] })
    return
  }
  const i = picked.value.indexOf(c)
  if (i >= 0) picked.value.splice(i, 1)
  else picked.value.push(c)
}

function done(): void {
  // Keeps the order of the card choices.
  void post({
    choices: props.card.choices.filter((c) => picked.value.includes(c)),
  })
}

function sendText(): void {
  const t = text.value.trim()
  if (!t || busy.value) return
  void post({ text: t })
}
</script>

<template>
  <div :id="'card-' + card.id" class="icard" role="group" :aria-label="card.header">
    <div class="icard-h">
      <span>{{ card.header }}</span>
      <span v-if="card.child" class="t-12 c-3">· {{ card.child }}</span>
      <span v-if="cd.text.value" class="cd" role="timer" :aria-label="cd.aria.value">{{
        cd.text.value
      }}</span>
    </div>
    <div v-if="cd.text.value" class="countdown">
      <span :style="{ width: cd.pct.value + '%' }"></span>
    </div>
    <div class="icard-b">
      <p v-if="card.summary" class="clarify-q">{{ card.summary }}</p>
      <template v-if="card.choices.length">
        <div class="choices">
          <button
            v-for="c in card.choices"
            :key="c"
            class="btn btn-secondary choice"
            :class="{ 'is-on': picked.includes(c) }"
            type="button"
            :disabled="busy"
            @click="choose(c)"
          >
            <template v-if="card.multi">{{ picked.includes(c) ? "✓ " : "☐ " }}</template
            >{{ c }}
          </button>
        </div>
        <div v-if="card.multi" class="icard-row">
          <button
            class="btn btn-primary"
            type="button"
            :disabled="busy || !picked.length"
            @click="done"
          >
            Done
          </button>
        </div>
      </template>
      <template v-else>
        <label class="t-12 c-3" :for="answerId">Your answer</label>
        <input
          :id="answerId"
          v-model="text"
          class="input"
          type="text"
          placeholder="Type your answer"
          @keydown.enter="sendText"
        />
        <div class="icard-row">
          <button class="btn btn-primary" type="button" :disabled="busy" @click="sendText">
            Send
          </button>
        </div>
      </template>
      <div v-if="error" class="t-12 c-bad">{{ error }}</div>
    </div>
  </div>
</template>

<style scoped>
.clarify-q {
  margin: 0;
  font-size: var(--fs-14);
  line-height: var(--lh-14);
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
