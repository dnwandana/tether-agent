import { onMounted, onUnmounted, type Ref } from "vue"

// The dirty flags of the mounted forms. A new build does not reload the page while one is true.
const flags = new Set<Ref<boolean>>()

// Adds the flag of a form while the component is mounted.
export function useDirty(dirty: Ref<boolean>): void {
  onMounted(() => flags.add(dirty))
  onUnmounted(() => flags.delete(dirty))
}

// Returns true when a mounted form has unsaved changes.
export function anyDirty(): boolean {
  for (const f of flags) if (f.value) return true
  return false
}
