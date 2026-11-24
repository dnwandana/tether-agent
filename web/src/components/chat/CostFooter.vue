<script setup lang="ts">
import { computed } from "vue"
import type { CostView } from "../../api/types.gen"
import { duration } from "../../lib/time"

const props = defineProps<{ cost: CostView }>()

// Returns a token count as "512", "61.4k", or "1.3M".
function tokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M"
  if (n >= 1000) return (n / 1000).toFixed(1) + "k"
  return String(n)
}

const model = computed(() =>
  props.cost.answered_by
    ? `${props.cost.model} · answered by ${props.cost.answered_by}`
    : props.cost.model,
)
const usd = computed(() => "$" + props.cost.usd.toFixed(4) + (props.cost.unpriced ? "+" : ""))
</script>

<template>
  <div class="cost-foot">
    <span>{{ model }}</span>
    <span>{{ cost.calls }} calls</span>
    <span>{{ tokens(cost.in) }} in ({{ tokens(cost.cached) }} cached)</span>
    <span>{{ tokens(cost.out) }} out</span>
    <span>{{ usd }}</span>
    <span>{{ duration(cost.seconds) }}</span>
  </div>
</template>
