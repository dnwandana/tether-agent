import { defineComponent, h, provide, ref } from "vue"
import { flushPromises, mount } from "@vue/test-utils"
import { afterEach, describe, expect, it, vi } from "vitest"
import { useOverlay } from "../lib/overlay"
import SidePanel from "./SidePanel.vue"

function media(q: { phone?: boolean; wide?: boolean }) {
  vi.stubGlobal("matchMedia", (s: string) => ({
    matches:
      (q.phone === true && s === "(max-width: 599px)") ||
      (q.wide === true && s === "(min-width: 1024px)"),
    media: s,
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

  it("closes only the top overlay on Esc", async () => {
    const w = mount(SidePanel, {
      props: { open: true, title: "Turn details" },
      attachTo: document.body,
    })
    const top = ref(false)
    const onClose = vi.fn()
    mount(
      defineComponent({
        setup() {
          const el = ref<HTMLElement | null>(null)
          useOverlay(top, el, { onClose, modal: true })
          return () => h("div", { ref: el }, [h("button", "x")])
        },
      }),
      { attachTo: document.body },
    )
    top.value = true
    await flushPromises()
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    )
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(w.emitted("close")).toBeUndefined()
  })

  it("renders into the aside slot of the shell", async () => {
    const Host = defineComponent({
      setup() {
        provide("asideSlot", true)
        return () => [
          h(SidePanel, { open: true, title: "Edit job" }, () => h("p", { class: "body" }, "x")),
          h("div", { class: "aside-slot" }),
        ]
      },
    })
    mount(Host, { attachTo: document.body })
    await flushPromises()
    expect(document.querySelector(".aside-slot > aside.aside .body")).not.toBeNull()
  })

  it("is inert when closed", () => {
    const w = mount(SidePanel, { props: { open: false, title: "Edit job" } })
    expect(w.find("aside").attributes("inert")).toBeDefined()
    const o = mount(SidePanel, { props: { open: true, title: "Edit job" } })
    expect(o.find("aside").attributes("inert")).toBeUndefined()
  })

  it("is a modal dialog below 1024 px and a column at 1024 px and wider", () => {
    media({})
    const narrow = mount(SidePanel, { props: { open: true, title: "Edit job" } })
    expect(narrow.find("aside").attributes("role")).toBe("dialog")
    expect(narrow.find("aside").attributes("aria-modal")).toBe("true")
    media({ wide: true })
    const wide = mount(SidePanel, { props: { open: true, title: "Edit job" } })
    expect(wide.find("aside").attributes("aria-modal")).toBeUndefined()
  })

  it("moves the focus in on open and back on close below 1024 px", async () => {
    media({})
    const opener = document.createElement("button")
    document.body.appendChild(opener)
    opener.focus()
    const w = mount(SidePanel, {
      props: { open: false, title: "Edit job" },
      slots: { default: '<input class="first" />' },
      attachTo: document.body,
    })
    await w.setProps({ open: true })
    await flushPromises()
    expect(w.find("aside").element.contains(document.activeElement)).toBe(true)
    await w.setProps({ open: false })
    await flushPromises()
    expect(document.activeElement).toBe(opener)
  })

  it("reads the modal mode again at each open after a resize", async () => {
    media({ wide: true })
    const opener = document.createElement("button")
    document.body.appendChild(opener)
    opener.focus()
    const w = mount(SidePanel, {
      props: { open: false, title: "Edit job" },
      slots: { default: '<input class="first" />' },
      attachTo: document.body,
    })
    await w.setProps({ open: true })
    await flushPromises()
    // A column on a wide screen does not take the focus.
    expect(document.activeElement).toBe(opener)
    await w.setProps({ open: false })
    await flushPromises()
    media({})
    await w.setProps({ open: true })
    await flushPromises()
    expect(w.find("aside").element.contains(document.activeElement)).toBe(true)
  })

  it("asks before it discards the changes of a dirty panel", async () => {
    const w = mount(SidePanel, {
      props: { open: true, title: "Edit job", dirty: true },
      attachTo: document.body,
    })
    await w.find('[aria-label="Close"]').trigger("click")
    expect(w.emitted("close")).toBeUndefined()
    expect(w.find(".discard").text()).toContain("Discard the changes?")
    await w
      .findAll(".discard button")
      .find((b) => b.text() === "Keep editing")!
      .trigger("click")
    expect(w.find(".discard").exists()).toBe(false)
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    )
    await flushPromises()
    expect(w.find(".discard").exists()).toBe(true)
    await w.find(".aside-scrim").trigger("click")
    expect(w.emitted("close")).toBeUndefined()
    const discard = w.findAll(".discard button").find((b) => b.text() === "Discard")!
    expect(discard.classes()).toContain("btn-danger-soft")
    await discard.trigger("click")
    expect(w.emitted("close")).toHaveLength(1)
  })

  it("tells the page that a side panel opened", async () => {
    const seen = vi.fn()
    document.addEventListener("tether:panel-open", seen)
    const w = mount(SidePanel, { props: { open: false, title: "Edit job" } })
    await w.setProps({ open: true })
    expect(seen).toHaveBeenCalledTimes(1)
    document.removeEventListener("tether:panel-open", seen)
  })

  it("closes on a drag down of the grabber on a phone", async () => {
    media({ phone: true })
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
    media({})
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
