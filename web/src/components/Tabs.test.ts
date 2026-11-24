import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"
import { nextTick } from "vue"
import Tabs from "./Tabs.vue"

const tabs = [
  { id: "modes", label: "Modes" },
  { id: "grants", label: "Always answers" },
  { id: "log", label: "Approval log" },
]

// Renders the tabs. The update handler writes the new value back, the same as v-model.
function mk(value = "modes", slot = true) {
  const r = render(Tabs, {
    props: {
      tabs,
      modelValue: value,
      "onUpdate:modelValue": (v: string) => void r.rerender({ modelValue: v }),
    },
    slots: slot ? { default: "<p>content</p>" } : {},
  })
  return r
}
const tabEls = () => screen.getAllByRole("tab")
const last = (r: ReturnType<typeof mk>) => r.emitted()["update:modelValue"]?.at(-1)

describe("Tabs", () => {
  it("renders a tab list with the selected tab", async () => {
    mk("grants")
    await nextTick()
    const list = screen.getByRole("tablist")
    expect(list.className).toContain("max-tab:flex-wrap")
    expect(tabEls().map((x) => x.textContent?.trim())).toEqual([
      "Modes",
      "Always answers",
      "Approval log",
    ])
    expect(tabEls().map((x) => x.getAttribute("aria-selected"))).toEqual(["false", "true", "false"])
    // Reka makes the list the tab stop. The focus then goes to the selected tab.
    expect(list.getAttribute("tabindex")).toBe("0")
    tabEls()[1].focus()
    await nextTick()
    expect(tabEls().map((x) => x.getAttribute("tabindex"))).toEqual(["-1", "0", "-1"])
    expect(tabEls()[1].getAttribute("data-state")).toBe("active")
  })

  it("shows the count of a tab as a badge", async () => {
    const r = mk("modes")
    await r.rerender({
      tabs: [
        { id: "modes", label: "Modes" },
        { id: "grants", label: "Always", count: 0 },
      ],
    })
    expect(tabEls()[0].querySelector('[data-slot="badge"]')).toBeNull()
    expect(within(tabEls()[1]).getByText("0").getAttribute("data-slot")).toBe("badge")
  })

  it("links the selected tab and the panel", async () => {
    mk("grants")
    await nextTick()
    const tab = tabEls()[1]
    const panel = screen.getByRole("tabpanel")
    expect(tab.getAttribute("aria-controls")).toBe(panel.id)
    expect(panel.getAttribute("aria-labelledby")).toBe(tab.id)
    expect(within(panel).getByText("content")).toBeTruthy()
  })

  it("has no panel and no aria-controls with no slot", () => {
    mk("modes", false)
    expect(screen.queryByRole("tabpanel")).toBeNull()
    expect(tabEls()[0].hasAttribute("aria-controls")).toBe(false)
  })

  it("selects a tab on a click", async () => {
    const user = userEvent.setup()
    const r = mk("modes")
    await user.click(tabEls()[2])
    expect(r.emitted()["update:modelValue"]).toEqual([["log"]])
  })

  it("moves with the arrow keys, Home, and End, and moves the focus", async () => {
    const user = userEvent.setup()
    const r = mk("modes")
    tabEls()[0].focus()
    await user.keyboard("{ArrowRight}")
    expect(last(r)).toEqual(["grants"])
    expect(document.activeElement).toBe(tabEls()[1])
    await user.keyboard("{ArrowLeft}")
    expect(last(r)).toEqual(["modes"])
    await user.keyboard("{ArrowLeft}")
    expect(last(r)).toEqual(["log"])
    await user.keyboard("{Home}")
    expect(last(r)).toEqual(["modes"])
    await user.keyboard("{End}")
    expect(last(r)).toEqual(["log"])
  })
})
