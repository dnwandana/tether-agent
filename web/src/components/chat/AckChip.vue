<script setup lang="ts">
import { computed } from "vue"
import type { AckView } from "../../api/types.gen"
import { Button } from "@/components/ui/button"
import { Marker, MarkerContent } from "@/components/ui/marker"
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

const tone = computed<Tone | undefined>(() => TONE_OF[props.ack.kind])
const buttons = computed(() => (props.ack.buttons ?? []).flat())
</script>

<template>
  <Marker :data-tone="tone" :class="cn('w-auto flex-wrap', tone && TONE_CLASS[tone])">
    <MarkerContent>{{ ack.text }}</MarkerContent>
    <Button
      v-for="(b, i) in buttons"
      :key="i"
      type="button"
      variant="ghost"
      size="xs"
      @click="emit('action', b.data)"
    >
      {{ b.text }}
    </Button>
  </Marker>
</template>
