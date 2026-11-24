import { flushPromises } from "@vue/test-utils"
import { fireEvent, render, screen, waitFor, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { defineComponent, h, nextTick, provide } from "vue"
import { afterEach, describe, expect, it, vi } from "vitest"
import { createMemoryHistory, createRouter } from "vue-router"

const gets: Record<string, unknown> = {
  "/api/models": {
    entries: [{ name: "primary", model: "glm-4.6" }],
    chain: [],
    health: [],
    roles: [],
  },
  "/api/skills": [{ name: "deploy" }, { name: "notes" }],
  "/api/bootstrap": { toolsets: ["web", "terminal", "file"], commands: [], skills: [] },
  "/api/brief": { default_deliver: "telegram:555" },
}
vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: vi.fn(async (_m: string, p: string) => gets[p]),
  runCommand: vi.fn(),
}))

import SidePanel from "./SidePanel.vue"
import JobForm from "./JobForm.vue"
import CommandPalette from "./CommandPalette.vue"
import AppToaster from "./AppToaster.vue"
import { toast } from "../stores/ui"

const WIDE = "(min-width: 1440px)"
const PHONE = "(max-width: 599px)"

// Stubs matchMedia. set() changes the answers and calls each change listener.
function media(start: { phone?: boolean; wide?: boolean }) {
  const state = { phone: false, wide: false, ...start }
  const fns = new Map<string, ((e: { matches: boolean }) => void)[]>()
  const matches = (q: string) => (q === PHONE && state.phone) || (q === WIDE && state.wide)
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
    async set(next: { phone?: boolean; wide?: boolean }) {
      Object.assign(state, { phone: false, wide: false }, next)
      for (const [q, list] of fns) for (const f of list) f({ matches: matches(q) })
      await nextTick()
    },
  }
}

const setup = () => userEvent.setup({ pointerEventsCheck: 0 })
const frame = () => document.querySelector<HTMLElement>("[data-mode]")!
const scrim = () => document.querySelector<HTMLElement>('[data-slot="panel-scrim"]')!

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

