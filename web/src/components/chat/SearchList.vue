<script setup lang="ts">
import { computed } from "vue"
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
import { parseSessionSearch, parseWebSearch } from "./parse"

const props = defineProps<{ kind: "web" | "session"; text: string; query: string }>()

const web = computed(() => (props.kind === "web" ? parseWebSearch(props.text) : []))
const sessions = computed(() => (props.kind === "session" ? parseSessionSearch(props.text) : []))
const count = computed(() => (props.kind === "web" ? web.value.length : sessions.value.length))

// A URL is a link only for http and https.
function safe(url: string): boolean {
  return /^https?:\/\//i.test(url)
}
</script>

<template>
  <Trace
    :data-untrusted="kind === 'web' || undefined"
    class="data-[untrusted=true]:rounded-md data-[untrusted=true]:border data-[untrusted=true]:border-dashed data-[untrusted=true]:border-warning/60 data-[untrusted=true]:p-2"
  >
    <TraceCollapsible default-open>
      <TraceCollapsibleTrigger>
        <TraceLine>
          <TraceIcon>🔍</TraceIcon>
          <TraceTitle>{{ query }}</TraceTitle>
          <TraceDescription>{{ count }} {{ count === 1 ? "result" : "results" }}</TraceDescription>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent>
        <Trace>
          <template v-if="kind === 'web'">
            <div v-for="(h, i) in web" :key="i" class="flex min-w-0 flex-col gap-0.5">
              <a
                v-if="safe(h.url)"
                :href="h.url"
                target="_blank"
                rel="noopener noreferrer"
                class="w-fit max-w-full"
              >
                <TraceLine>
                  <TraceTitle>{{ i + 1 }}. {{ h.title }}</TraceTitle>
                  <TraceDescription class="font-mono">{{ h.url }}</TraceDescription>
                </TraceLine>
              </a>
              <span v-else class="w-fit max-w-full">
                <TraceLine>
                  <TraceTitle>{{ i + 1 }}. {{ h.title }}</TraceTitle>
                  <TraceDescription class="font-mono">{{ h.url }}</TraceDescription>
                </TraceLine>
              </span>
              <p v-if="h.snippet" class="text-xs text-muted-foreground">{{ h.snippet }}</p>
            </div>
          </template>
          <template v-else>
            <div v-for="(h, i) in sessions" :key="i" class="flex min-w-0 flex-col gap-0.5">
              <RouterLink
                :to="{
                  name: 'transcript',
                  params: { id: h.sessionId },
                  query: { message: String(h.messageId) },
                }"
                class="w-fit max-w-full"
              >
                <TraceLine>
                  <TraceTitle>{{ h.title }}</TraceTitle>
                  <TraceDescription>{{ h.date }}</TraceDescription>
                </TraceLine>
              </RouterLink>
              <p class="text-xs text-muted-foreground">{{ h.snippet }}</p>
            </div>
          </template>
        </Trace>
      </TraceCollapsibleContent>
    </TraceCollapsible>
  </Trace>
</template>
