import { fireEvent, render, screen, waitFor, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h, nextTick, provide } from "vue"
import { afterEach, describe, expect, it, vi } from "vitest"
import PanelFrame from "./PanelFrame.vue"

const DOCK = "(min-width: 1440px)"
const PHONE = "(max-width: 599px)"

// Stubs matchMedia. set() changes the answers and calls each change listener.
function media(start: { phone?: boolean; dock?: boolean }) {
  const state = { phone: false, dock: false, ...start }
  const fns = new Map<string, ((e: { matches: boolean }) => void)[]>()
  const matches = (q: string) => (q === PHONE && state.phone) || (q === DOCK && state.dock)
  vi.stubGlobal("matchMedia", (q: string) => ({
    get matches() {
      return matches(q)
    },
    media: q,
    addEventListener: (_t: string, f: (e: { matches: boolean }) => void) =>
      fns.set(q, [...(fns.get(q) ?? []), f]),
    removeEventListener: () => {},
  }))
  return {
    async set(next: { phone?: boolean; dock?: boolean }) {
      Object.assign(state, { phone: false, dock: false }, next)
      for (const [q, list] of fns) for (const f of list) f({ matches: matches(q) })
      await nextTick()
    },
  }
}

const props = { open: true, title: "Edit job", dockQuery: DOCK }
const panel = () => document.querySelector<HTMLElement>("[data-mode]")!
const setup = () => userEvent.setup({ pointerEventsCheck: 0 })

afterEach(() => vi.unstubAllGlobals())

// Drags the grabber of the sheet from y 100 to the given y.
async function drag(to: number) {
  await fireEvent.mouseDown(document.querySelector<HTMLElement>('[data-slot="panel-grabber"]')!, {
    clientY: 100,
  })
  window.dispatchEvent(new MouseEvent("mousemove", { clientY: to }))
  window.dispatchEvent(new MouseEvent("mouseup"))
  await nextTick()
}

