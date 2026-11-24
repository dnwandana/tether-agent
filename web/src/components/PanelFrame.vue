<script setup lang="ts">
// One frame for the side panel and the turn drawer. It keeps one mounted container in each mode,
// so a mode change keeps the state of the slot. Only classes, attributes, the focus trap, and
// the Teleport target change.
import { XIcon } from "@lucide/vue"
import { FocusScope } from "reka-ui"
import {
  type Ref,
  computed,
  inject,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  toRef,
  useTemplateRef,
  watch,
} from "vue"
import { Button } from "@/components/ui/button"
import { guardOutside, hasOpenLayer, useReturnFocus } from "@/lib/layer"
import { cn } from "@/lib/utils"

const props = defineProps<{ open: boolean; title: string; dockQuery: string }>()
const emit = defineEmits<{ "request-close": [] }>()

const hasSlot = inject("asideSlot", false)

// Keeps the answer of one media query. Without matchMedia, the answer is false.
function mediaFlag(q: string): Ref<boolean> {
  const query = globalThis.matchMedia?.(q) ?? null
  const flag = ref(query?.matches ?? false)
  const onChange = (e: { matches: boolean }) => {
    flag.value = e.matches
  }
  onMounted(() => {
    flag.value = query?.matches ?? false
    query?.addEventListener?.("change", onChange)
  })
  onBeforeUnmount(() => query?.removeEventListener?.("change", onChange))
  return flag
}

const phone = mediaFlag("(max-width: 599px)")
const dock = mediaFlag(props.dockQuery)
const mode = computed<"sheet" | "modal" | "docked">(() =>
  phone.value ? "sheet" : dock.value ? "docked" : "modal",
)

const panel = useTemplateRef<{ $el: HTMLElement }>("panel")
const { onCloseAutoFocus } = useReturnFocus(toRef(props, "open"))

function requestClose() {
  emit("request-close")
}

function onScrim(e: MouseEvent) {
  guardOutside(e)
  if (!e.defaultPrevented) requestClose()
}

// The focus moves into the panel at open, except when the panel is docked.
watch(
  () => props.open,
  async (on) => {
    if (!on) {
      onCloseAutoFocus(new Event("close"))
      return
    }
    if (mode.value === "docked") return
    await nextTick()
    const el = panel.value?.$el
    if (!el) return
    const first = el.querySelector<HTMLElement>(
      'input, select, textarea, button, a[href], [tabindex]:not([tabindex="-1"])',
    )
    ;(first ?? el).focus()
  },
  { flush: "post" },
)

// A mode change can move the panel in or out of the aside slot. The move takes the focus away,
// so the panel keeps the focused element and focuses it again after the move. A panel that
// becomes modal with no focus inside takes the focus.
let kept: HTMLElement | null = null
watch(
  mode,
  () => {
    const el = panel.value?.$el
    const active = document.activeElement
    kept = el && active instanceof HTMLElement && el.contains(active) ? active : null
  },
  { flush: "pre" },
)
watch(
  mode,
  async (m) => {
    const keep = kept
    kept = null
    if (!props.open) return
    await nextTick()
    const el = panel.value?.$el
    if (!el) return
    if (keep?.isConnected && el.contains(keep)) {
      keep.focus()
      return
    }
    if (m === "docked" || el.contains(document.activeElement)) return
    const first = el.querySelector<HTMLElement>(
      'input, select, textarea, button, a[href], [tabindex]:not([tabindex="-1"])',
    )
    ;(first ?? el).focus()
  },
  { flush: "post" },
)

// FocusScope focuses its first element on mount. A closed or docked panel must not take the
// focus, and jsdom does not skip an inert element.
function onMountFocus(e: Event) {
  if (!props.open || mode.value === "docked") e.preventDefault()
}

function onKey(e: KeyboardEvent) {
  if (e.key !== "Escape" || !props.open || e.defaultPrevented || hasOpenLayer()) return
  requestClose()
}

// A drag down of more than 70 px closes the sheet.
const dy = ref(0)
let startY: number | null = null

function yOf(e: TouchEvent | MouseEvent): number {
  return "touches" in e ? (e.touches[0]?.clientY ?? 0) : e.clientY
}

function start(e: TouchEvent | MouseEvent) {
  startY = mode.value === "sheet" ? yOf(e) : null
}

function move(e: TouchEvent | MouseEvent) {
  if (startY === null) return
  dy.value = Math.max(0, yOf(e) - startY)
}

function end() {
  if (startY === null) return
  const moved = dy.value
  startY = null
  dy.value = 0
  if (moved > 70) requestClose()
}

onMounted(() => {
  window.addEventListener("keydown", onKey)
  window.addEventListener("mousemove", move)
  window.addEventListener("mouseup", end)
})
onBeforeUnmount(() => {
  window.removeEventListener("keydown", onKey)
  window.removeEventListener("mousemove", move)
  window.removeEventListener("mouseup", end)
})
</script>

<template>
  <Teleport defer to=".aside-slot" :disabled="!(mode === 'docked' && hasSlot)">
    <div
      v-if="mode !== 'docked'"
      v-show="open"
      data-slot="panel-scrim"
      aria-hidden="true"
      class="fixed inset-0 z-50 bg-foreground/40"
      @click="onScrim"
    />
    <FocusScope
      ref="panel"
      as="aside"
      loop
      :trapped="open && mode !== 'docked'"
      @mount-auto-focus="onMountFocus"
      role="dialog"
      tabindex="-1"
      :aria-label="title"
      :aria-modal="mode === 'docked' ? undefined : 'true'"
      :data-mode="mode"
      :inert="open ? undefined : true"
      :style="dy > 0 ? { transform: `translateY(${dy}px)` } : undefined"
      :class="
        cn(
          'flex flex-col bg-background outline-none transition-transform duration-200',
          mode === 'sheet' &&
            'fixed inset-x-0 bottom-0 z-50 max-h-[85dvh] rounded-t-xl border-t pb-[env(safe-area-inset-bottom)]',
          mode === 'modal' && 'fixed inset-y-0 right-0 z-50 w-full max-w-md border-l shadow-lg',
          mode === 'docked' && 'static w-[380px] shrink-0 border-l',
          !open && mode === 'sheet' && 'translate-y-full',
          !open && mode === 'modal' && 'translate-x-full',
          !open && mode === 'docked' && 'hidden',
        )
      "
    >
      <div
        v-if="mode === 'sheet'"
        data-slot="panel-grabber"
        aria-hidden="true"
        class="mx-auto my-2 h-1 w-10 shrink-0 rounded-full bg-muted"
        @touchstart.passive="start"
        @touchmove.passive="move"
        @touchend.passive="end"
        @mousedown="start"
      />
      <div
        class="flex shrink-0 items-center gap-2 border-b px-4 py-2"
        @touchstart.passive="start"
        @touchmove.passive="move"
        @touchend.passive="end"
        @mousedown="start"
      >
        <span class="min-w-0 flex-1 truncate font-medium">{{ title }}</span>
        <Button
          variant="ghost"
          size="icon"
          type="button"
          aria-label="Close"
          title="Close (Esc)"
          @click="requestClose"
        >
          <XIcon />
        </Button>
      </div>
      <div data-slot="panel-body" class="min-h-0 flex-1 overflow-y-auto p-4">
        <slot />
      </div>
    </FocusScope>
  </Teleport>
</template>
