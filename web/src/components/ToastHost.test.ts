import { mount } from "@vue/test-utils"
import { afterEach, describe, expect, it, vi } from "vitest"

vi.mock("../api/client", () => ({
  runCommand: vi.fn(async () => ({
    request_id: "r1",
    text: "🗄 Unarchived",
    buttons: [],
    edit: false,
  })),
}))
import { runCommand } from "../api/client"
import ToastHost from "./ToastHost.vue"
import { toast, toasts } from "../stores/ui"

afterEach(() => {
  toasts.splice(0)
})

describe("ToastHost", () => {
  it("shows the toast and closes it", async () => {
    const w = mount(ToastHost)
    toast("Saved. Applies to new sessions.", "ok")
    await w.vm.$nextTick()
    expect(w.find('.toasts .toast[role="status"]').text()).toContain(
      "Saved. Applies to new sessions.",
    )
    expect(w.find(".toast .t-ok").text()).toBe("✓")
    await w.find('.toast [aria-label="Close"]').trigger("click")
    expect(toasts).toHaveLength(0)
  })

  it("marks an error toast as an alert", async () => {
    const w = mount(ToastHost)
    toast("The gateway is stopping.", "error")
    await w.vm.$nextTick()
    expect(w.find('.toast[role="alert"]').text()).toContain("⚠")
    expect(w.find(".toast").classes()).toContain("toast-error")
  })

  it("renders Markdown only for reply text", async () => {
    const w = mount(ToastHost)
    toast("**Saved** `a_b`", "ok", { markdown: true })
    toast("**name** of a device")
    await w.vm.$nextTick()
    const items = w.findAll(".toast")
    expect(items[0].find("strong").text()).toBe("Saved")
    expect(items[1].text()).toContain("**name** of a device")
  })

  it("runs an action and closes the toast", async () => {
    const w = mount(ToastHost)
    toast("🗄 Archived", "ok", { actions: [{ label: "↩️ Undo", data: "/unarchive abc" }] })
    await w.vm.$nextTick()
    const undo = w.findAll(".toast button").find((b) => b.text() === "↩️ Undo")!
    await undo.trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/unarchive abc")
    expect(toasts.map((t) => t.text)).not.toContain("🗄 Archived")
  })

  it("shows +N more when more than 3 sticky toasts exist", async () => {
    const w = mount(ToastHost)
    for (const t of ["A", "B", "C", "D"]) toast(t, "error")
    await w.vm.$nextTick()
    expect(w.findAll(".toast[role]")).toHaveLength(2)
    const more = w.find(".toast-more")
    expect(more.text()).toBe("+2 more")
    await more.trigger("click")
    expect(w.findAll(".toast[role]")).toHaveLength(4)
  })
})
