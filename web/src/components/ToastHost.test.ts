import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import ToastHost from "./ToastHost.vue"
import { toast, toasts } from "../stores/ui"

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

  it("marks an error toast", async () => {
    const w = mount(ToastHost)
    toast("The gateway is stopping.", "error")
    await w.vm.$nextTick()
    expect(w.find(".toast").text()).toContain("⚠")
  })
})
