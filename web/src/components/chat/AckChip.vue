<script setup lang="ts">
import { computed } from "vue"
import type { AckView } from "../../api/types.gen"
import { Button } from "@/components/ui/button"
import { Marker, MarkerContent } from "@/components/ui/marker"
import ButtonLabel from "@/components/ButtonLabel.vue"
import { buttonLabel, type GlyphTone } from "@/lib/glyphs"
import { renderSystemText } from "@/lib/markdown"
import { cn } from "@/lib/utils"

type Tone = "pending" | "run" | "ok" | "warn" | "bad"

const props = defineProps<{ ack: AckView }>()
const emit = defineEmits<{ action: [data: string] }>()

// The tone of each ack kind. An unknown kind has no tone.
const TONE_OF: Record<string, Tone> = {
  pending: "pending",
  queue: "pending",
  queued: "pending",
  steer: "run",
  run: "run",
  ok: "ok",
  done: "ok",
  read: "ok",
  busy: "warn",
  warn: "warn",
  error: "bad",
  bad: "bad",
}

// The text color of each tone. It uses the status tokens only.
const TONE_CLASS: Record<Tone, string> = {
  pending: "text-muted-foreground",
  run: "text-info",
  ok: "text-success",
  warn: "text-warning",
  bad: "text-destructive",
}

// The glyph tone of each ack tone. A lead glyph of this tone repeats the chip color, so it goes.
const GLYPH_TONE: Record<Tone, GlyphTone> = {
  pending: "muted",
  run: "info",
  ok: "ok",
  warn: "warn",
  bad: "bad",
}

const tone = computed<Tone | undefined>(() => TONE_OF[props.ack.kind])
const html = computed(() =>
  renderSystemText(props.ack.text, {
    inline: true,
    lead: "drop",
    kind: tone.value && GLYPH_TONE[tone.value],
  }),
)
const buttons = computed(() => (props.ack.buttons ?? []).flat())
</script>

<template>
  <Marker :data-tone="tone" :class="cn('w-auto flex-wrap', tone && TONE_CLASS[tone])">
    <!-- eslint-disable-next-line vue/no-v-html -- renderSystemText escapes raw HTML -->
    <MarkerContent v-html="html" />
    <Button
      v-for="(b, i) in buttons"
      :key="i"
      type="button"
      variant="ghost"
      size="xs"
      :aria-pressed="buttonLabel(b.text).current || undefined"
      @click="emit('action', b.data)"
    >
      <ButtonLabel :text="b.text" />
    </Button>
  </Marker>
</template>
