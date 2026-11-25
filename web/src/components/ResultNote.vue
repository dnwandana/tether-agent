<script setup lang="ts">
import { computed } from "vue"
import { CircleAlertIcon, CircleCheckIcon, InfoIcon, TriangleAlertIcon, XIcon } from "@lucide/vue"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import type { ResultNoteData } from "../lib/command"
import { toneOfKind } from "../lib/glyphs"
import { renderSystemText } from "../lib/markdown"

const props = defineProps<{ note: ResultNoteData | null }>()
const emit = defineEmits<{ close: [] }>()

const time = computed(() => {
  const at = props.note?.at
  if (!at) return ""
  return `${String(at.getHours()).padStart(2, "0")}:${String(at.getMinutes()).padStart(2, "0")}`
})
</script>

<template>
  <Alert
    v-if="note"
    :role="note.kind === 'error' ? 'alert' : 'status'"
    :variant="note.kind === 'error' ? 'destructive' : 'default'"
    :data-kind="note.kind"
    class="mt-3"
  >
    <CircleCheckIcon v-if="note.kind === 'ok'" class="text-success" />
    <InfoIcon v-else-if="note.kind === 'info'" class="text-info" />
    <TriangleAlertIcon v-else-if="note.kind === 'warn'" class="text-warning" />
    <CircleAlertIcon v-else />
    <AlertTitle class="flex items-center gap-2">
      <span data-title class="font-semibold [overflow-wrap:anywhere]">{{ note.title }}</span>
      <span data-time class="ml-auto text-xs text-muted-foreground">{{ time }}</span>
      <Button variant="ghost" size="icon" type="button" aria-label="Close" @click="emit('close')">
        <XIcon />
      </Button>
    </AlertTitle>
    <AlertDescription>
      <!-- renderSystemText escapes raw HTML, so v-html is safe here. -->
      <div
        class="typeset typeset-chat"
        v-html="renderSystemText(note.text, { lead: 'drop', kind: toneOfKind(note.kind) })"
      ></div>
    </AlertDescription>
  </Alert>
</template>
