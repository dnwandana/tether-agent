<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, type Ref } from "vue"
import type { Card } from "../../api/types.gen"
import { answerCard } from "../../stores/cards"
import { Button } from "@/components/ui/button"
import { Item, ItemContent, ItemFooter, ItemHeader } from "@/components/ui/item"
import { Progress } from "@/components/ui/progress"

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
const hasAlways = computed(() => props.card.answers.includes("always"))
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
  <Item
    variant="outline"
    :id="'card-' + card.id"
    role="group"
    :aria-label="card.header"
    :data-human-only="card.human_only || undefined"
    class="min-w-0 bg-card data-[human-only=true]:border-warning"
  >
    <ItemHeader>
      <span class="font-medium">{{ card.header }}</span>
      <span v-if="card.child" class="text-xs text-muted-foreground">· {{ card.child }}</span>
      <span
        v-if="cd.text.value"
        role="timer"
        :aria-label="cd.aria.value"
        class="ml-auto shrink-0 text-xs tabular-nums text-muted-foreground"
        >{{ cd.text.value }}</span
      >
    </ItemHeader>
    <Progress v-if="cd.text.value" :model-value="cd.pct.value" class="h-1 basis-full" />
    <ItemContent class="min-w-0 basis-full gap-2">
      <template v-if="card.kind === 'pin'">
        <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
          <dt class="text-muted-foreground">Program</dt>
          <dd class="min-w-0 break-all">{{ card.description }}</dd>
          <dt class="text-muted-foreground">SHA-256</dt>
          <dd class="min-w-0 break-all">{{ card.rule }}</dd>
          <dt class="text-muted-foreground">Command</dt>
          <dd class="min-w-0 break-all">{{ card.command }}</dd>
        </dl>
        <p class="text-xs text-muted-foreground">
          The program gets the value in its environment. The model never sees the value.
        </p>
      </template>
      <template v-else>
        <div v-if="card.summary" class="text-sm">{{ card.summary }}</div>
        <code
          v-if="card.command"
          class="block whitespace-pre-wrap break-all rounded-md bg-muted px-2 py-1 font-mono text-xs"
          >{{ card.command }}</code
        >
        <pre
          v-if="card.diff"
          class="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-2 font-mono text-xs"
          >{{ card.diff }}</pre>
        <div v-if="card.rule || card.description" class="text-xs text-muted-foreground">
          <span v-if="card.rule">{{ card.rule }}</span>
          <span v-if="card.description"> {{ card.description }}</span>
        </div>
        <div v-for="(f, i) in card.findings" :key="i" class="text-xs text-warning">⚠ {{ f }}</div>
        <dl
          v-if="card.workdir || card.via || card.secrets.length"
          class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm"
        >
          <template v-if="card.workdir">
            <dt class="text-muted-foreground">Workdir</dt>
            <dd class="min-w-0 break-all">{{ card.workdir }}</dd>
          </template>
          <template v-if="card.via">
            <dt class="text-muted-foreground">Via</dt>
            <dd class="min-w-0 break-all">{{ card.via }}</dd>
          </template>
          <template v-if="card.secrets.length">
            <dt class="text-muted-foreground">Secrets</dt>
            <dd class="min-w-0 break-all">{{ card.secrets.join(", ") }}</dd>
          </template>
        </dl>
      </template>
      <p v-if="card.human_only" class="text-xs text-muted-foreground">
        A human-only action needs your answer each time. “Approve all” never answers it.
      </p>
    </ItemContent>
    <ItemFooter class="flex-col items-stretch gap-2">
      <div v-if="hasDeny" data-row class="flex">
        <Button
          variant="outline"
          type="button"
          :disabled="busy"
          class="max-tab:h-11"
          @click="send('deny')"
        >
          ❌ Deny
        </Button>
      </div>
      <div v-if="row2.length" data-row class="flex flex-wrap gap-2">
        <Button
          v-for="a in row2"
          :key="a"
          :variant="a === 'always' ? 'ghost' : 'secondary'"
          type="button"
          :disabled="busy"
          class="max-tab:h-11"
          @click="send(a)"
        >
          {{ LABELS[a] }}
        </Button>
      </div>
      <p v-if="hasAlways" data-always-hint class="text-xs text-muted-foreground">
        <template v-if="card.workdir">
          ✅ Always: this exact command in <code class="font-mono">{{ card.workdir }}</code
          >, from now on.
        </template>
        <template v-else>✅ Always: this tool, with any arguments, from now on.</template>
      </p>
      <div v-if="error" role="alert" class="text-xs text-destructive">{{ error }}</div>
    </ItemFooter>
  </Item>
</template>
