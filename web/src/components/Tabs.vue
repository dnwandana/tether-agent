<script setup lang="ts">
import { nextTick, useSlots } from "vue"
import { Badge } from "@/components/ui/badge"
import { Tabs as TabsRoot, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"

defineProps<{ tabs: { id: string; label: string; count?: number }[] }>()
const model = defineModel<string>({ required: true })
const slots = useSlots()

// A click gives a mousedown and a focus event before the parent writes the value back.
// Reka selects the tab on both events. This guard sends the update only once.
let pending: string | null = null
function select(v: string | number): void {
  const id = String(v)
  if (id === model.value || id === pending) return
  pending = id
  model.value = id
  void nextTick(() => {
    pending = null
  })
}
</script>

<template>
  <TabsRoot :model-value="model" @update:model-value="select">
    <TabsList loop class="max-tab:h-auto max-tab:flex-wrap">
      <!-- With no panel slot, no tab points to a panel, so aria-controls is removed. -->
      <TabsTrigger
        v-for="t in tabs"
        :key="t.id"
        :value="t.id"
        v-bind="slots.default ? {} : { 'aria-controls': undefined }"
      >
        {{ t.label }}
        <Badge v-if="typeof t.count === 'number'" variant="secondary">{{ t.count }}</Badge>
      </TabsTrigger>
    </TabsList>
    <TabsContent v-if="slots.default" :value="model">
      <slot />
    </TabsContent>
  </TabsRoot>
</template>
