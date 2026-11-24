<script setup lang="ts">
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { useCommand } from "../lib/command"
import { renderMarkdown } from "../lib/markdown"
import { closeToast } from "../stores/ui"
import type { Toast, ToastAction } from "../stores/ui"

const props = defineProps<{ toast: Toast }>()

const { run } = useCommand()

function runAction(a: ToastAction): void {
  closeToast(props.toast.id)
  void run(a.data)
}
</script>

<template>
  <div
    :role="toast.kind === 'error' ? 'alert' : 'status'"
    :data-kind="toast.kind"
    :class="
      cn(
        'bg-popover text-popover-foreground flex w-full items-start gap-2 rounded-lg border p-3 text-sm shadow-lg',
        toast.kind === 'error' && 'border-destructive',
      )
    "
  >
    <span v-if="toast.kind === 'ok'" class="text-success" aria-hidden="true">✓</span>
    <span v-else-if="toast.kind === 'warn'" class="text-warning" aria-hidden="true">⚠</span>
    <span v-else-if="toast.kind === 'error'" class="text-destructive" aria-hidden="true">⚠</span>
    <!-- renderMarkdown escapes raw HTML, so v-html is safe here. -->
    <div
      v-if="toast.markdown"
      class="typeset typeset-chat min-w-0 grow"
      v-html="renderMarkdown(toast.text)"
    />
    <div v-else class="min-w-0 grow break-words">{{ toast.text }}</div>
    <div class="flex shrink-0 items-center gap-1">
      <Button
        v-for="a in toast.actions.slice(0, 2)"
        :key="a.data"
        variant="ghost"
        size="sm"
        @click="runAction(a)"
      >
        {{ a.label }}
      </Button>
      <Button variant="ghost" size="sm" aria-label="Close" @click="closeToast(toast.id)">✕</Button>
    </div>
  </div>
</template>
