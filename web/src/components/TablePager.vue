<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useId } from "vue"
import { cn } from "cn"
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import {
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationItem,
  PaginationNext,
  PaginationPrevious,
} from "@/components/ui/pagination"
import type { PageSize } from "@/lib/pager"
import { PAGE_SIZES, lastPage, pageRange, pagerVisible } from "@/lib/pager"

const props = defineProps<{
  page: number
  size: PageSize
  total: number
  label: string
  loading?: boolean
}>()
const emit = defineEmits<{ "update:page": [page: number]; "update:size": [size: PageSize] }>()

const selectId = useId()
const PHONE_WIDTH = 600

let mql: MediaQueryList | null = null

// Returns the media query list, or null when matchMedia does not exist.
function query(): MediaQueryList | null {
  const mm = (globalThis as { matchMedia?: (q: string) => MediaQueryList }).matchMedia
  if (typeof mm !== "function") return null
  return mm.call(globalThis, `(max-width: ${PHONE_WIDTH - 1}px)`)
}

const narrow = ref(query()?.matches ?? false)

function onChange(e: MediaQueryListEvent) {
  narrow.value = e.matches
}

onMounted(() => {
  mql = query()
  mql?.addEventListener?.("change", onChange)
})
onBeforeUnmount(() => {
  mql?.removeEventListener?.("change", onChange)
  mql = null
})

const range = computed(() => pageRange(props.page, props.size, props.total))
const last = computed(() => lastPage(props.total, props.size))

// Emits the size only when it is one of the allowed choices.
function onSize(v: unknown) {
  const n = Number(v)
  if ((PAGE_SIZES as readonly number[]).includes(n)) emit("update:size", n as PageSize)
}
</script>

<template>
  <div v-if="pagerVisible(total)" class="flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
    <div class="flex items-center gap-2">
      <label :for="selectId" :class="cn('text-muted-foreground', narrow && 'sr-only')">
        Rows per page
      </label>
      <NativeSelect
        :id="selectId"
        :model-value="size"
        :disabled="loading"
        class="max-tab:h-11"
        @change="onSize(($event.target as HTMLSelectElement).value)"
      >
        <NativeSelectOption v-for="n in PAGE_SIZES" :key="n" :value="n">{{ n }}</NativeSelectOption>
      </NativeSelect>
    </div>
    <span class="text-muted-foreground tabular-nums">
      {{ range.start }}–{{ range.end }} of {{ total }}
    </span>
    <Pagination
      :page="page"
      :total="total"
      :items-per-page="size"
      :sibling-count="1"
      show-edges
      :disabled="loading"
      :aria-label="`${label} pages`"
      class="mx-0 ms-auto w-auto"
      @update:page="emit('update:page', $event)"
    >
      <PaginationContent v-slot="{ items }">
        <PaginationPrevious class="max-tab:h-11" />
        <span v-if="narrow" class="px-2 tabular-nums">{{ page }} / {{ last }}</span>
        <template v-else>
          <template v-for="(item, i) in items" :key="i">
            <PaginationItem
              v-if="item.type === 'page'"
              :value="item.value"
              :is-active="item.value === page"
            >
              {{ item.value }}
            </PaginationItem>
            <PaginationEllipsis v-else :index="i" />
          </template>
        </template>
        <PaginationNext class="max-tab:h-11" />
      </PaginationContent>
    </Pagination>
  </div>
</template>