describe("SidePanel", () => {
  it("shows the title and the slot when open", () => {
    media({})
    render(SidePanel, {
      props: { open: true, title: "Turn details" },
      slots: { default: "<p>Call ID</p>" },
    })
    const dialog = screen.getByRole("dialog", { name: "Turn details" })
    expect(within(dialog).getByText("Turn details")).toBeTruthy()
    expect(within(dialog).getByText("Call ID")).toBeTruthy()
  })

  it("is closed when not open", () => {
    media({})
    render(SidePanel, { props: { open: false, title: "Turn details" } })
    expect(frame().hasAttribute("inert")).toBe(true)
    expect(scrim()?.offsetParent ?? null).toBeNull()
  })

  it("closes on Esc, on the scrim, and on the close button", async () => {
    media({})
    const user = setup()
    const { emitted } = render(SidePanel, { props: { open: true, title: "Turn details" } })
    await user.keyboard("{Escape}")
    await user.click(scrim())
    await user.click(screen.getByRole("button", { name: "Close" }))
    expect(emitted().close).toHaveLength(3)
  })

  it("closes only the palette on Esc over the docked panel", async () => {
    media({ wide: true })
    const user = setup()
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ name: "chat", path: "/chat", component: { render: () => null } }],
    })
    router.push("/chat")
    await router.isReady()
    const panelClose = vi.fn()
    const paletteClose = vi.fn()
    const Host = defineComponent({
      setup: () => () => [
        h(SidePanel, { open: true, title: "Edit job", onClose: panelClose }),
        h(CommandPalette, { open: true, onClose: paletteClose }),
      ],
    })
    render(Host, { global: { plugins: [router] } })
    await flushPromises()
    expect(frame().dataset.mode).toBe("docked")
    await user.keyboard("{Escape}")
    expect(paletteClose).toHaveBeenCalledTimes(1)
    expect(panelClose).not.toHaveBeenCalled()
  })

  it("renders into the aside slot of the shell", async () => {
    media({ wide: true })
    const Host = defineComponent({
      setup() {
        provide("asideSlot", true)
        return () => [
          h(SidePanel, { open: true, title: "Edit job" }, () => h("p", "Body")),
          h("div", { class: "aside-slot" }),
        ]
      },
    })
    render(Host)
    await flushPromises()
    const slot = document.querySelector<HTMLElement>(".aside-slot")!
    expect(within(slot).getByRole("dialog", { name: "Edit job" })).toBeTruthy()
    expect(within(slot).getByText("Body")).toBeTruthy()
  })

  it("is inert when closed", async () => {
    media({})
    const { rerender } = render(SidePanel, { props: { open: false, title: "Edit job" } })
    expect(frame().hasAttribute("inert")).toBe(true)
    await rerender({ open: true })
    expect(frame().hasAttribute("inert")).toBe(false)
  })

  it("is a modal dialog below 1440 px and a column at 1440 px and wider", async () => {
    const m = media({})
    render(SidePanel, { props: { open: true, title: "Edit job" } })
    expect(screen.getByRole("dialog", { name: "Edit job" }).getAttribute("aria-modal")).toBe("true")
    await m.set({ wide: true })
    expect(screen.getByRole("dialog", { name: "Edit job" }).hasAttribute("aria-modal")).toBe(false)
  })

  it("moves the focus in on open and back on close below 1440 px", async () => {
    media({})
    const opener = document.createElement("button")
    document.body.append(opener)
    opener.focus()
    const { rerender } = render(SidePanel, {
      props: { open: false, title: "Edit job" },
      slots: { default: '<input aria-label="Name" />' },
    })
    await rerender({ open: true })
    await waitFor(() => expect(frame().contains(document.activeElement)).toBe(true))
    await rerender({ open: false })
    await waitFor(() => expect(document.activeElement).toBe(opener))
    opener.remove()
  })

  it("moves the trap and aria-modal with a resize across 1440 px while open", async () => {
    const m = media({})
    const user = setup()
    const opener = document.createElement("button")
    document.body.append(opener)
    render(SidePanel, {
      props: { open: true, title: "Edit job" },
      slots: { default: '<input aria-label="Name" />' },
    })
    await flushPromises()
    expect(frame().getAttribute("aria-modal")).toBe("true")
    await m.set({ wide: true })
    expect(frame().hasAttribute("aria-modal")).toBe(false)
    opener.focus()
    expect(document.activeElement).toBe(opener)
    await m.set({})
    expect(frame().getAttribute("aria-modal")).toBe("true")
    screen.getByLabelText("Name").focus()
    await user.tab()
    expect(frame().contains(document.activeElement)).toBe(true)
    opener.focus()
    expect(frame().contains(document.activeElement)).toBe(true)
    opener.remove()
  })

  it("reads the modal mode again at each open after a resize", async () => {
    const m = media({ wide: true })
    const opener = document.createElement("button")
    document.body.append(opener)
    opener.focus()
    const { rerender } = render(SidePanel, {
      props: { open: false, title: "Edit job" },
      slots: { default: '<input aria-label="Name" />' },
    })
    await rerender({ open: true })
    await flushPromises()
    // A column on a wide screen does not take the focus.
    expect(document.activeElement).toBe(opener)
    await rerender({ open: false })
    await m.set({})
    await rerender({ open: true })
    await waitFor(() => expect(frame().contains(document.activeElement)).toBe(true))
    opener.remove()
  })

  it("asks before it discards the changes of a dirty panel", async () => {
    media({})
    const user = setup()
    const { emitted } = render(SidePanel, {
      props: { open: true, title: "Edit job", dirty: true },
    })
    await user.click(screen.getByRole("button", { name: "Close" }))
    expect(emitted().close).toBeUndefined()
    expect(screen.getByRole("alert").textContent).toContain("Discard the changes?")
    await user.click(screen.getByRole("button", { name: "Keep editing" }))
    expect(screen.queryByRole("alert")).toBeNull()
    // The focus stays in the panel after the question closes.
    await waitFor(() =>
      expect(
        screen.getByRole("dialog", { name: "Edit job" }).contains(document.activeElement),
      ).toBe(true),
    )
    await user.keyboard("{Escape}")
    expect(screen.getByRole("alert")).toBeTruthy()
    await user.click(scrim())
    expect(emitted().close).toBeUndefined()
    const discard = screen.getByRole("button", { name: "Discard" })
    expect(discard.className).toContain("bg-destructive")
    await user.click(discard)
    expect(emitted().close).toHaveLength(1)
  })

  it("tells the page that a side panel opened", async () => {
    media({})
    const seen = vi.fn()
    document.addEventListener("tether:panel-open", seen)
    const { rerender } = render(SidePanel, { props: { open: false, title: "Edit job" } })
    await rerender({ open: true })
    expect(seen).toHaveBeenCalledTimes(1)
    document.removeEventListener("tether:panel-open", seen)
  })

  it("closes on a drag down of the grabber on a phone", async () => {
    media({ phone: true })
    const { emitted } = render(SidePanel, { props: { open: true, title: "Turn details" } })
    await drag(150)
    expect(emitted().close).toBeUndefined()
    await drag(171)
    expect(emitted().close).toHaveLength(1)
  })

  it("does not drag on a wide screen", async () => {
    media({})
    const { emitted } = render(SidePanel, { props: { open: true, title: "Turn details" } })
    await fireEvent.mouseDown(screen.getByText("Turn details"), { clientY: 100 })
    window.dispatchEvent(new MouseEvent("mousemove", { clientY: 300 }))
    window.dispatchEvent(new MouseEvent("mouseup"))
    expect(emitted().close).toBeUndefined()
  })

  it("keeps the edits of a dirty JobForm across a resize over 1440 px", async () => {
    const m = media({})
    const user = setup()
    const Host = defineComponent({
      setup() {
        provide("asideSlot", true)
        return () => [
          h(SidePanel, { open: true, title: "New job", dirty: true }, () =>
            h(JobForm, { job: null }),
          ),
          h("div", { class: "aside-slot" }),
        ]
      },
    })
    render(Host)
    await flushPromises()
    await user.type(screen.getByLabelText("Name"), "nightly")
    await m.set({ wide: true })
    expect(frame().dataset.mode).toBe("docked")
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("nightly")
    await m.set({})
    expect(frame().dataset.mode).toBe("modal")
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("nightly")
  })

  it("stays open on a tap on a toast while it is modal", async () => {
    media({})
    const user = setup()
    const Host = defineComponent({
      setup: () => () => [h(SidePanel, { open: true, title: "Edit job" }), h(AppToaster)],
    })
    const { emitted } = render(Host)
    toast("Saved the job.", "info")
    await user.click(await screen.findByText("Saved the job."))
    expect(emitted().close).toBeUndefined()
    expect(frame().getAttribute("aria-modal")).toBe("true")
    expect(frame().hasAttribute("inert")).toBe(false)
  })
})
