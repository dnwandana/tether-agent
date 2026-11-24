<script setup lang="ts">
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
  <span class="live-h t-12 c-3" :aria-busy="stale">
    <template v-if="updatedAt">Updated {{ time }} · </template>
    <button type="button" class="link" data-act="refresh" @click="emit('refresh')">
      ↻ Refresh
    </button>
    <a
      class="help-link"
      href="https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages"
      target="_blank"
      rel="noreferrer"
      title="docs/wiki/Dashboard.md#pages"
      aria-label="Help for the pages: docs/wiki/Dashboard.md#pages"
      >?</a
    >
  </span>
</template>
