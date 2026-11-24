import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import RecordLine from "./RecordLine.vue"

const record = (header: string) => ({
  id: "c1",
  kind: "approval",
  header,
  preview: "rm -rf build/ && make release VERSION=0.25.0",
  session_id: "s1",
  at: new Date(2026, 10, 23, 9, 5).toISOString(),
})
const stubs = {
  RouterLink: {
    props: ["to"],
    template: '<a class="rl" :data-to="JSON.stringify(to)"><slot /></a>',
  },
}

const color = (h: string) =>
  mount(RecordLine, { props: { record: record(h) }, global: { stubs } })
    .find(".rec-h")
    .classes()

describe("RecordLine", () => {
  it("shows the header, the preview, the time, and the open mark on one line", () => {
    const w = mount(RecordLine, {
      props: { record: record("✅ Approval allowed · once") },
      global: { stubs },
    })
    expect(w.find(".collapsed").exists()).toBe(true)
    expect(w.find(".rec-h").text()).toBe("✅ Approval allowed · once")
    expect(w.find(".sum").text()).toBe("rm -rf build/ && make release VERSION=0.25.0")
    expect(w.find(".rec-t").text()).toBe("09:05")
    expect(w.find(".chev").text()).toBe("▸")
  })

  it("shows midnight as 00:05, never 24:05", () => {
    const rec = { ...record("✅ Approval allowed"), at: new Date(2026, 10, 23, 0, 5).toISOString() }
    const w = mount(RecordLine, { props: { record: rec }, global: { stubs } })
    expect(w.find(".rec-t").text()).toBe("00:05")
  })

  it("colors the header by its result", () => {
    expect(color("✅ Approval allowed · once")).toContain("c-ok")
    expect(color("❌ Approval denied")).toContain("c-bad")
    expect(color("⌛ Approval denied · no answer in 5 min")).toContain("c-warn")
    expect(color("⏹ No answer · the turn stopped")).toContain("c-warn")
  })

  it("opens a read-only view with the full preview and the approval log link", async () => {
    const w = mount(RecordLine, {
      props: { record: record("❌ Approval denied") },
      global: { stubs },
    })
    expect(w.find(".rec-detail").exists()).toBe(false)
    await w.find(".chev").trigger("click")
    expect(w.find(".chev").attributes("aria-expanded")).toBe("true")
    expect(w.find(".rec-detail .cmd").text()).toBe("rm -rf build/ && make release VERSION=0.25.0")
    expect(w.find(".rec-detail button").exists()).toBe(false)
    const link = w.find(".rec-detail .rl")
    expect(link.text()).toBe("Open the approval log")
    expect(JSON.parse(link.attributes("data-to")!)).toEqual({
      name: "security",
      query: { tab: "log", scope: "dashboard" },
    })
  })
})
