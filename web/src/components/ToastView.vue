<script setup lang="ts">
import { Button } from "@/components/ui/button"
import { CircleAlertIcon, CircleCheckIcon, InfoIcon, TriangleAlertIcon, XIcon } from "@lucide/vue"
import { computed } from "vue"
import { cn } from "@/lib/utils"
import { useCommand } from "../lib/command"
import { GLYPH_TONE_CLASS, buttonLabel, splitGlyph, toneOfKind } from "../lib/glyphs"
import { renderSystemText } from "../lib/markdown"
import ButtonLabel from "./ButtonLabel.vue"
import GlyphText from "./GlyphText.vue"
import { closeToast } from "../stores/ui"
import type { Toast, ToastAction } from "../stores/ui"

const props = defineProps<{ toast: Toast }>()

const { run } = useCommand()

// The icon of each toast kind. ResultNote uses the same icons.
const KIND_ICON = {
  ok: CircleCheckIcon,
  info: InfoIcon,
  warn: TriangleAlertIcon,
  error: CircleAlertIcon,
}
const tone = computed(() => toneOfKind(props.toast.kind))
// The plain branch shows page text and error text as typed. A lead glyph with the tone of
// the kind icon goes away, so the toast shows one icon for one state.
const plain = computed(() => {
  const s = splitGlyph(props.toast.text)
  return s.glyph !== null && s.glyph.tone === tone.value ? s.rest : props.toast.text
})

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
    <component
      :is="KIND_ICON[toast.kind]"
      :class="cn('mt-0.5 size-4 shrink-0', GLYPH_TONE_CLASS[tone])"
    />
    <!-- renderSystemText escapes raw HTML, so v-html is safe here. -->
    <div
      v-if="toast.markdown"
      class="typeset typeset-chat min-w-0 grow"
      v-html="renderSystemText(toast.text, { lead: 'drop', kind: tone })"
    />
    <div v-else class="min-w-0 grow break-words"><GlyphText :text="plain" /></div>
    <div class="flex shrink-0 items-center gap-1">
      <Button
        v-for="a in toast.actions.slice(0, 2)"
        :key="a.data"
        variant="ghost"
        size="sm"
        :aria-pressed="buttonLabel(a.label).current || undefined"
        @click="runAction(a)"
      >
        <ButtonLabel :text="a.label" />
      </Button>
      <Button variant="ghost" size="sm" aria-label="Close" @click="closeToast(toast.id)">
        <XIcon />
      </Button>
    </div>
  </div>
</template>
