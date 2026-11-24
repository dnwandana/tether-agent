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

  it("starts on the button that focus names", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    const p = confirm({
      title: "Approve 2 commands once?",
      body: "Each command runs one time.",
      confirmLabel: "✅ Approve all once",
      focus: "cancel",
    })
    await w.vm.$nextTick()
    await w.vm.$nextTick()
    expect(w.findAll(".dialog-f button")[1].classes()).toContain("btn-primary")
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel")
    pendingConfirm.resolve?.(false)
    await p
    const q = confirm({ title: "Restart?", body: "", confirmLabel: "Restart" })
    await w.vm.$nextTick()
    await w.vm.$nextTick()
    expect(document.activeElement?.textContent?.trim()).toBe("Restart")
    pendingConfirm.resolve?.(false)
    await q
  })

  it("shows the items as typed, one row each", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    confirm({
      title: "Approve 3 commands once?",
      body: "",
      confirmLabel: "✅ Approve all once",
      items: ["![a](b)", "[](x)", "rm *.log *.tmp"],
    })
    await w.vm.$nextTick()
    const rows = w.findAll(".confirm-items li")
    expect(rows.map((r) => r.text())).toEqual(["![a](b)", "[](x)", "rm *.log *.tmp"])
    expect(w.find(".confirm-items img").exists()).toBe(false)
    expect(w.find(".confirm-items a").exists()).toBe(false)
    expect(w.find(".confirm-items em").exists()).toBe(false)
  })

  it("shows the title and the body as typed with no markdown flag", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    confirm({
      title: "Remove **x**?",
      body: "![a](b) and [](x)",
      confirmLabel: "Remove",
      danger: true,
    })
    await w.vm.$nextTick()
    expect(w.find("h3").text()).toBe("Remove **x**?")
    expect(w.find(".confirm-body").text()).toBe("![a](b) and [](x)")
    expect(w.find(".dialog-b strong").exists()).toBe(false)
  })

  it("renders Markdown in the title and the body of a gateway reply", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    confirm({
      title: 'Delete **"Old"**?',
      body: "The session has **12** messages.",
      confirmLabel: "🗑 Delete",
      danger: true,
      markdown: true,
    })
    await w.vm.$nextTick()
    expect(w.find("h3 strong").text()).toBe('"Old"')
    expect(w.find(".confirm-body strong").text()).toBe("12")
  })

  it("describes the dialog with the body and traps Tab", async () => {
    const w = mount(ConfirmDialog, { attachTo: document.body })
    confirm({ title: "Restart?", body: "The chat stops for 10 s.", confirmLabel: "Restart" })
    await w.vm.$nextTick()
    await w.vm.$nextTick()
    const dlg = w.find(".dialog")
    const id = dlg.attributes("aria-describedby")
    expect(id).toBeTruthy()
    expect(document.getElementById(id!)?.textContent).toContain("The chat stops for 10 s.")
    const e = new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true })
    document.activeElement?.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(true)
    expect(document.activeElement?.textContent?.trim()).toBe("Cancel")
  })

  it("gives the focus back to the opener", async () => {
    const opener = document.createElement("button")
    document.body.appendChild(opener)
    opener.focus()
    const w = mount(ConfirmDialog, { attachTo: document.body })
    const p = confirm({ title: "Restart?", body: "", confirmLabel: "Restart" })
    await w.vm.$nextTick()
    await w.find(".dialog-f .btn-primary").trigger("click")
    await p
    await w.vm.$nextTick()
    await w.vm.$nextTick()
    expect(document.activeElement).toBe(opener)
  })
})
