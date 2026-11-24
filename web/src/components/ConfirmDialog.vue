<script setup lang="ts">
import { computed, ref, watch } from "vue"
import { useOverlay } from "../lib/overlay"
import { renderMarkdown } from "../lib/markdown"
import { pendingConfirm } from "../stores/ui"

const dialogEl = ref<HTMLElement | null>(null)
const cancelBtn = ref<HTMLButtonElement | null>(null)
const okBtn = ref<HTMLButtonElement | null>(null)
const open = computed(() => pendingConfirm.opts !== null)

// Answers the open confirm.
function answer(ok: boolean): void {
  pendingConfirm.resolve?.(ok)
}

// The title shows inline, so the paragraph wrapper of the Markdown output goes away.
const inline = (s: string) => renderMarkdown(s).replace(/^<p>([\s\S]*)<\/p>\s*$/, "$1")

// The focus follows the focus option. The default is Cancel for a danger confirm and the confirm
// button for the others. This watcher runs before useOverlay, so the helper keeps this focus.
watch(
  () => pendingConfirm.opts,
  (opts) => {
    if (!opts) return
    const first = opts.focus ?? (opts.danger ? "cancel" : "ok")
    ;(first === "cancel" ? cancelBtn.value : okBtn.value)?.focus()
  },
  { flush: "post" },
)

useOverlay(open, dialogEl, { onClose: () => answer(false), modal: true })
</script>

<template>
  <div v-if="pendingConfirm.opts" class="ov scrim is-open" @click.self="answer(false)">
    <div
      ref="dialogEl"
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-t"
      :aria-describedby="
        pendingConfirm.opts.body || pendingConfirm.opts.items?.length ? 'confirm-b' : undefined
      "
    >
      <div class="grabber" aria-hidden="true" />
      <div class="dialog-b">
        <h3
          v-if="pendingConfirm.opts.markdown"
          id="confirm-t"
          v-html="inline(pendingConfirm.opts.title)"
        />
        <h3 v-else id="confirm-t">{{ pendingConfirm.opts.title }}</h3>
        <div id="confirm-b">
          <template v-if="pendingConfirm.opts.body">
            <div
              v-if="pendingConfirm.opts.markdown"
              class="confirm-body md"
              v-html="renderMarkdown(pendingConfirm.opts.body)"
            />
            <p v-else class="confirm-body">{{ pendingConfirm.opts.body }}</p>
          </template>
          <ul v-if="pendingConfirm.opts.items?.length" class="confirm-items">
            <li v-for="(it, i) in pendingConfirm.opts.items" :key="i" class="mono">{{ it }}</li>
          </ul>
        </div>
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
.confirm-items {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
  max-height: 40vh;
  overflow-y: auto;
}
.confirm-items li {
  padding: 4px 8px;
  border-radius: var(--r-sm);
  background: var(--surface-inset);
  font-family: var(--font-mono);
  font-size: var(--fs-12);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.confirm-items li + li {
  margin-top: 4px;
}
</style>
