<script setup lang="ts">
import { useElementSize } from "@vueuse/core"
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue"
import TablePager from "@/components/TablePager.vue"
import {
  Table,
  TableBody,
  TableCell,
  TableFooter,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { PageSize } from "@/lib/pager"
import { DEFAULT_SIZE, clampPage, readSize, writeSize } from "@/lib/pager"
import type { Column } from "@/lib/table"
import { columnType, tableMinRem } from "@/lib/table"
import { cn } from "@/lib/utils"

const props = withDefaults(
  defineProps<{
    columns: Column[]
    rows: Record<string, unknown>[]
    rowKey: string
    cardsBelow?: number
    pageKey?: string
    pageLabel?: string
    total?: number
    page?: number
    size?: PageSize
    loading?: boolean
  }>(),
  { cardsBelow: 600, pageLabel: "Rows" },
)
const emit = defineEmits<{
  "row-click": [row: Record<string, unknown>]
  "update:page": [page: number]
  "update:size": [size: PageSize]
}>()

const server = computed(() => props.total !== undefined)
const ownPage = ref(1)
const ownSize = ref<PageSize>(props.pageKey ? readSize(props.pageKey) : DEFAULT_SIZE)
const curPage = computed(() => props.page ?? ownPage.value)
const curSize = computed(() => props.size ?? ownSize.value)
const count = computed(() => props.total ?? props.rows.length)
const shown = computed(() =>
  server.value
    ? props.rows
    : props.rows.slice((curPage.value - 1) * curSize.value, curPage.value * curSize.value),
)
const wrap = ref<HTMLElement | null>(null)

// Sets the page and tells the parent. The clamp uses it with no scroll.
function movePage(p: number): void {
  ownPage.value = p
  emit("update:page", p)
}

// Scrolls the top of the table into view, only when the top is above the visible part of the
// nearest .scroll box (or above the viewport when there is no box).
function scrollTop(): void {
  const el = wrap.value
  if (!el) return
  const box = el.closest(".scroll")
  const top = box ? box.getBoundingClientRect().top : 0
  if (el.getBoundingClientRect().top < top) el.scrollIntoView({ block: "start" })
}

// Handles a page change of the pager.
function onPage(p: number): void {
  movePage(p)
  scrollTop()
}

// Handles a size change of the pager. It keeps the size only when the size prop is not set.
function onSize(n: PageSize): void {
  if (props.size === undefined) {
    ownSize.value = n
    if (props.pageKey) writeSize(props.pageKey, n)
  }
  emit("update:size", n)
  movePage(1)
}

// Moves to the last page when the browser rows shrink, for example after a Remove or a live
// reload. At mount it also moves a page from the URL that is beyond the end, but not before
// the rows are loaded. The parent clamps in server mode.
watch(
  () => props.rows.length,
  (n, old) => {
    if (server.value) return
    if (old === undefined && n === 0) return
    const p = clampPage(curPage.value, n, curSize.value)
    if (p !== curPage.value) movePage(p)
  },
  { immediate: true },
)

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

const { width } = useElementSize(wrap)

// Returns the size of 1 rem in px.
function remPx(): number {
  return parseFloat(getComputedStyle(document.documentElement).fontSize) || 16
}

// Optional columns show in card mode, before the first measure, and while the full table fits.
const withOptional = computed(
  () =>
    cards.value || width.value === 0 || width.value >= tableMinRem(props.columns, true) * remPx(),
)
const minRem = computed(() => tableMinRem(props.columns, withOptional.value))

const cols = computed(() =>
  props.columns
    .filter((c) => withOptional.value || !c.optional)
    .map((c, i) => {
      const type = columnType(c)
      const fixed = type === "num" || type === "date" || type === "badge"
      return {
        ...c,
        type,
        first: i === 0,
        num: type === "num",
        act: type === "act",
        colWidth: c.width ?? (fixed ? `${tableMinRem([c], true)}rem` : undefined),
        label2: i === 0 || type === "act" ? "" : c.label,
      }
    }),
)

// Returns the text of a cell with no slot. Only a string or a number is a readable value.
function plain(v: unknown): string {
  return typeof v === "string" || typeof v === "number" ? String(v) : ""
}
</script>

<template>
  <div ref="wrap" data-slot="data-table" class="@container flex flex-col gap-2">
    <Table
      :data-layout="cards ? 'cards' : 'table'"
      :class="cn(cards ? 'block' : 'table-fixed min-w-(--table-min)')"
      :style="cards ? undefined : { '--table-min': `${minRem}rem` }"
    >
      <colgroup v-if="!cards">
        <col
          v-for="c in cols"
          :key="c.key"
          :style="c.colWidth ? { width: c.colWidth } : undefined"
        />
      </colgroup>
      <TableHeader :class="cn(cards && 'sr-only')">
        <TableRow>
          <TableHead
            v-for="c in cols"
            :key="c.key"
            :class="cn(c.num && 'text-right tabular-nums', c.act && 'text-right')"
          >
            {{ c.label }}
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody :class="cn(cards && 'flex flex-col gap-2')">
        <TableRow v-if="shown.length === 0">
          <TableCell :colspan="cols.length" class="text-muted-foreground">
            Nothing to show.
          </TableCell>
        </TableRow>
        <TableRow
          v-for="row in shown"
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
            :title="
              !$slots['cell-' + c.key] && c.type === 'text'
                ? plain(row[c.key]) || undefined
                : undefined
            "
            :class="
              cn(
                c.num && 'text-right tabular-nums',
                !cards && c.type === 'text' && 'overflow-hidden text-ellipsis',
                !cards && c.type === 'wrap' && 'overflow-hidden whitespace-normal',
                !cards && c.type === 'block' && 'whitespace-normal',
                !cards && c.act && 'text-right',
                cards &&
                  'flex min-w-0 items-baseline gap-2 border-0 p-0 whitespace-normal wrap-anywhere before:shrink-0 before:text-muted-foreground before:content-[attr(data-l)]',
                cards && c.first && 'font-medium',
                cards && c.act && 'justify-end',
              )
            "
          >
            <div v-if="c.act" class="inline-flex items-center justify-end gap-2">
              <slot
                v-if="$slots['cell-' + c.key]"
                :name="`cell-${c.key}`"
                :row="row"
                :value="row[c.key]"
              />
              <template v-else>{{ plain(row[c.key]) }}</template>
            </div>
            <slot
              v-else-if="$slots['cell-' + c.key]"
              :name="`cell-${c.key}`"
              :row="row"
              :value="row[c.key]"
            />
            <template v-else>{{ plain(row[c.key]) }}</template>
          </TableCell>
        </TableRow>
      </TableBody>
      <TableFooter v-if="$slots.footer"><slot name="footer" /></TableFooter>
    </Table>
    <TablePager
      :page="curPage"
      :size="curSize"
      :total="count"
      :label="pageLabel"
      :loading="loading"
      @update:page="onPage"
      @update:size="onSize"
    />
  </div>
</template>
