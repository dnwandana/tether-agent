<script setup lang="ts">
import { computed } from "vue"
import { parseDiff } from "./parse"

const props = defineProps<{ text: string }>()
const res = computed(() => parseDiff(props.text))
</script>

<template>
  <div class="result">
    <div class="result-h">
      <span aria-hidden="true">🔧</span>
      <span class="path">{{ res.summary || res.path }}</span>
      <span class="grow"></span>
      <span class="c-ok">+{{ res.added }}</span>
      <span class="c-bad">−{{ res.removed }}</span>
    </div>
    <div class="diff">
      <template v-for="(l, i) in res.lines" :key="i">
        <div v-if="l.kind === 'hunk'" class="hunk">{{ l.text }}</div>
        <div v-else class="dl" :class="{ add: l.kind === 'add', del: l.kind === 'del' }">
          <span>{{ l.a ?? "" }}</span>
          <span>{{ l.b ?? "" }}</span>
          <span>{{ l.kind === "add" ? "+" : l.kind === "del" ? "−" : " " }}</span>
          <span>{{ l.text }}</span>
        </div>
      </template>
    </div>
  </div>
</template>
