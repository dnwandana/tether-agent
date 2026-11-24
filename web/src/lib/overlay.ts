// useOverlay gives an overlay the shared dialog rules. Only the top overlay of the stack gets Esc.
// A modal overlay also gets the focus on open and keeps Tab inside it. The focus goes back to the
// opener on close.
import { nextTick, onScopeDispose, watch, type Ref } from "vue"

type Entry = {
  el: Ref<HTMLElement | null>
  onClose: () => void
  modal: boolean
  opener: HTMLElement | null
}

const stack: Entry[] = []

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

function focusables(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE))
}

function onKey(e: KeyboardEvent) {
  const top = stack[stack.length - 1]
  if (!top) return
  if (e.key === "Escape") {
    e.preventDefault()
    e.stopPropagation()
    top.onClose()
    return
  }
  if (e.key === "Tab" && top.modal && top.el.value) {
    const root = top.el.value
    const items = focusables(root)
    if (items.length === 0) {
      e.preventDefault()
      root.focus()
      return
    }
    const first = items[0]!
    const last = items[items.length - 1]!
    const active = document.activeElement
    const inside = active instanceof Node && root.contains(active)
    if (e.shiftKey) {
      if (!inside || active === first || active === root) {
        e.preventDefault()
        last.focus()
      }
    } else if (!inside || active === last) {
      e.preventDefault()
      first.focus()
    }
  }
}

function push(entry: Entry) {
  if (stack.length === 0) window.addEventListener("keydown", onKey, true)
  stack.push(entry)
}

function remove(entry: Entry) {
  const i = stack.indexOf(entry)
  if (i >= 0) stack.splice(i, 1)
  if (stack.length === 0) window.removeEventListener("keydown", onKey, true)
}

export function useOverlay(
  open: Ref<boolean>,
  el: Ref<HTMLElement | null>,
  opts: { onClose: () => void; modal?: boolean },
): void {
  let entry: Entry | null = null

  // The sync watcher reads the opener before any focus moves.
  watch(
    open,
    (on) => {
      if (on) {
        if (entry) return
        entry = {
          el,
          onClose: opts.onClose,
          // A getter, so that a modal value that changes while the overlay is open takes effect.
          get modal() {
            return !!opts.modal
          },
          opener: document.activeElement as HTMLElement | null,
        }
        push(entry)
        return
      }
      if (!entry) return
      const opener = entry.opener
      remove(entry)
      entry = null
      void nextTick(() => {
        if (opener && opener.isConnected) opener.focus()
      })
    },
    { flush: "sync", immediate: true },
  )

  watch(
    open,
    (on) => {
      if (!on || !opts.modal) return
      const root = el.value
      if (!root) return
      const active = document.activeElement
      if (active instanceof Node && root.contains(active)) return
      const items = focusables(root)
      if (items.length > 0) {
        items[0]!.focus()
      } else {
        root.setAttribute("tabindex", "-1")
        root.focus()
      }
    },
    { flush: "post", immediate: true },
  )

  onScopeDispose(() => {
    if (entry) remove(entry)
    entry = null
  })
}
