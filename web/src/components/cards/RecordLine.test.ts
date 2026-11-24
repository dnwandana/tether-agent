import { describe, expect, it } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { createMemoryHistory, createRouter } from "vue-router"
import RecordLine from "./RecordLine.vue"

const record = (header: string) => ({
  id: "c1",
  kind: "approval",
  header,
  preview: "rm -rf build/ && make release VERSION=0.25.0",
  session_id: "s1",
  at: new Date(2026, 10, 23, 9, 5).toISOString(),
})
const PREVIEW = "rm -rf build/ && make release VERSION=0.25.0"
const Blank = { render: () => null }
function mk(rec: ReturnType<typeof record>) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", component: Blank },
      { path: "/security", name: "security", component: Blank },
    ],
  })
  return render(RecordLine, { props: { record: rec }, global: { plugins: [router] } })
}
const tone = (h: string) => {
  const r = mk(record(h))
  const line = r.container.querySelector('[data-slot="trace-line"]')!
  const out = [line.getAttribute("data-tone"), line.getAttribute("data-status")]
  r.unmount()
  return out
}

describe("RecordLine", () => {
  it("shows the header, the preview, the time, and the open mark on one line", () => {
    mk(record("✅ Approval allowed · once"))
    const trigger = screen.getByRole("button")
    expect(within(trigger).getByText("✅ Approval allowed · once")).toBeTruthy()
    expect(within(trigger).getByText(PREVIEW)).toBeTruthy()
    expect(within(trigger).getByText("09:05")).toBeTruthy()
    expect(trigger.querySelector('[data-slot="trace-icon"] svg')).not.toBeNull()
    expect(trigger.getAttribute("aria-expanded")).toBe("false")
  })

  it("shows midnight as 00:05, never 24:05", () => {
    mk({ ...record("✅ Approval allowed"), at: new Date(2026, 10, 23, 0, 5).toISOString() })
    expect(screen.getByText("00:05")).toBeTruthy()
  })

  it("colors the header by its result", () => {
    expect(tone("✅ Approval allowed · once")).toEqual(["ok", null])
    expect(tone("❌ Approval denied")).toEqual(["bad", "error"])
    expect(tone("⌛ Approval denied · no answer in 5 min")).toEqual(["warn", null])
    expect(tone("⏹ No answer · the turn stopped")).toEqual(["warn", null])
  })

  it("opens a read-only view with the full preview and the approval log link", async () => {
    const r = mk(record("❌ Approval denied"))
    const trigger = screen.getByRole("button")
    expect(r.container.querySelector('[data-slot="trace-collapsible-content"]')).toBeNull()
    await userEvent.setup().click(trigger)
    expect(trigger.getAttribute("aria-expanded")).toBe("true")
    const detail = r.container.querySelector(
      '[data-slot="trace-collapsible-content"]',
    ) as HTMLElement
    expect(within(detail).getByText(PREVIEW)).toBeTruthy()
    expect(within(detail).queryAllByRole("button")).toHaveLength(0)
    const link = within(detail).getByRole("link", { name: "Open the approval log" })
    expect(link.getAttribute("href")).toBe("/security?tab=log&scope=dashboard")
  })
})
