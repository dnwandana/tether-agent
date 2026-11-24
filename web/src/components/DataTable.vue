<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { cn } from "@/lib/utils"

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
    first: i === 0,
    num: c.align === "right",
    act: c.key === "actions",
    label2: i === 0 || c.key === "actions" ? "" : c.label,
  })),
)

// Returns the text of a cell with no slot. Only a string or a number is a readable value.
function plain(v: unknown): string {
  return typeof v === "string" || typeof v === "number" ? String(v) : ""
}
</script>

<template>
  <Table :data-layout="cards ? 'cards' : 'table'" :class="cn(cards && 'block')">
    <TableHeader :class="cn(cards && 'sr-only')">
      <TableRow>
        <TableHead
          v-for="c in cols"
          :key="c.key"
          :class="cn(c.num && 'text-right tabular-nums')"
          :style="c.width ? { width: c.width } : undefined"
        >
          {{ c.label }}
        </TableHead>
      </TableRow>
    </TableHeader>
    <TableBody :class="cn(cards && 'flex flex-col gap-2')">
      <TableRow v-if="rows.length === 0">
        <TableCell :colspan="columns.length" class="text-muted-foreground">
          Nothing to show.
        </TableCell>
      </TableRow>
      <TableRow
        v-for="row in rows"
        :key="String(row[rowKey])"
        :class="cn(cards && 'flex flex-col gap-1 rounded-lg border! p-3')"
        @click="emit('row-click', row)"
      >
        <TableCell
          v-for="c in cols"
          :key="c.key"
          :data-l="c.label2"
          :data-first="c.first ? '' : undefined"
          :data-act-col="c.act ? '' : undefined"
          :class="
            cn(
              c.num && 'text-right tabular-nums',
              cards &&
                'flex min-w-0 items-baseline gap-2 border-0 p-0 whitespace-normal wrap-anywhere before:shrink-0 before:text-muted-foreground before:content-[attr(data-l)]',
              cards && c.first && 'font-medium',
              cards && c.act && 'justify-end',
            )
          "
        >
          <slot
            v-if="$slots['cell-' + c.key]"
            :name="`cell-${c.key}`"
            :row="row"
            :value="row[c.key]"
          />
          <template v-else>{{ plain(row[c.key]) }}</template>
        </TableCell>
      </TableRow>
    </TableBody>
  </Table>
</template>
