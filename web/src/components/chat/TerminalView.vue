<script setup lang="ts">
import { computed } from "vue"
import { parseTerminal } from "./parse"

const props = defineProps<{ command: string; text: string }>()
const res = computed(() => parseTerminal(props.text))
</script>

<template>
  <div class="result">
    <div class="result-h">
      <span aria-hidden="true">💻</span>
      <span class="path">{{ props.command }}</span>
      <span class="grow"></span>
      <span v-if="res.background" class="c-3">background · PID {{ res.background.pid }}</span>
      <span v-else-if="res.exit === 0" class="c-ok">exit 0</span>
      <span v-else-if="res.exit !== null" class="c-bad">exit {{ res.exit }}</span>
    </div>
    <div class="term">
      <span class="ps">$</span> {{ props.command }}
      <template v-if="res.background">{{ res.background.logPath }}</template
      ><template v-else>{{ res.output }}</template
      ><span v-for="(n, i) in res.notes" :key="i" class="dim"> {{ n }}</span>
    </div>
  </div>
</template>
