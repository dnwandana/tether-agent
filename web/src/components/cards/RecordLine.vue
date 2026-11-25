<script setup lang="ts">
import { computed } from "vue"
import type { AnsweredCard } from "../../api/types.gen"
import { splitGlyph } from "../../lib/glyphs"
import GlyphText from "../GlyphText.vue"
import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceDescription,
  TraceLine,
  TraceTitle,
} from "@/components/ui/trace"

const props = defineProps<{ record: AnsweredCard }>()

const TONE = { ok: "text-success", bad: "", warn: "text-warning" } as const

// The glyph key of the header gives the tone: allowed, denied, or no answer.
const tone = computed<"ok" | "bad" | "warn">(() => {
  const key = splitGlyph(props.record.header).glyph?.key
  if (key === "ok") return "ok"
  if (key === "denied") return "bad"
  return "warn"
})
const time = computed(() => {
  const d = new Date(props.record.at)
  if (Number.isNaN(d.getTime())) return ""
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
})
</script>

<template>
  <Trace>
    <TraceCollapsible v-slot="{ open }">
      <TraceCollapsibleTrigger>
        <TraceLine
          :data-tone="tone"
          :status="tone === 'bad' ? 'error' : undefined"
          :class="TONE[tone]"
        >
          <TraceTitle class="shrink-0"><GlyphText :text="record.header" /></TraceTitle>
          <span class="min-w-0 truncate">{{ record.preview }}</span>
          <TraceDescription>{{ time }}</TraceDescription>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent v-if="open">
        <div class="flex flex-col gap-2">
          <div class="whitespace-pre-wrap break-words">{{ record.preview }}</div>
          <RouterLink :to="{ name: 'security', query: { tab: 'log', scope: 'dashboard' } }">
            Open the approval log
          </RouterLink>
        </div>
      </TraceCollapsibleContent>
    </TraceCollapsible>
  </Trace>
</template>
