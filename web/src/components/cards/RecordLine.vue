<script setup lang="ts">
import { computed, ref } from "vue"
import type { AnsweredCard } from "../../api/types.gen"

const props = defineProps<{ record: AnsweredCard }>()
const open = ref(false)

// The mark at the start of the header gives the color: allowed, denied, or no answer.
const tone = computed(() => {
  const mark = props.record.header
    .replace(/\uFE0F/g, "")
    .trim()
    .charAt(0)
  if (mark === "✅") return "c-ok"
  if (mark === "❌") return "c-bad"
  return "c-warn"
})
const time = computed(() => {
  const d = new Date(props.record.at)
  if (Number.isNaN(d.getTime())) return ""
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
})
</script>

<template>
  <div class="rec">
    <div class="collapsed">
      <span class="rec-h" :class="tone">{{ record.header }}</span>
      <span class="sum">{{ record.preview }}</span>
      <span class="rec-t t-12 c-3">{{ time }}</span>
      <button
        type="button"
        class="chev"
        :aria-expanded="open"
        aria-label="Show the record"
        @click="open = !open"
      >
        ▸
      </button>
    </div>
    <div v-if="open" class="rec-detail">
      <div class="cmd">{{ record.preview }}</div>
      <RouterLink :to="{ name: 'security', query: { tab: 'log', scope: 'dashboard' } }">
        Open the approval log
      </RouterLink>
    </div>
  </div>
</template>

<style scoped>
.rec-h {
  flex: none;
  white-space: nowrap;
}
.rec-t {
  flex: none;
}
.rec-detail {
  display: flex;
  flex-direction: column;
  gap: var(--sp-2, 8px);
  max-width: 640px;
  padding: 8px 14px;
}
.rec-detail .cmd {
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
</style>
