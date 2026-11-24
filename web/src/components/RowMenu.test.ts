import { describe, it, expect, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import RowMenu from "./RowMenu.vue"

const items = [
  { label: "Rename", act: "rename" },
  { label: "Delete…", act: "delete", danger: true },
]
const sheet = () => document.body.querySelector(".row-menu") as HTMLElement | null

afterEach(() => {
  document.body.innerHTML = ""
})

describe("RowMenu", () => {
  it("opens a popover outside the table and emits the action", async () => {
    const w = mount(RowMenu, { props: { items }, attachTo: document.body })
    const trigger = w.find("button[aria-label='More actions']")
    expect(trigger.attributes("aria-expanded")).toBe("false")
    await trigger.trigger("click")
    expect(trigger.attributes("aria-expanded")).toBe("true")
    // The popover is a child of body, so .table-wrap cannot cut it.
    expect(sheet()?.parentElement).toBe(document.body)
    const del = [...sheet()!.querySelectorAll("button")].find((b) => b.textContent === "Delete…")!
    expect(del.classList.contains("danger")).toBe(true)
    del.click()
    await flushPromises()
    expect(w.emitted("select")).toEqual([["delete"]])
    expect(sheet()).toBeNull()
    w.unmount()
  })

  it("closes on Esc and on a tap outside", async () => {
    const w = mount(RowMenu, { props: { items }, attachTo: document.body })
    await w.find("button[aria-label='More actions']").trigger("click")
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))
    await flushPromises()
    expect(sheet()).toBeNull()
    await w.find("button[aria-label='More actions']").trigger("click")
    document.body.dispatchEvent(new Event("pointerdown", { bubbles: true }))
    await flushPromises()
    expect(sheet()).toBeNull()
    w.unmount()
  })

  it("focuses the first item and moves with the arrow keys", async () => {
    const w = mount(RowMenu, { props: { items }, attachTo: document.body })
    await w.find("button[aria-label='More actions']").trigger("click")
    await flushPromises()
    const its = [...sheet()!.querySelectorAll<HTMLElement>("[role='menuitem']")]
    expect(document.activeElement).toBe(its[0])
    sheet()!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }))
    expect(document.activeElement).toBe(its[1])
    sheet()!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }))
    expect(document.activeElement).toBe(its[0])
    sheet()!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }))
    expect(document.activeElement).toBe(its[1])
    w.unmount()
  })

  it("closes when Tab leaves the menu", async () => {
    const w = mount(RowMenu, { props: { items }, attachTo: document.body })
    await w.find("button[aria-label='More actions']").trigger("click")
    await flushPromises()
    sheet()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }))
    await flushPromises()
    expect(sheet()).toBeNull()
    w.unmount()
  })

  it("closes on a scroll or a resize", async () => {
    const w = mount(RowMenu, { props: { items }, attachTo: document.body })
    await w.find("button[aria-label='More actions']").trigger("click")
    window.dispatchEvent(new Event("scroll"))
    await flushPromises()
    expect(sheet()).toBeNull()
    await w.find("button[aria-label='More actions']").trigger("click")
    window.dispatchEvent(new Event("resize"))
    await flushPromises()
    expect(sheet()).toBeNull()
    w.unmount()
  })
})
