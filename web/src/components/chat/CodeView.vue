<script setup lang="ts">
import { computed } from "vue"
import { parseReadFile, unwrapUntrusted } from "./parse"

const props = defineProps<{ path: string; text: string }>()

const res = computed(() => parseReadFile(props.text))
const untrusted = computed(() => unwrapUntrusted(props.text).untrusted)
const numbers = computed(() => res.value.lines.map((_, i) => res.value.start + i).join("\n"))
const source = computed(() => res.value.lines.join("\n"))
const last = computed(() => res.value.start + res.value.lines.length - 1)
</script>

<template>
  <div class="result" :class="{ untrusted }">
    <div class="result-h">
      <span>📖</span>
      <span class="path">{{ path }}</span>
      <span class="grow"></span>
      <span v-if="res.lines.length" class="c-3">lines {{ res.start }}–{{ last }}</span>
    </div>
    <div v-if="res.lines.length" class="code">
      <pre class="ln">{{ numbers }}</pre>
      <pre class="src">{{ source }}</pre>
    </div>
    <p v-if="res.next !== null" class="t-12 c-3">More lines follow from line {{ res.next }}.</p>
    <p v-if="res.note" class="t-12 c-3">{{ res.note }}</p>
  </div>
</template>
