<script setup lang="ts">
import { markRaw, onMounted, watch } from "vue"
import { toast } from "vue-sonner"
import { Toaster } from "@/components/ui/sonner"
import ToastView from "./ToastView.vue"
import { closeToast, holdAllToasts, releaseAllToasts, resolvedTheme, toasts } from "../stores/ui"

const view = markRaw(ToastView)

type StoreToast = (typeof toasts)[number]

function show(t: StoreToast): void {
  const id = t.id
  toast.custom(view, {
    id,
    duration: Infinity,
    componentProps: { toast: t },
    // A swipe closes a toast in vue-sonner. The store must follow.
    onDismiss: () => closeToast(id),
  })
}

// The store owns the toasts and their timers. This watch only draws and removes them.
// The Toaster is not ready before the mount, so a toast from before the mount waits for onMounted.
let mounted = false
watch(
  () => toasts.map((t) => t.id),
  (ids, old) => {
    if (!mounted) return
    const before = new Set(old ?? [])
    for (const t of toasts) if (!before.has(t.id)) show(t)
    for (const id of before) if (!ids.includes(id)) toast.dismiss(id)
  },
  { immediate: true },
)

onMounted(() => {
  mounted = true
  for (const t of toasts) show(t)
})
</script>

<template>
  <div
    @mouseenter="holdAllToasts()"
    @mouseleave="releaseAllToasts()"
    @focusin="holdAllToasts()"
    @focusout="releaseAllToasts()"
  >
    <!-- A modal sets pointer-events: none on body. The style keeps the toasts clickable.
         The mobile offset keeps the toasts above the tab bar. -->
    <Toaster
      :theme="resolvedTheme()"
      position="bottom-right"
      :visible-toasts="3"
      :mobile-offset="{ bottom: 'calc(150px + env(safe-area-inset-bottom))' }"
      :style="{ pointerEvents: 'auto' }"
    />
  </div>
</template>
