<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { guardOutside, useReturnFocus } from "../lib/layer"
import { renderMarkdown } from "../lib/markdown"
import { pendingConfirm } from "../stores/ui"

const cancelBtn = ref<{ $el: HTMLElement } | null>(null)
const okBtn = ref<{ $el: HTMLElement } | null>(null)
const open = computed(() => pendingConfirm.opts !== null)
const { onCloseAutoFocus } = useReturnFocus(open)

// Answers the open confirm.
function answer(ok: boolean): void {
  pendingConfirm.resolve?.(ok)
}

// The title shows inline, so the paragraph wrapper of the Markdown output goes away.
const inline = (s: string) => renderMarkdown(s).replace(/^<p>([\s\S]*)<\/p>\s*$/, "$1")

// The focus follows the focus option. The default is Cancel for a danger confirm and the confirm
// button for the others. Reka focuses Cancel one tick after the open event, so wait two ticks.
async function focusFirst(): Promise<void> {
  await nextTick()
  await nextTick()
  const opts = pendingConfirm.opts
  if (!opts) return
  const first = opts.focus ?? (opts.danger ? "cancel" : "ok")
  const target = (first === "cancel" ? cancelBtn : okBtn).value?.$el
  target?.focus()
}

function onOpenAutoFocus(e: Event): void {
  e.preventDefault()
  void focusFirst()
}

// A second confirm() while the dialog is open changes opts with no new open event.
watch(
  () => pendingConfirm.opts,
  (opts) => {
    if (opts) void focusFirst()
  },
)
</script>

<template>
  <AlertDialog :open="open">
    <AlertDialogContent
      v-if="pendingConfirm.opts"
      @open-auto-focus="onOpenAutoFocus"
      @close-auto-focus="onCloseAutoFocus"
      @escape-key-down="answer(false)"
      @pointer-down-outside="guardOutside"
      @interact-outside="guardOutside"
    >
      <AlertDialogHeader>
        <AlertDialogTitle
          v-if="pendingConfirm.opts.markdown"
          v-html="inline(pendingConfirm.opts.title)"
        />
        <AlertDialogTitle v-else>{{ pendingConfirm.opts.title }}</AlertDialogTitle>
        <AlertDialogDescription as="div" class="flex flex-col gap-2">
          <template v-if="pendingConfirm.opts.body">
            <div
              v-if="pendingConfirm.opts.markdown"
              class="typeset typeset-chat"
              v-html="renderMarkdown(pendingConfirm.opts.body)"
            />
            <p v-else class="whitespace-pre-line [overflow-wrap:anywhere]">
              {{ pendingConfirm.opts.body }}
            </p>
          </template>
          <ul
            v-if="pendingConfirm.opts.items?.length"
            class="flex max-h-[40vh] flex-col gap-1 overflow-y-auto"
          >
            <li
              v-for="(it, i) in pendingConfirm.opts.items"
              :key="i"
              class="rounded-sm bg-muted px-2 py-1 font-mono text-xs whitespace-pre-wrap [overflow-wrap:anywhere]"
            >
              {{ it }}
            </li>
          </ul>
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <AlertDialogCancel ref="cancelBtn" @click="answer(false)">Cancel</AlertDialogCancel>
        <AlertDialogAction
          ref="okBtn"
          :class="cn(pendingConfirm.opts.danger && buttonVariants({ variant: 'destructive' }))"
          @click="answer(true)"
        >
          {{ pendingConfirm.opts.confirmLabel }}
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
