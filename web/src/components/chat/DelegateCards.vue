<script setup lang="ts">
import { computed } from "vue"
import { CircleCheckIcon, CircleXIcon } from "@lucide/vue"
import { Badge } from "@/components/ui/badge"
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
import { parseDelegate } from "./parse"
import { duration } from "../../lib/time"

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
  <Trace v-if="res && res.results.length">
    <TraceCollapsible v-for="r in res.results" :key="r.task" :default-open="r.status !== 'ok'">
      <TraceCollapsibleTrigger>
        <TraceLine :status="r.status === 'ok' ? undefined : 'error'">
          <TraceIcon>
            <CircleCheckIcon v-if="r.status === 'ok'" />
            <CircleXIcon v-else />
          </TraceIcon>
          <TraceTitle>{{ goal(r.task) }}</TraceTitle>
          <TraceDescription
            >{{ calls(r.tool_calls) }} · {{ duration(r.duration_s) }}</TraceDescription
          >
          <Badge :variant="r.status === 'ok' ? 'secondary' : 'destructive'">{{ r.status }}</Badge>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent>
        <p v-if="r.summary">{{ r.summary }}</p>
        <p v-if="r.error" class="text-destructive">{{ r.error }}</p>
      </TraceCollapsibleContent>
    </TraceCollapsible>
  </Trace>
  <p v-else-if="res && res.batch" class="text-sm">
    Started batch {{ res.batch.id }} with {{ res.batch.children.length }}
    {{ res.batch.children.length === 1 ? "child" : "children" }}:
    {{ res.batch.children.join(", ") }}
  </p>
</template>
