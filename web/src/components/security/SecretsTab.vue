<script setup lang="ts">
import { ref } from "vue"
import type { PinView, SecretView } from "../../api/types.gen"
import DataTable from "../DataTable.vue"
import ResultNote from "../ResultNote.vue"
import StateBlock from "../StateBlock.vue"
import { useCommand, type ResultNoteData } from "../../lib/command"
import { fullTime, shortTime } from "../../lib/time"
import { ui } from "../../stores/ui"

defineProps<{ secrets: SecretView[] }>()
const emit = defineEmits<{ changed: [] }>()

const { run } = useCommand()
const note = ref<ResultNoteData | null>(null)

const cols = [
  { key: "name", label: "Name" },
  { key: "state", label: "State" },
  { key: "commands", label: "Bound to" },
  { key: "pins", label: "Allowed programs" },
  { key: "actions", label: "", align: "right" as const },
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
    <p>Bound exec_secrets names. Values are never shown here, in a log, or to the model.</p>
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
    >
      <template #cell-state="{ row }">
        <span v-if="row.state === 'set'" class="chip chip-ok">✓ set</span>
        <span
          v-else-if="row.state === 'check'"
          class="chip chip-warn"
          :title="fullTime(row.failed_at as string)"
          >✓ set · auth failed {{ shortTime(row.failed_at as string) }}</span
        >
        <span v-else class="chip chip-warn">not set</span>
      </template>
      <template #cell-commands="{ row }">{{ (row.commands as string[]).join(", ") }}</template>
      <template #cell-pins="{ row }">
        <div v-for="p in row.pins as PinView[]" :key="p.path" class="pin">
          <code>{{ p.path }}</code>
          <details class="pin-sha">
            <summary class="mono t-12">{{ p.sha256.slice(0, 12) }}</summary>
            <span class="mono t-12">{{ p.sha256 }}</span>
          </details>
          <button
            class="btn btn-danger-soft btn-sm"
            type="button"
            @click="unpin(row as unknown as SecretView, p)"
          >
            Unpin
          </button>
        </div>
        <span v-if="(row.pins as PinView[]).length === 0" class="c-3">No allowed programs.</span>
      </template>
      <template #cell-actions="{ row }">
        <button
          class="btn btn-secondary btn-sm"
          type="button"
          @click="setValue(row as unknown as SecretView)"
        >
          Set value…
        </button>
      </template>
    </DataTable>
  </div>
</template>

<style scoped>
.pin-sha summary {
  cursor: pointer;
}
code {
  overflow-wrap: anywhere;
}
</style>
