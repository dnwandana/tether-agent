<script setup lang="ts">
import { computed } from "vue"
import type { AckView } from "../../api/types.gen"

const props = defineProps<{ ack: AckView }>()
const emit = defineEmits<{ action: [data: string] }>()

// The ack class of each kind. An unknown kind has no extra class.
const KIND: Record<string, string> = {
  pending: "ack-pending",
  queue: "ack-pending",
  queued: "ack-pending",
  steer: "ack-run",
  run: "ack-run",
  ok: "ack-ok",
  done: "ack-ok",
  read: "ack-ok",
  busy: "ack-warn",
  warn: "ack-warn",
  error: "ack-bad",
  bad: "ack-bad",
}

const cls = computed(() => KIND[props.ack.kind] ?? "")
const buttons = computed(() => (props.ack.buttons ?? []).flat())
</script>

<template>
  <span class="ack" :class="cls">
    {{ ack.text }}
    <button
      v-for="(b, i) in buttons"
      :key="i"
      type="button"
      class="btn btn-ghost btn-sm"
      @click="emit('action', b.data)"
    >
      {{ b.text }}
    </button>
  </span>
</template>