describe("PanelFrame", () => {
  it("takes its mode from the media queries", async () => {
    const m = media({})
    render(PanelFrame, { props })
    expect(panel().dataset.mode).toBe("modal")
    await m.set({ phone: true })
    expect(panel().dataset.mode).toBe("sheet")
    await m.set({ dock: true })
    expect(panel().dataset.mode).toBe("docked")
    await m.set({})
    expect(panel().dataset.mode).toBe("modal")
  })

  it("keeps the value of a text input across a mode change", async () => {
    const m = media({})
    const user = setup()
    render(PanelFrame, { props, slots: { default: '<input aria-label="Name" />' } })
    await user.type(screen.getByLabelText("Name"), "nightly")
    await m.set({ dock: true })
    await m.set({ phone: true })
    await m.set({})
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("nightly")
  })

  it("emits request-close on Esc only when no Reka layer is open", async () => {
    media({})
    const user = setup()
    const { emitted } = render(PanelFrame, { props })
    const layer = document.createElement("div")
    layer.setAttribute("data-dismissable-layer", "")
    document.body.append(layer)
    await user.keyboard("{Escape}")
    expect(emitted()["request-close"]).toBeUndefined()
    layer.remove()
    await user.keyboard("{Escape}")
    expect(emitted()["request-close"]).toHaveLength(1)
  })

  it("emits request-close on the close button and on the scrim, not on a toast", async () => {
    media({})
    const user = setup()
    const { emitted } = render(PanelFrame, { props })
    await user.click(screen.getByRole("button", { name: "Close" }))
    await user.click(document.querySelector<HTMLElement>('[data-slot="panel-scrim"]')!)
    expect(emitted()["request-close"]).toHaveLength(2)
    const toaster = document.createElement("div")
    toaster.setAttribute("data-sonner-toaster", "")
    toaster.innerHTML = "<button>Undo</button>"
    document.body.append(toaster)
    await user.click(within(toaster).getByRole("button", { name: "Undo" }))
    expect(emitted()["request-close"]).toHaveLength(2)
    toaster.remove()
  })

  it("closes on a drag of more than 70 px in the sheet mode only", async () => {
    const m = media({ phone: true })
    const { emitted } = render(PanelFrame, { props })
    await drag(150)
    expect(emitted()["request-close"]).toBeUndefined()
    await drag(171)
    expect(emitted()["request-close"]).toHaveLength(1)
    await m.set({})
    expect(document.querySelector('[data-slot="panel-grabber"]')).toBeNull()
    await fireEvent.mouseDown(screen.getByText("Edit job"), { clientY: 100 })
    window.dispatchEvent(new MouseEvent("mousemove", { clientY: 300 }))
    window.dispatchEvent(new MouseEvent("mouseup"))
    expect(emitted()["request-close"]).toHaveLength(1)
  })

  it("is a modal dialog that traps the focus in the modal mode", async () => {
    media({})
    const user = setup()
    const outside = document.createElement("button")
    outside.textContent = "Outside"
    document.body.append(outside)
    const { rerender } = render(PanelFrame, {
      props: { ...props, open: false },
      slots: { default: '<input aria-label="Name" />' },
    })
    await rerender({ open: true })
    const dialog = screen.getByRole("dialog", { name: "Edit job" })
    expect(dialog.getAttribute("aria-modal")).toBe("true")
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))
    screen.getByLabelText("Name").focus()
    await user.tab()
    expect(dialog.contains(document.activeElement)).toBe(true)
    outside.focus()
    expect(dialog.contains(document.activeElement)).toBe(true)
    outside.remove()
  })

  it("gives the focus back to the opener on close", async () => {
    media({})
    const opener = document.createElement("button")
    document.body.append(opener)
    opener.focus()
    const { rerender } = render(PanelFrame, {
      props: { ...props, open: false },
      slots: { default: '<input aria-label="Name" />' },
    })
    await rerender({ open: true })
    await waitFor(() => expect(panel().contains(document.activeElement)).toBe(true))
    await rerender({ open: false })
    await waitFor(() => expect(document.activeElement).toBe(opener))
    opener.remove()
  })

  it("is not modal and goes into the aside slot when docked", async () => {
    media({ dock: true })
    const Host = defineComponent({
      setup() {
        provide("asideSlot", true)
        return () => [h(PanelFrame, props, () => h("p", "Body")), h("div", { class: "aside-slot" })]
      },
    })
    render(Host)
    await nextTick()
    const docked = document.querySelector<HTMLElement>(".aside-slot [data-mode='docked']")
    expect(docked).not.toBeNull()
    expect(docked!.hasAttribute("aria-modal")).toBe(false)
    expect(within(docked!).getByText("Body")).toBeTruthy()
  })

  it("keeps the focus in the panel when the panel leaves the aside slot", async () => {
    const m = media({ dock: true })
    const Host = defineComponent({
      setup() {
        provide("asideSlot", true)
        return () =>
          h("div", [
            h(PanelFrame, props, () => h("input", { "aria-label": "Name" })),
            h("div", { class: "aside-slot" }),
          ])
      },
    })
    render(Host)
    await nextTick()
    const name = screen.getByLabelText("Name")
    name.focus()
    await m.set({})
    expect(panel().dataset.mode).toBe("modal")
    await waitFor(() => expect(document.activeElement).toBe(name))
  })

  it("stays in place when docked with no aside slot", async () => {
    media({ dock: true })
    const { container } = render(PanelFrame, { props })
    expect(container.contains(panel())).toBe(true)
    expect(panel().dataset.mode).toBe("docked")
  })

  it("keeps the content mounted while closed", async () => {
    media({})
    const { rerender } = render(PanelFrame, {
      props: { ...props, open: false },
      slots: { default: '<input aria-label="Name" />' },
    })
    expect(screen.getByLabelText("Name")).toBeTruthy()
    expect(panel().hasAttribute("inert")).toBe(true)
    await rerender({ open: true })
    expect(panel().hasAttribute("inert")).toBe(false)
  })

  it("gives the bottom sheet the safe-area padding, a height limit, and a scroll body", () => {
    media({ phone: true })
    render(PanelFrame, { props })
    expect(panel().className).toContain("max-h-[85dvh]")
    expect(panel().className).toContain("pb-[env(safe-area-inset-bottom)]")
    const body = panel().querySelector<HTMLElement>('[data-slot="panel-body"]')!
    expect(body.className).toContain("overflow-y-auto")
  })
})
