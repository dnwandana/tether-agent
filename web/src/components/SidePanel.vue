<script setup lang="ts">
import { nextTick, ref, watch } from "vue"
import { Alert } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import PanelFrame from "./PanelFrame.vue"

const props = defineProps<{ open: boolean; title: string; dirty?: boolean }>()
const emit = defineEmits<{ close: [] }>()

// True while the panel asks "Discard the changes?".
const asking = ref(false)

// Closes the panel, or asks first when the panel has unsaved changes.
function requestClose(): void {
  if (props.dirty !== true) {
    emit("close")
    return
  }
  asking.value = true
}

// Closes the question. The question takes its buttons away, so the focus moves to the first
// control in the panel and does not go to the page behind it.
async function keepEditing(e: MouseEvent): Promise<void> {
  const frame = (e.currentTarget as HTMLElement | null)?.closest<HTMLElement>("[data-mode]")
  asking.value = false
  await nextTick()
  const first = frame?.querySelector<HTMLElement>(
    '[data-slot="panel-body"] :is(input, select, textarea, button, a[href], [tabindex]:not([tabindex="-1"]))',
  )
  ;(first ?? frame)?.focus()
}

function discard(): void {
  asking.value = false
  emit("close")
}

watch(
  () => props.open,
  (on) => {
    if (!on) {
      asking.value = false
      return
    }
    // Chat closes its TurnDrawer, so that only one side column shows.
    document.dispatchEvent(new CustomEvent("tether:panel-open"))
  },
)
</script>

<template>
  <PanelFrame
    :open="open"
    :title="title"
    dock-query="(min-width: 1440px)"
    @request-close="requestClose"
  >
    <Alert v-if="asking" class="mb-4">
      <div class="flex flex-wrap items-center gap-2">
        <span>Discard the changes?</span>
        <Button variant="secondary" size="sm" type="button" @click="keepEditing">
          Keep editing
        </Button>
        <Button variant="destructive" size="sm" type="button" @click="discard">Discard</Button>
      </div>
    </Alert>
    <slot />
  </PanelFrame>
</template>
