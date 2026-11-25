<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, useId, type Ref } from "vue"
import type { Card } from "../../api/types.gen"
import { answerCard } from "../../stores/cards"
import { splitGlyph } from "../../lib/glyphs"
import GlyphText from "../GlyphText.vue"
import { CheckIcon } from "@lucide/vue"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Item, ItemContent, ItemHeader } from "@/components/ui/item"
import { Label } from "@/components/ui/label"
import { Progress } from "@/components/ui/progress"

const props = defineProps<{ card: Card }>()
const title = computed(() => splitGlyph(props.card.header).rest)

// Returns the text "m:ss left" and the bar width in percent. It ticks once a second and stops on unmount.
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
    text.value = `${clock} left`
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
  <Item
    :id="'card-' + card.id"
    variant="outline"
    role="group"
    :aria-label="title"
    class="min-w-0 bg-card"
  >
    <ItemHeader>
      <span><GlyphText :text="card.header" /></span>
      <span v-if="card.child" class="text-xs text-muted-foreground">· {{ card.child }}</span>
      <span
        v-if="cd.text.value"
        class="ml-auto text-xs text-muted-foreground tabular-nums"
        role="timer"
        :aria-label="cd.aria.value"
        >{{ cd.text.value }}</span
      >
    </ItemHeader>
    <Progress v-if="cd.text.value" :model-value="cd.pct.value" class="h-1 basis-full" />
    <ItemContent class="min-w-0 basis-full gap-2">
      <p v-if="card.summary" class="text-sm break-words whitespace-pre-wrap text-foreground">
        {{ card.summary }}
      </p>
      <template v-if="card.choices.length">
        <div class="flex flex-wrap gap-2">
          <Button
            v-for="c in card.choices"
            :key="c"
            type="button"
            variant="secondary"
            class="max-tab:h-11"
            :disabled="busy"
            :aria-pressed="card.multi ? picked.includes(c) : undefined"
            @click="choose(c)"
          >
            <CheckIcon v-if="card.multi && picked.includes(c)" />{{ c }}
          </Button>
        </div>
        <div v-if="card.multi" data-row class="flex flex-wrap gap-2">
          <Button
            type="button"
            class="max-tab:h-11"
            :disabled="busy || !picked.length"
            @click="done"
          >
            Done
          </Button>
        </div>
      </template>
      <template v-else>
        <Label :for="answerId" class="text-xs text-muted-foreground">Your answer</Label>
        <Input
          :id="answerId"
          v-model="text"
          type="text"
          placeholder="Type your answer"
          @keydown.enter="sendText"
        />
        <div data-row class="flex flex-wrap gap-2">
          <Button type="button" class="max-tab:h-11" :disabled="busy" @click="sendText">
            Send
          </Button>
        </div>
      </template>
      <div v-if="error" role="alert" class="text-xs text-destructive">{{ error }}</div>
    </ItemContent>
  </Item>
</template>
