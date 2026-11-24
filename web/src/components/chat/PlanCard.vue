<script setup lang="ts">
import { computed } from "vue"
import type { PlanItem } from "../../api/types.gen"
import { Badge } from "@/components/ui/badge"
import { Progress } from "@/components/ui/progress"
import {
  Trace,
  TraceCollapsible,
  TraceCollapsibleContent,
  TraceCollapsibleTrigger,
  TraceDescription,
  TraceLine,
  TraceTitle,
} from "@/components/ui/trace"
import { cn } from "@/lib/utils"

const props = defineProps<{ items: PlanItem[]; before?: PlanItem[] }>()

const MARK: Record<string, string> = {
  completed: "[x]",
  in_progress: "[>]",
  cancelled: "[~]",
  pending: "[ ]",
}

const total = computed(() => props.items.length)
const doneCount = computed(() => props.items.filter((i) => i.state === "completed").length)
const allClosed = computed(
  () =>
    total.value > 0 && props.items.every((i) => i.state === "completed" || i.state === "cancelled"),
)
const pct = computed(() =>
  total.value === 0 ? 0 : Math.round((doneCount.value / total.value) * 100),
)
const header = computed(() => (allClosed.value ? "📋 Plan done" : "📋 Plan"))
const progress = computed(() => `${doneCount.value} of ${total.value} done`)
const running = computed(() => props.items.some((i) => i.state === "in_progress"))

// Returns true when the item at the index has a state that differs from the same item before.
function changed(i: number): boolean {
  const old = props.before?.[i]
  return old !== undefined && old.state !== props.items[i].state
}
</script>

<template>
  <Trace>
    <TraceCollapsible default-open>
      <TraceCollapsibleTrigger>
        <TraceLine :status="running ? 'running' : undefined">
          <TraceTitle>{{ header }}</TraceTitle>
          <TraceDescription v-if="!allClosed">{{ progress }}</TraceDescription>
        </TraceLine>
      </TraceCollapsibleTrigger>
      <TraceCollapsibleContent>
        <div class="flex flex-col gap-2">
          <Progress :model-value="pct" />
          <Trace role="list">
            <TraceLine
              v-for="(item, i) in items"
              :key="i"
              role="listitem"
              :data-state="item.state"
              :data-child="item.parent >= 0 ? '' : undefined"
              :data-changed="changed(i) ? '' : undefined"
              :status="
                item.state === 'in_progress'
                  ? 'running'
                  : item.state === 'pending'
                    ? 'pending'
                    : undefined
              "
              :class="
                cn(
                  item.parent >= 0 && 'ml-4',
                  item.state === 'cancelled' && 'line-through',
                  item.state === 'completed' && 'text-foreground',
                )
              "
            >
              <span class="font-mono">{{ MARK[item.state] ?? "[ ]" }}</span>
              <TraceTitle>{{ item.text }}</TraceTitle>
              <Badge v-if="changed(i)" variant="outline">changed</Badge>
            </TraceLine>
          </Trace>
        </div>
      </TraceCollapsibleContent>
    </TraceCollapsible>
  </Trace>
</template>
