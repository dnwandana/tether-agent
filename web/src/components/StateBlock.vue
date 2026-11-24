<script setup lang="ts">
import { CircleAlertIcon, ListIcon } from "@lucide/vue"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"

defineProps<{
  state: "loading" | "empty" | "error"
  title?: string
  text?: string
  hint?: string
  action?: string
}>()
const emit = defineEmits<{ action: [] }>()
</script>

<template>
  <div v-if="state === 'loading'" role="status" aria-busy="true" aria-label="Loading">
    <div v-for="n in 5" :key="n" data-slot="skeleton-row" class="flex items-center gap-3 py-2">
      <Skeleton class="size-6" />
      <Skeleton class="h-4 flex-1" />
      <Skeleton class="h-4 w-[15%]" />
      <Skeleton class="h-4 w-[10%]" />
    </div>
  </div>
  <Empty v-else-if="state === 'empty'">
    <EmptyHeader>
      <EmptyMedia variant="icon"><ListIcon /></EmptyMedia>
      <EmptyTitle>{{ title ?? text }}</EmptyTitle>
      <EmptyDescription v-if="title && text">{{ text }}</EmptyDescription>
      <EmptyDescription v-if="hint" data-hint>{{ hint }}</EmptyDescription>
    </EmptyHeader>
    <EmptyContent v-if="action">
      <Button variant="secondary" @click="emit('action')">{{ action }}</Button>
    </EmptyContent>
  </Empty>
  <Alert v-else variant="destructive">
    <CircleAlertIcon />
    <AlertTitle>{{ title ?? "Could not load" }}</AlertTitle>
    <AlertDescription>
      <span data-cause>{{ text }}</span>
      <span v-if="hint" data-hint>{{ hint }}</span>
      <Button variant="secondary" size="sm" @click="emit('action')">
        {{ action ?? "Retry" }}
      </Button>
    </AlertDescription>
  </Alert>
</template>
