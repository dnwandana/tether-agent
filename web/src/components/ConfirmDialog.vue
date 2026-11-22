<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue"
import { pendingConfirm } from "../stores/ui"

const cancelBtn = ref<HTMLButtonElement | null>(null)
const okBtn = ref<HTMLButtonElement | null>(null)

// Answers the open confirm.
function answer(ok: boolean): void {
  pendingConfirm.resolve?.(ok)
}

function onKey(e: KeyboardEvent): void {
  if (e.key === "Escape" && pendingConfirm.opts) {
    e.preventDefault()
    answer(false)
  }
}

onMounted(() => document.addEventListener("keydown", onKey))
onBeforeUnmount(() => document.removeEventListener("keydown", onKey))

// A danger confirm starts on Cancel. Other confirms start on the confirm button.
watch(
  () => pendingConfirm.opts,
  (opts) => {
    if (!opts) return
    ;(opts.danger ? cancelBtn.value : okBtn.value)?.focus()
  },
  { flush: "post" },
)
</script>

<template>
  <div v-if="pendingConfirm.opts" class="ov scrim is-open" @click.self="answer(false)">
    <div class="dialog" role="dialog" aria-modal="true" :aria-label="pendingConfirm.opts.title">
      <div class="grabber" aria-hidden="true" />
      <div class="dialog-b">
        <h3>{{ pendingConfirm.opts.title }}</h3>
        <p v-if="pendingConfirm.opts.body" class="confirm-body">{{ pendingConfirm.opts.body }}</p>
      </div>
      <div class="dialog-f">
        <button ref="cancelBtn" type="button" class="btn btn-secondary" @click="answer(false)">
          Cancel
        </button>
        <button
          ref="okBtn"
          type="button"
          class="btn"
          :class="pendingConfirm.opts.danger ? 'btn-danger' : 'btn-primary'"
          @click="answer(true)"
        >
          {{ pendingConfirm.opts.confirmLabel }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.confirm-body {
  white-space: pre-line;
  overflow-wrap: anywhere;
}
</style>
