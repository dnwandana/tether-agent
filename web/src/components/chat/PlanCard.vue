<script setup lang="ts">
import { computed } from "vue"
import type { PlanItem } from "../../api/types.gen"

const props = defineProps<{ items: PlanItem[]; before?: PlanItem[] }>()

const MARK: Record<string, string> = {
  completed: "[x]",
  in_progress: "[>]",
  cancelled: "[~]",
  pending: "[ ]",
}

const total = computed(() => props.items.length)
const doneCount = computed(() => props.items.filter((i) => i.state === "completed").length)
const allClosed = computed(
  () =>
    total.value > 0 && props.items.every((i) => i.state === "completed" || i.state === "cancelled"),
)
const pct = computed(() =>
  total.value === 0 ? 0 : Math.round((doneCount.value / total.value) * 100),
)
const header = computed(() =>
  allClosed.value ? "📋 Plan done" : `📋 Plan ${doneCount.value}/${total.value}`,
)

// Returns true when the item at the index has a state that differs from the same item before.
function changed(i: number): boolean {
  const old = props.before?.[i]
  return old !== undefined && old.state !== props.items[i].state
}
</script>

<template>
  <div class="plan">
    <div class="plan-h">
      <span>{{ header }}</span>
      <div class="meter meter-ok"><span :style="{ width: pct + '%' }" /></div>
    </div>
    <div class="plan-list">
      <div
        v-for="(item, i) in items"
        :key="i"
        class="pi"
        :class="{ child: item.parent >= 0, changed: changed(i), done: item.state === 'completed' }"
      >
        <span class="mk">{{ MARK[item.state] ?? "[ ]" }}</span>
        <span class="tx">{{ item.text }}</span>
        <span v-if="changed(i)" class="chg">changed</span>
      </div>
    </div>
  </div>
</template>
