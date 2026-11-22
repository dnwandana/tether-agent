<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"

interface Column {
  key: string
  label: string
  width?: string
  align?: "left" | "right"
}

const props = withDefaults(
  defineProps<{
    columns: Column[]
    rows: Record<string, unknown>[]
    rowKey: string
    cardsBelow?: number
  }>(),
  { cardsBelow: 600 },
)
const emit = defineEmits<{ "row-click": [row: Record<string, unknown>] }>()

let mql: MediaQueryList | null = null

// Returns the media query list, or null when matchMedia does not exist.
function query(cardsBelow: number): MediaQueryList | null {
  const mm = (globalThis as { matchMedia?: (q: string) => MediaQueryList }).matchMedia
  if (typeof mm !== "function") return null
  return mm.call(globalThis, `(max-width: ${cardsBelow - 1}px)`)
}

// Returns true when the viewport is narrower than cardsBelow px.
function narrow(cardsBelow: number): boolean {
  return query(cardsBelow)?.matches ?? false
}

const cards = ref(narrow(props.cardsBelow))

function onChange(e: MediaQueryListEvent) {
  cards.value = e.matches
}

onMounted(() => {
  mql = query(props.cardsBelow)
  mql?.addEventListener?.("change", onChange)
})
onBeforeUnmount(() => {
  mql?.removeEventListener?.("change", onChange)
  mql = null
})

const cols = computed(() =>
  props.columns.map((c, i) => ({
    ...c,
    cls: {
      first: i === 0,
      num: c.align === "right",
      act: c.key === "actions",
    },
    label2: i === 0 || c.key === "actions" ? "" : c.label,
  })),
)
</script>

<template>
  <div class="table-wrap">
    <table class="table" :class="{ 'rt-cards': cards }">
      <thead>
        <tr>
          <th
            v-for="c in cols"
            :key="c.key"
            :class="{ num: c.align === 'right' }"
            :style="c.width ? { width: c.width } : undefined"
          >
            {{ c.label }}
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-if="rows.length === 0">
          <td :colspan="columns.length" class="c-3">Nothing to show.</td>
        </tr>
        <tr v-for="row in rows" :key="String(row[rowKey])" @click="emit('row-click', row)">
          <td v-for="c in cols" :key="c.key" :class="c.cls" :data-l="c.label2">
            <slot :name="`cell-${c.key}`" :row="row" :value="row[c.key]">{{ row[c.key] }}</slot>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
