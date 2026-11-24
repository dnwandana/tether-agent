<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue"
import { useOverlay } from "../lib/overlay"

defineProps<{ items: { label: string; act: string; danger?: boolean }[] }>()
const emit = defineEmits<{ select: [act: string] }>()

const open = ref(false)
const btn = ref<HTMLButtonElement | null>(null)
const menu = ref<HTMLElement | null>(null)
const pos = ref({ top: 0, right: 0 })

useOverlay(open, menu, { onClose: () => (open.value = false) })

function toggle(): void {
  if (!open.value && btn.value) {
    const r = btn.value.getBoundingClientRect()
    pos.value = { top: r.bottom + 4, right: window.innerWidth - r.right }
  }
  open.value = !open.value
}

function outside(e: Event): void {
  const t = e.target
  if (!(t instanceof Node)) return
  if (menu.value?.contains(t) || btn.value?.contains(t)) return
  open.value = false
}

// The menu has a fixed position from the open time, so a scroll or a resize closes it.
function away(e: Event): void {
  if (e.target instanceof Node && menu.value?.contains(e.target)) return
  open.value = false
}

function detach(): void {
  document.removeEventListener("pointerdown", outside)
  window.removeEventListener("scroll", away, true)
  window.removeEventListener("resize", away)
}

watch(open, (on) => {
  detach()
  if (!on) return
  document.addEventListener("pointerdown", outside)
  window.addEventListener("scroll", away, true)
  window.addEventListener("resize", away)
  void nextTick(() => menu.value?.querySelector<HTMLElement>('[role="menuitem"]')?.focus())
})

// Moves the focus between the items with ArrowUp and ArrowDown. Home and End go to the ends.
// Tab leaves the menu, so it also closes the menu.
function onKey(e: KeyboardEvent): void {
  if (e.key === "Tab") {
    open.value = false
    return
  }
  const els = Array.from(menu.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])
  if (els.length === 0) return
  const at = els.indexOf(document.activeElement as HTMLElement)
  let to: number
  if (e.key === "ArrowDown") to = (at + 1) % els.length
  else if (e.key === "ArrowUp") to = (at <= 0 ? els.length : at) - 1
  else if (e.key === "Home") to = 0
  else if (e.key === "End") to = els.length - 1
  else return
  e.preventDefault()
  els[to]?.focus()
}
onBeforeUnmount(detach)

function pick(act: string): void {
  open.value = false
  emit("select", act)
}
</script>

<template>
  <button
    ref="btn"
    class="btn btn-ghost"
    type="button"
    aria-label="More actions"
    aria-haspopup="menu"
    :aria-expanded="open ? 'true' : 'false'"
    @click="toggle"
  >
    ⋯
  </button>
  <Teleport to="body">
    <div
      v-if="open"
      ref="menu"
      class="menu row-menu"
      role="menu"
      @keydown="onKey"
      :style="{ '--top': pos.top + 'px', '--right': pos.right + 'px' }"
    >
      <button
        v-for="it in items"
        :key="it.act"
        class="menu-item"
        :class="{ danger: it.danger }"
        type="button"
        role="menuitem"
        @click="pick(it.act)"
      >
        {{ it.label }}
      </button>
    </div>
  </Teleport>
</template>

<style scoped>
.row-menu {
  position: fixed;
  top: var(--top);
  right: var(--right);
  z-index: 60;
  min-width: 180px;
}
@media (max-width: 599px) {
  .row-menu {
    top: auto;
    left: 0;
    right: 0;
    bottom: 0;
    border-radius: 12px 12px 0 0;
    padding-bottom: env(safe-area-inset-bottom);
  }
  .row-menu .menu-item {
    min-height: 44px;
  }
}
</style>
