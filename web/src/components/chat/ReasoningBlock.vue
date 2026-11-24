<script setup lang="ts">
import { BrainIcon } from "@lucide/vue"
import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceDescription,
  TraceIcon,
  TraceLine,
  TraceTitle,
} from "@/components/ui/trace"

defineProps<{ text: string; tokens: number }>()
</script>

<template>
  <Trace v-if="text">
    <TraceCollapsible>
      <TraceCollapsibleTrigger>
        <TraceLine>
          <TraceIcon><BrainIcon /></TraceIcon>
          <TraceTitle>Reasoning</TraceTitle>
          <TraceDescription>{{ tokens.toLocaleString("en-US") }} tokens</TraceDescription>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent class="whitespace-pre-wrap">{{ text }}</TraceCollapsibleContent>
    </TraceCollapsible>
  </Trace>
</template>
