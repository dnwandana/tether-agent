import { mount } from "@vue/test-utils"
import { afterEach, describe, expect, it } from "vitest"
import ConfirmDialog from "./ConfirmDialog.vue"
import { confirm, pendingConfirm } from "../stores/ui"

afterEach(() => {
  pendingConfirm.resolve?.(false)
  document.body.innerHTML = ""
})

describe("ConfirmDialog", () => {
  it("shows the open confirm and answers true", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    const p = confirm({
      title: "Approve 2 commands once?",
      body: "rm -rf build/\ngo test ./...",
      confirmLabel: "✅ Approve all once",
    })
    await w.vm.$nextTick()
    expect(w.find(".ov.scrim.is-open .dialog").exists()).toBe(true)
    expect(w.find("h3").text()).toBe("Approve 2 commands once?")
    expect(w.find(".dialog-b").text()).toContain("rm -rf build/")
    const buttons = w.findAll(".dialog-f button")
    expect(buttons.map((b) => b.text())).toEqual(["Cancel", "✅ Approve all once"])
    expect(buttons[1].classes()).toContain("btn-primary")
    await buttons[1].trigger("click")
    await expect(p).resolves.toBe(true)
    expect(w.find(".dialog").exists()).toBe(false)
  })

  it("answers false on Esc and uses the danger button", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    const p = confirm({ title: "Delete the job?", body: "", confirmLabel: "Delete", danger: true })
    await w.vm.$nextTick()
    expect(w.findAll(".dialog-f button")[1].classes()).toContain("btn-danger")
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel")
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))
    await expect(p).resolves.toBe(false)
  })

  it("answers false on a scrim tap", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    const p = confirm({ title: "Restart?", body: "", confirmLabel: "Restart" })
    await w.vm.$nextTick()
    await w.find(".ov").trigger("click")
    await expect(p).resolves.toBe(false)
  })
})
