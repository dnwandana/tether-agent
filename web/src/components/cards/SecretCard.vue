<script setup lang="ts">
import { ref, onMounted, onUnmounted, type Ref } from "vue"
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
const busy = ref(false)
const error = ref("")
const value = ref("")

// Posts the value. The input is empty after the post, so the card keeps no copy.
async function save(): Promise<void> {
  if (!value.value || busy.value) return
  const v = value.value
  busy.value = true
  error.value = ""
  try {
    await answerCard(props.card.id, {
      answer: "",
      choices: [],
      text: "",
      value: v,
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    value.value = ""
    busy.value = false
  }
}

// Posts the deny answer.
async function deny(): Promise<void> {
  busy.value = true
  error.value = ""
  try {
    await answerCard(props.card.id, {
      answer: "deny",
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
      <dl class="ikv">
        <dt>For</dt>
        <dd style="white-space: pre-wrap">{{ card.command }}</dd>
        <dt>Reason</dt>
        <dd>{{ card.reason }}</dd>
      </dl>
      <input
        v-model="value"
        class="input masked"
        type="password"
        autocomplete="off"
        spellcheck="false"
        placeholder="Paste the value"
        @keydown.enter="save"
      />
      <div v-if="card.answers.includes('deny')" class="icard-row">
        <button class="btn btn-danger-soft" type="button" :disabled="busy" @click="deny">
          ❌ Deny
        </button>
      </div>
      <div class="icard-row">
        <button class="btn btn-primary" type="button" :disabled="busy || !value" @click="save">
          Save
        </button>
      </div>
      <p class="t-12 c-3">
        The value goes into .env on the host. It is never shown again, and the model sees only
        “Saved”.
      </p>
      <div v-if="error" class="t-12 c-bad">{{ error }}</div>
    </div>
  </div>
</template>
