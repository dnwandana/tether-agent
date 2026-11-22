<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue"

const props = defineProps<{ open: boolean; title: string }>()
const emit = defineEmits<{ close: [] }>()

const dy = ref(0)
let startY: number | null = null

function isPhone(): boolean {
  const mm = (globalThis as { matchMedia?: (q: string) => MediaQueryList }).matchMedia
  if (typeof mm !== "function") return false
  return mm.call(globalThis, "(max-width: 599px)").matches
}

function onKey(e: KeyboardEvent) {
  if (e.key !== "Escape" || !props.open) return
  if (document.querySelector(".ov.is-open")) return
  emit("close")
}

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
  if (moved > 70) emit("close")
}

onMounted(() => {
  document.addEventListener("keydown", onKey)
  window.addEventListener("mousemove", move)
  window.addEventListener("mouseup", end)
})
onBeforeUnmount(() => {
  document.removeEventListener("keydown", onKey)
  window.removeEventListener("mousemove", move)
  window.removeEventListener("mouseup", end)
})
</script>

<template>
  <div class="aside-scrim" :class="{ 'is-open': open }" @click="emit('close')" />
  <aside
    class="aside"
    :class="{ 'is-open': open }"
    role="dialog"
    :aria-label="title"
    :aria-hidden="!open"
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
        @click="emit('close')"
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
    <div class="aside-b"><slot /></div>
  </aside>
</template>
