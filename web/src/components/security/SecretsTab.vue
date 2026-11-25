<script setup lang="ts">
import { ref } from "vue"
import type { PinView, SecretView } from "../../api/types.gen"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import DataTable from "../DataTable.vue"
import type { Column } from "../../lib/table"
import ResultNote from "../ResultNote.vue"
import StateBlock from "../StateBlock.vue"
import { useCommand, type ResultNoteData } from "../../lib/command"
import { fullTime, shortTime } from "../../lib/time"
import { ui } from "../../stores/ui"

defineProps<{ secrets: SecretView[] }>()
const emit = defineEmits<{ changed: [] }>()

const { run } = useCommand()
const note = ref<ResultNoteData | null>(null)

const cols: Column[] = [
  { key: "name", label: "Name", type: "text" },
  { key: "state", label: "State", type: "badge", width: "14rem" },
  { key: "commands", label: "Bound to", type: "text" },
  { key: "pins", label: "Allowed programs", type: "block" },
  { key: "actions", label: "", type: "act", width: "8rem" },
]

// Removes one allowed program of a name. The path is the rest of the line.
async function unpin(s: SecretView, p: PinView): Promise<void> {
  await run(`/secrets unpin ${s.name} ${p.path}`, { result: note, title: "Unpin" })
  emit("changed")
}

// Asks for a secret value. The value goes in the card of the tray, never in this page.
async function setValue(s: SecretView): Promise<void> {
  const kind = await run(s.set)
  if (kind !== "error") {
    note.value = {
      title: "Set value",
      at: new Date(),
      text: "Type the value in the 🔑 card in ⏳ Waiting.",
      kind: "info",
    }
    ui.trayOpen = true
  }
  emit("changed")
}
</script>

<template>
  <div>
    <p class="text-sm text-muted-foreground">
      Bound exec_secrets names. Values are never shown here, in a log, or to the model.
    </p>
    <ResultNote :note="note" @close="note = null" />
    <StateBlock
      v-if="secrets.length === 0"
      state="empty"
      text="No bound secrets."
      hint="Bind a secret in exec_secrets in config.yaml."
    />
    <DataTable
      v-else
      :columns="cols"
      :rows="secrets as unknown as Record<string, unknown>[]"
      row-key="name"
      page-key="secrets"
      page-label="Secrets"
    >
      <template #cell-state="{ row }">
        <Badge v-if="row.state === 'set'" variant="secondary">✓ set</Badge>
        <Badge
          v-else-if="row.state === 'check'"
          variant="outline"
          class="text-warning"
          :title="fullTime(row.failed_at as string)"
          >✓ set · auth failed {{ shortTime(row.failed_at as string) }}</Badge
        >
        <Badge v-else variant="outline" class="text-warning">not set</Badge>
      </template>
      <template #cell-commands="{ row }">{{ (row.commands as string[]).join(", ") }}</template>
      <template #cell-pins="{ row }">
        <div
          v-for="p in row.pins as PinView[]"
          :key="p.path"
          class="flex flex-wrap items-center gap-2"
        >
          <code class="font-mono break-all">{{ p.path }}</code>
          <Collapsible>
            <CollapsibleTrigger as-child>
              <Button variant="ghost" size="xs" class="font-mono">{{
                p.sha256.slice(0, 12)
              }}</Button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <span class="font-mono text-xs break-all">{{ p.sha256 }}</span>
            </CollapsibleContent>
          </Collapsible>
          <Button
            variant="outline"
            size="sm"
            class="max-tab:h-11"
            type="button"
            @click="unpin(row as unknown as SecretView, p)"
          >
            Unpin
          </Button>
        </div>
        <span v-if="(row.pins as PinView[]).length === 0" class="text-muted-foreground"
          >No allowed programs.</span
        >
      </template>
      <template #cell-actions="{ row }">
        <Button
          variant="secondary"
          size="sm"
          class="max-tab:h-11"
          type="button"
          @click="setValue(row as unknown as SecretView)"
        >
          Set value…
        </Button>
      </template>
    </DataTable>
  </div>
</template>
