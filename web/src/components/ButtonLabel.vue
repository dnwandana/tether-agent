<script setup lang="ts">
// Shows a button text with no marks, and the Check or warning icon for a current or unusable entry.
import { CheckIcon, TriangleAlertIcon } from "@lucide/vue"
import { computed } from "vue"

import { buttonLabel } from "@/lib/glyphs"

const props = defineProps<{ text: string }>()
const b = computed(() => buttonLabel(props.text))
</script>

<template>
  <CheckIcon v-if="b.current" />
  <template v-if="b.warn">
    <TriangleAlertIcon class="text-warning" />
    <span class="sr-only">not usable</span>
  </template>
  <span>{{ b.label }}</span>
</template>
