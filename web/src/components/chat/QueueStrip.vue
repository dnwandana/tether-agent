<script setup lang="ts">
import type { QueueItem } from "../../api/types.gen"

withDefaults(defineProps<{ items: QueueItem[]; max?: number }>(), { max: 20 })
const emit = defineEmits<{ now: [] }>()
</script>

<template>
  <div v-if="items.length > 0" class="queue-strip">
    <div class="qitem qhead">
      <span class="t-12 w-600">Waiting · {{ items.length }} / {{ max }}</span>
      <span class="grow t-12 c-3 truncate">They run after the current task, one at a time.</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="emit('now')">
        ⚡ Do this now
      </button>
    </div>
    <div v-for="(it, i) in items" :key="i" class="qitem">
      <span class="n">{{ i + 1 }}</span>
      <span class="grow truncate">{{ it.preview }}</span>
    </div>
  </div>
</template>
