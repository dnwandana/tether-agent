// Helpers for stacked layers.
//
// Reka handles Esc on `window` and does not stop the event. So the Esc listener of a custom
// docked part (the side panel, the turn drawer) does nothing while `hasOpenLayer()` is true.
// One Esc then closes only the top layer.
//
// Reka returns the focus only to the element that was active at open. When a menu item or a
// palette row opened the dialog, that element is gone, and the focus goes to `body`.
// `useReturnFocus` and `rememberOpener` fix this.
import { type Ref, watch } from "vue"

/** Returns true when the document has an open Reka layer. */
export function hasOpenLayer(): boolean {
  return document.querySelector("[data-dismissable-layer]") !== null
}

/** Returns true when the target is inside the toaster. */
export function fromToaster(target: EventTarget | null): boolean {
  if (!(target instanceof Node)) return false
  const el = target instanceof Element ? target : target.parentElement
  return el?.closest("[data-sonner-toaster]") != null
}

/** Stops a modal from closing on a tap on a toast. Use it as the outside-event handler. */
export function guardOutside(e: Event): void {
  if (fromToaster(e.target)) e.preventDefault()
}

let opener: HTMLElement | null = null

/** Remembers the element that returns the focus when the active element is gone. */
export function rememberOpener(el: HTMLElement | null): void {
  opener = el
}

/** Keeps the active element at open. On close, focuses it, or the remembered opener. */
export function useReturnFocus(open: Ref<boolean>): { onCloseAutoFocus: (e: Event) => void } {
  let previous: HTMLElement | null = null
  watch(
    open,
    (value) => {
      if (value) previous = document.activeElement as HTMLElement | null
    },
    { flush: "sync" },
  )
  function onCloseAutoFocus(e: Event): void {
    const target = previous?.isConnected ? previous : opener?.isConnected ? opener : null
    previous = null
    opener = null
    if (!target) return
    e.preventDefault()
    target.focus()
  }
  return { onCloseAutoFocus }
}
