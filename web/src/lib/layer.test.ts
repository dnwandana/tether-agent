import { afterEach, describe, expect, it } from "vitest"
import { effectScope, ref } from "vue"
import { fromToaster, guardOutside, hasOpenLayer, rememberOpener, useReturnFocus } from "./layer"

function button(text: string, parent: HTMLElement = document.body): HTMLButtonElement {
  const b = document.createElement("button")
  b.textContent = text
  parent.append(b)
  return b
}

function toaster(): HTMLElement {
  const ol = document.createElement("ol")
  ol.setAttribute("data-sonner-toaster", "")
  document.body.append(ol)
  return ol
}

// Sends an event the way Reka sends pointer-down-outside: on the target, with no bubble.
function outside(target: Element): CustomEvent {
  const e = new CustomEvent("pointer-down-outside", { bubbles: false, cancelable: true })
  target.addEventListener("pointer-down-outside", guardOutside, { once: true })
  target.dispatchEvent(e)
  return e
}

afterEach(() => {
  rememberOpener(null)
  document.body.innerHTML = ""
})

describe("hasOpenLayer", () => {
  it("is true only while a Reka layer is in the document", () => {
    expect(hasOpenLayer()).toBe(false)
    const layer = document.createElement("div")
    layer.setAttribute("data-dismissable-layer", "")
    document.body.append(layer)
    expect(hasOpenLayer()).toBe(true)
    layer.remove()
    expect(hasOpenLayer()).toBe(false)
  })
})

describe("fromToaster", () => {
  it("is true for a target inside the toaster", () => {
    const close = button("✕", toaster())
    expect(fromToaster(close)).toBe(true)
    expect(fromToaster(close.firstChild)).toBe(true)
  })

  it("is false for other targets", () => {
    toaster()
    expect(fromToaster(button("Save"))).toBe(false)
    expect(fromToaster(null)).toBe(false)
    expect(fromToaster(window)).toBe(false)
  })
})

describe("guardOutside", () => {
  it("stops a modal from closing on a tap on a toast", () => {
    expect(outside(button("Undo", toaster())).defaultPrevented).toBe(true)
  })

  it("lets a tap outside the toaster close the modal", () => {
    expect(outside(button("Page")).defaultPrevented).toBe(false)
  })
})

// Runs useReturnFocus in its own effect scope.
function setup() {
  const open = ref(false)
  const scope = effectScope()
  const api = scope.run(() => useReturnFocus(open))!
  return { open, api, stop: () => scope.stop() }
}
const closeEvent = () => new Event("close-auto-focus", { cancelable: true })

describe("useReturnFocus", () => {
  it("focuses the element that had the focus at open", () => {
    const { open, api, stop } = setup()
    const opener = button("Delete…")
    opener.focus()
    open.value = true
    button("Cancel").focus()
    open.value = false
    const e = closeEvent()
    api.onCloseAutoFocus(e)
    expect(document.activeElement).toBe(opener)
    expect(e.defaultPrevented).toBe(true)
    stop()
  })

  it("focuses the remembered opener when that element is gone", () => {
    const { open, api, stop } = setup()
    const trigger = button("More for nightly")
    const item = button("Delete…")
    item.focus()
    rememberOpener(trigger)
    open.value = true
    item.remove()
    open.value = false
    api.onCloseAutoFocus(closeEvent())
    expect(document.activeElement).toBe(trigger)
    stop()
  })

  it("uses a remembered opener once", () => {
    const { open, api, stop } = setup()
    const trigger = button("More for nightly")
    rememberOpener(trigger)
    for (let i = 0; i < 2; i++) {
      const item = button("Row")
      item.focus()
      open.value = true
      item.remove()
      open.value = false
      api.onCloseAutoFocus(closeEvent())
    }
    trigger.blur()
    const item = button("Row")
    item.focus()
    open.value = true
    item.remove()
    open.value = false
    const e = closeEvent()
    api.onCloseAutoFocus(e)
    expect(document.activeElement).not.toBe(trigger)
    expect(e.defaultPrevented).toBe(false)
    stop()
  })

  it("lets Reka act when no target exists", () => {
    const { open, api, stop } = setup()
    const item = button("Row")
    item.focus()
    open.value = true
    item.remove()
    open.value = false
    const e = closeEvent()
    api.onCloseAutoFocus(e)
    expect(e.defaultPrevented).toBe(false)
    stop()
  })
})
