<script setup lang="ts">
// Shows the icon of the first mark of a text in its tone, then the rest of the text.
import { computed } from "vue"

import { cn } from "@/lib/utils"
import { GLYPH_TONE_CLASS, splitGlyph } from "@/lib/glyphs"

// truncate cuts a long text to one line with an ellipsis. A wrapping text keeps it off.
const props = withDefaults(
  defineProps<{ text: string; iconClass?: string; truncate?: boolean }>(),
  { iconClass: "size-4 shrink-0", truncate: false },
)
const s = computed(() => splitGlyph(props.text))
</script>

<template>
  <span :class="cn('inline-flex min-w-0 items-center gap-1.5', truncate && 'max-w-full')">
    <component
      :is="s.glyph.icon"
      v-if="s.glyph"
      :class="cn(GLYPH_TONE_CLASS[s.glyph.tone], iconClass)"
    />
    <span v-if="s.glyph && s.rest === ''" class="sr-only">{{ s.glyph.word }}</span>
    <span v-else :class="cn('min-w-0', truncate && 'truncate')">{{ s.rest }}</span>
  </span>
</template>
