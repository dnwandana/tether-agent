import { mount } from "@vue/test-utils"
import { afterEach, describe, expect, it } from "vitest"
import Tabs from "./Tabs.vue"
import tabsSource from "./Tabs.vue?raw"

const tabs = [
  { id: "modes", label: "Modes" },
  { id: "grants", label: "Always answers" },
  { id: "log", label: "Approval log" },
]

// Mounts the tabs. The update handler writes the new value back, the same as v-model.
function mk(value = "modes", slot = true) {
  let w!: ReturnType<typeof mount>
  w = mount(Tabs, {
    props: {
      tabs,
      modelValue: value,
      "onUpdate:modelValue": (v: string) => void w.setProps({ modelValue: v }),
    },
    slots: slot ? { default: '<p class="body">content</p>' } : {},
    attachTo: document.body,
  })
  return w
}

afterEach(() => (document.body.innerHTML = ""))

describe("Tabs", () => {
  it("renders a tab list with the selected tab", () => {
    const w = mk("grants")
    expect(w.find(".tabs").attributes("role")).toBe("tablist")
    const t = w.findAll('[role="tab"]')
    expect(t.map((x) => x.text())).toEqual(["Modes", "Always answers", "Approval log"])
    expect(t.map((x) => x.attributes("aria-selected"))).toEqual(["false", "true", "false"])
    expect(t.map((x) => x.attributes("tabindex"))).toEqual(["-1", "0", "-1"])
    expect(t[1].classes()).toContain("is-on")
  })

  it("shows the count of a tab as a badge", () => {
    const w = mk("modes")
    w.setProps({
      tabs: [
        { id: "modes", label: "Modes" },
        { id: "grants", label: "Always", count: 0 },
      ],
    })
    return w.vm.$nextTick().then(() => {
      const t = w.findAll('[role="tab"]')
      expect(t[0].find(".badge").exists()).toBe(false)
      expect(t[1].find(".badge").text()).toBe("0")
    })
  })

  it("links the selected tab and the panel", () => {
    const w = mk("grants")
    const tab = w.findAll('[role="tab"]')[1]
    const panel = w.find('[role="tabpanel"]')
    expect(tab.attributes("aria-controls")).toBe(panel.attributes("id"))
    expect(panel.attributes("aria-labelledby")).toBe(tab.attributes("id"))
    expect(panel.find(".body").exists()).toBe(true)
  })

  it("has no panel and no aria-controls with no slot", () => {
    const w = mk("modes", false)
    expect(w.find('[role="tabpanel"]').exists()).toBe(false)
    expect(w.findAll('[role="tab"]')[0].attributes("aria-controls")).toBeUndefined()
  })

  it("selects a tab on a click", async () => {
    const w = mk("modes")
    await w.findAll('[role="tab"]')[2].trigger("click")
    expect(w.emitted("update:modelValue")).toEqual([["log"]])
  })

  it("moves with the arrow keys, Home, and End, and moves the focus", async () => {
    const w = mk("modes")
    const t = () => w.findAll('[role="tab"]')
    await t()[0].trigger("keydown", { key: "ArrowRight" })
    expect(w.emitted("update:modelValue")!.at(-1)).toEqual(["grants"])
    expect(document.activeElement).toBe(t()[1].element)
    await t()[1].trigger("keydown", { key: "ArrowLeft" })
    expect(w.emitted("update:modelValue")!.at(-1)).toEqual(["modes"])
    await t()[0].trigger("keydown", { key: "ArrowLeft" })
    expect(w.emitted("update:modelValue")!.at(-1)).toEqual(["log"])
    await t()[2].trigger("keydown", { key: "Home" })
    expect(w.emitted("update:modelValue")!.at(-1)).toEqual(["modes"])
    await t()[0].trigger("keydown", { key: "End" })
    expect(w.emitted("update:modelValue")!.at(-1)).toEqual(["log"])
  })

  it("wraps the tabs below 600 px", () => {
    const src = tabsSource.replace(/\s+/g, " ")
    expect(src).toMatch(/@media \(max-width: 599px\) \{ \.tabs \{[^}]*flex-wrap: wrap/)
    expect(src).toMatch(/@media \(max-width: 599px\) \{ \.tabs \{[^}]*overflow-x: visible/)
  })
})
