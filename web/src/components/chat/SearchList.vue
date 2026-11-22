<script setup lang="ts">
import { computed } from "vue"
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
  <div class="result" :class="{ untrusted: kind === 'web' }">
    <div class="result-h">
      <span>🔍</span>
      <span class="path">{{ query }}</span>
      <span class="grow"></span>
      <span class="c-3">{{ count }} {{ count === 1 ? "result" : "results" }}</span>
    </div>
    <div class="list">
      <template v-if="kind === 'web'">
        <div v-for="(h, i) in web" :key="i" class="li">
          <span class="c-3 mono">{{ i + 1 }}.</span>
          <b>{{ h.title }}</b>
          <a
            v-if="safe(h.url)"
            :href="h.url"
            target="_blank"
            rel="noopener noreferrer"
            class="mono t-12"
            >{{ h.url }}</a
          >
          <span v-else class="mono t-12">{{ h.url }}</span>
          <span v-if="h.snippet" class="c-2">{{ h.snippet }}</span>
        </div>
      </template>
      <template v-else>
        <div v-for="(h, i) in sessions" :key="i" class="li">
          <RouterLink
            :to="{
              name: 'transcript',
              params: { id: h.sessionId },
              query: { message: String(h.messageId) },
            }"
            >{{ h.title }}</RouterLink
          >
          <span class="c-3 t-12">{{ h.date }}</span>
          <span class="c-2">{{ h.snippet }}</span>
        </div>
      </template>
    </div>
  </div>
</template>
