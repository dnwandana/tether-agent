<script setup lang="ts">
import { ref } from "vue"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { rememberOpener } from "@/lib/layer"

defineProps<{ items: { label: string; act: string; danger?: boolean }[] }>()
const emit = defineEmits<{ select: [act: string] }>()

const open = ref(false)
const trigger = ref<{ $el: HTMLElement } | null>(null)

function triggerEl(): HTMLElement | null {
  return trigger.value?.$el ?? null
}

// The menu item is gone when the select handler opens a dialog.
// The dialog must return the focus to the trigger, so remember it first.
function pick(act: string): void {
  rememberOpener(triggerEl())
  emit("select", act)
}
</script>

<template>
  <DropdownMenu v-model:open="open">
    <DropdownMenuTrigger as-child>
      <Button ref="trigger" variant="ghost" size="icon" type="button" aria-label="More actions"
        >⋯</Button
      >
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" loop class="min-w-44" @keydown.tab="open = false">
      <DropdownMenuGroup>
        <DropdownMenuItem
          v-for="it in items"
          :key="it.act"
          :variant="it.danger ? 'destructive' : 'default'"
          class="max-tab:min-h-11"
          @select="pick(it.act)"
        >
          {{ it.label }}
        </DropdownMenuItem>
      </DropdownMenuGroup>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
