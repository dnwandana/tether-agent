import { mount } from "@vue/test-utils"
import { afterEach, describe, expect, it, vi } from "vitest"
import SidePanel from "./SidePanel.vue"

function phone(on: boolean) {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: on && q === "(max-width: 599px)",
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

afterEach(() => {
  vi.unstubAllGlobals()
  document.body.innerHTML = ""
})

describe("SidePanel", () => {
  it("shows the title and the slot when open", () => {
    const w = mount(SidePanel, {
      props: { open: true, title: "Turn details" },
      slots: { default: '<p class="body">Call ID</p>' },
    })
    expect(w.find(".aside").classes()).toContain("is-open")
    expect(w.find(".aside-scrim").classes()).toContain("is-open")
    expect(w.find(".aside-h").text()).toContain("Turn details")
    expect(w.find(".aside-b .body").exists()).toBe(true)
  })

  it("is closed when not open", () => {
    const w = mount(SidePanel, { props: { open: false, title: "Turn details" } })
    expect(w.find(".aside").classes()).not.toContain("is-open")
  })

  it("closes on Esc, on the scrim, and on the close button", async () => {
    const w = mount(SidePanel, {
      props: { open: true, title: "Turn details" },
      attachTo: document.body,
    })
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))
    await w.find(".aside-scrim").trigger("click")
    await w.find('[aria-label="Close"]').trigger("click")
    expect(w.emitted("close")).toHaveLength(3)
  })

  it("leaves Esc to a dialog on top", () => {
    const ov = document.createElement("div")
    ov.className = "ov is-open"
    document.body.appendChild(ov)
    const w = mount(SidePanel, {
      props: { open: true, title: "Turn details" },
      attachTo: document.body,
    })
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))
    expect(w.emitted("close")).toBeUndefined()
  })

  it("closes on a drag down of the grabber on a phone", async () => {
    phone(true)
    const w = mount(SidePanel, {
      props: { open: true, title: "Turn details" },
      attachTo: document.body,
    })
    const g = w.find(".grabber")
    const drag = async (to: number) => {
      await g.trigger("mousedown", { clientY: 100 })
      window.dispatchEvent(new MouseEvent("mousemove", { clientY: to }))
      window.dispatchEvent(new MouseEvent("mouseup"))
    }
    await drag(150)
    expect(w.emitted("close")).toBeUndefined()
    await drag(171)
    expect(w.emitted("close")).toHaveLength(1)
  })

  it("does not drag on a wide screen", async () => {
    phone(false)
    const w = mount(SidePanel, {
      props: { open: true, title: "Turn details" },
      attachTo: document.body,
    })
    await w.find(".grabber").trigger("mousedown", { clientY: 100 })
    window.dispatchEvent(new MouseEvent("mousemove", { clientY: 300 }))
    window.dispatchEvent(new MouseEvent("mouseup"))
    expect(w.emitted("close")).toBeUndefined()
  })
})
