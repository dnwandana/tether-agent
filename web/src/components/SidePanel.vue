<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, ref, toRef, watch } from "vue"
import { useOverlay } from "../lib/overlay"

const props = defineProps<{ open: boolean; title: string; dirty?: boolean }>()
const emit = defineEmits<{ close: [] }>()

// App.vue provides the slot, so a page that mounts alone renders the panel in place.
const hasSlot = inject("asideSlot", false)

const panel = ref<HTMLElement | null>(null)
const dy = ref(0)
let startY: number | null = null

function isPhone(): boolean {
  const mm = (globalThis as { matchMedia?: (q: string) => MediaQueryList }).matchMedia
  if (typeof mm !== "function") return false
  return mm.call(globalThis, "(max-width: 599px)").matches
}

function isWide(): boolean {
  const mm = (globalThis as { matchMedia?: (q: string) => MediaQueryList }).matchMedia
  if (typeof mm !== "function") return false
  return mm.call(globalThis, "(min-width: 1024px)").matches
}

// True while the panel asks "Discard the changes?".
const asking = ref(false)

// Closes the panel, or asks first when the panel has unsaved changes.
function requestClose(): void {
  if (props.dirty !== true) {
    emit("close")
    return
  }
  asking.value = true
}

function discard(): void {
  asking.value = false
  emit("close")
}

// useOverlay reads modal at each open, so the getter follows a resize between two opens.
useOverlay(toRef(props, "open"), panel, {
  onClose: requestClose,
  get modal() {
    return !isWide()
  },
})

watch(
  () => props.open,
  (on) => {
    if (!on) {
      asking.value = false
      return
    }
    // Chat closes its TurnDrawer, so that only one side column shows.
    document.dispatchEvent(new CustomEvent("tether:panel-open"))
  },
)

function yOf(e: TouchEvent | MouseEvent): number {
  return "touches" in e ? (e.touches[0]?.clientY ?? 0) : e.clientY
}

function start(e: TouchEvent | MouseEvent) {
  startY = isPhone() ? yOf(e) : null
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
  window.addEventListener("mousemove", move)
  window.addEventListener("mouseup", end)
})
onBeforeUnmount(() => {
  window.removeEventListener("mousemove", move)
  window.removeEventListener("mouseup", end)
})
</script>

<template>
  <Teleport defer to=".aside-slot" :disabled="!hasSlot">
    <div class="aside-scrim" :class="{ 'is-open': open }" @click="requestClose" />
    <aside
      ref="panel"
      class="aside"
      :class="{ 'is-open': open }"
      role="dialog"
      :aria-label="title"
      :aria-modal="isWide() ? undefined : 'true'"
      :inert="open ? undefined : true"
      :style="dy > 0 ? { transform: `translateY(${dy}px)` } : undefined"
    >
      <div
        class="grabber"
        aria-hidden="true"
        @touchstart.passive="start"
        @touchmove.passive="move"
        @touchend.passive="end"
        @mousedown="start"
      />
      <div
        class="aside-h"
        @touchstart.passive="start"
        @touchmove.passive="move"
        @touchend.passive="end"
        @mousedown="start"
      >
        <span class="truncate">{{ title }}</span
        ><span class="grow" />
        <button
          class="icon-btn"
          type="button"
          aria-label="Close"
          title="Close (Esc)"
          @click="requestClose"
        >
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.75"
            stroke-linecap="round"
            aria-hidden="true"
          >
            <path d="M6 6l12 12M18 6L6 18" />
          </svg>
        </button>
      </div>
      <div class="aside-b">
        <div v-if="asking" class="discard" role="alert">
          <span>Discard the changes?</span>
          <button type="button" class="btn btn-secondary btn-sm" @click="asking = false">
            Keep editing
          </button>
          <button type="button" class="btn btn-danger-soft btn-sm" @click="discard">Discard</button>
        </div>
        <slot />
      </div>
    </aside>
  </Teleport>
</template>
