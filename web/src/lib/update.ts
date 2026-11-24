import { toast, ui } from "../stores/ui"
import { anyDirty } from "./dirty"

const KEY_DRAFT = "tether.draft"
const KEY_FROM = "tether.updated-from"
let version = ""

// Stores the version of the gateway. After a reload for a new build it says "Updated to vX.Y.Z.".
export function setVersion(v: string): void {
  version = v
  let from: string | null = null
  try {
    from = sessionStorage.getItem(KEY_FROM)
    if (from !== null) sessionStorage.removeItem(KEY_FROM)
  } catch {
    from = null
  }
  if (from && version && from !== version) {
    toast("Updated to " + (version.startsWith("v") ? version : "v" + version) + ".", "ok")
  }
}

// Returns true when the composer keeps a draft.
function hasDraft(): boolean {
  try {
    return !!sessionStorage.getItem(KEY_DRAFT)
  } catch {
    return false
  }
}

// Writes the old version for the "Updated to" toast, then loads the new page files.
export function reloadNow(): void {
  try {
    if (version) sessionStorage.setItem(KEY_FROM, version)
  } catch {
    // The toast is lost. The reload still runs.
  }
  location.reload()
}

// Reloads at once, or shows the update banner while a form is dirty or a draft waits.
export function onNewBuild(): void {
  if (anyDirty() || hasDraft()) ui.updateReady = true
  else reloadNow()
}
