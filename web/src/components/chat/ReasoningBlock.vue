<script setup lang="ts">
defineProps<{ text: string; tokens: number }>()
</script>

<template>
  <details v-if="text" class="reasoning">
    <summary>▸ Reasoning · {{ tokens.toLocaleString("en-US") }} tokens</summary>
    <div class="rs-b">{{ text }}</div>
  </details>
</template>

<style scoped>
.rs-b {
  white-space: pre-wrap;
}
</style>
