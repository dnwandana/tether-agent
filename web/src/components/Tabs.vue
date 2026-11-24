<script setup lang="ts">
import { nextTick, useId, useSlots } from "vue"

const props = defineProps<{ tabs: { id: string; label: string; count?: number }[] }>()
const model = defineModel<string>({ required: true })
const slots = useSlots()
// A prefix from useId keeps the ids unique when a page has two tab lists.
const uid = useId()

// Selects the tab at index i (with wrap), and moves the focus to it.
function go(i: number): void {
  const n = props.tabs.length
  if (n === 0) return
  const t = props.tabs[((i % n) + n) % n]
  model.value = t.id
  void nextTick(() => document.getElementById(`${uid}-tab-${t.id}`)?.focus())
}

function onKey(e: KeyboardEvent, i: number): void {
  let to: number
  if (e.key === "ArrowRight") to = i + 1
  else if (e.key === "ArrowLeft") to = i - 1
  else if (e.key === "Home") to = 0
  else if (e.key === "End") to = props.tabs.length - 1
  else return
  e.preventDefault()
  go(to)
}
</script>

<template>
  <div class="tabs" role="tablist">
    <button
      v-for="(t, i) in tabs"
      :id="uid + '-tab-' + t.id"
      :key="t.id"
      type="button"
      role="tab"
      :aria-selected="t.id === model"
      :aria-controls="slots.default ? uid + '-panel' : undefined"
      :tabindex="t.id === model ? 0 : -1"
      :class="{ 'is-on': t.id === model }"
      @click="model = t.id"
      @keydown="onKey($event, i)"
    >
      {{ t.label }}
      <span v-if="typeof t.count === 'number'" class="badge">{{ t.count }}</span>
    </button>
  </div>
  <div
    v-if="slots.default"
    :id="uid + '-panel'"
    role="tabpanel"
    :aria-labelledby="uid + '-tab-' + model"
    tabindex="0"
  >
    <slot />
  </div>
</template>

<style scoped>
@media (max-width: 599px) {
  .tabs {
    flex-wrap: wrap;
    overflow-x: visible;
  }
}
</style>
