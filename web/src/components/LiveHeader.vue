<script setup lang="ts">
import { Button } from "@/components/ui/button"
import { computed } from "vue"

const props = defineProps<{ updatedAt: Date | null; stale: boolean }>()
const emit = defineEmits<{ refresh: [] }>()

const time = computed(() => {
  const d = props.updatedAt
  if (!d) return ""
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
})
</script>

<template>
  <span class="inline-flex items-center gap-2 text-xs text-muted-foreground" :aria-busy="stale">
    <template v-if="updatedAt">Updated {{ time }} · </template>
    <Button type="button" variant="ghost" size="xs" @click="emit('refresh')">↻ Refresh</Button>
    <Button
      as="a"
      variant="ghost"
      size="icon-xs"
      href="https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages"
      target="_blank"
      rel="noreferrer"
      title="docs/wiki/Dashboard.md#pages"
      aria-label="Help for the pages: docs/wiki/Dashboard.md#pages"
      >?</Button
    >
  </span>
</template>
