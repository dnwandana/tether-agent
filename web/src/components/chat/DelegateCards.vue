<script setup lang="ts">
import { computed } from "vue"
import { parseDelegate } from "./parse"

const props = defineProps<{ text: string; args: string }>()

const res = computed(() => parseDelegate(props.text))

const parsedArgs = computed<Record<string, unknown>>(() => {
  try {
    const v = JSON.parse(props.args)
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : {}
  } catch {
    return {}
  }
})

// Returns the goal of a child, or "Task N".
function goal(task: number): string {
  const tasks = parsedArgs.value.tasks
  if (Array.isArray(tasks)) {
    const t = tasks[task - 1]
    if (t && typeof t === "object" && typeof (t as { goal?: unknown }).goal === "string")
      return (t as { goal: string }).goal
  }
  const g = parsedArgs.value.goal
  return typeof g === "string" && g !== "" ? g : `Task ${task}`
}

function calls(n: number): string {
  return `${n} tool ${n === 1 ? "call" : "calls"}`
}
</script>

<template>
  <div v-if="res && res.results.length" class="child-cards">
    <div v-for="r in res.results" :key="r.task" class="child">
      <div class="row">
        <b>{{ goal(r.task) }}</b>
        <span class="grow"></span>
        <span class="chip" :class="r.status === 'ok' ? 'chip-ok' : 'chip-bad'">{{ r.status }}</span>
      </div>
      <span v-if="r.summary" class="c-2">{{ r.summary }}</span>
      <span v-if="r.error" class="c-bad t-12">{{ r.error }}</span>
      <span class="meta">{{ calls(r.tool_calls) }} · {{ r.duration_s }} s</span>
    </div>
  </div>
  <p v-else-if="res && res.batch" class="t-13">
    Started batch {{ res.batch.id }} with {{ res.batch.children.length }}
    {{ res.batch.children.length === 1 ? "child" : "children" }}:
    {{ res.batch.children.join(", ") }}
  </p>
</template>
