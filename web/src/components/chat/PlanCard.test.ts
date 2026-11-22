import { mount } from "@vue/test-utils"
import { describe, expect, it } from "vitest"
import PlanCard from "./PlanCard.vue"

const items = [
  { text: "Read the store and the test", state: "completed", parent: -1 },
  { text: "Add an offset to Runs", state: "in_progress", parent: -1 },
  { text: "Update the test", state: "pending", parent: 0 },
  { text: "Try a cache", state: "cancelled", parent: -1 },
]

describe("PlanCard", () => {
  it("shows the items with their marks", () => {
    const w = mount(PlanCard, { props: { items } })
    expect(w.find(".plan-h").text()).toContain("📋 Plan 1/4")
    expect(w.findAll(".pi .mk").map((m) => m.text())).toEqual(["[x]", "[>]", "[ ]", "[~]"])
    expect(w.findAll(".pi .tx").map((m) => m.text())).toEqual(items.map((i) => i.text))
    expect(w.findAll(".pi")[2].classes()).toContain("child")
    expect(w.findAll(".pi")[0].classes()).not.toContain("child")
    expect(w.findAll(".pi")[1].classes()).not.toContain("child")
    expect(w.find(".plan-h .meter > span").attributes("style")).toContain("width: 25%")
  })

  it("marks the changed items", () => {
    const before = [{ ...items[0], state: "in_progress" }, items[1]]
    const w = mount(PlanCard, { props: { items, before } })
    expect(w.findAll(".pi")[0].classes()).toContain("changed")
    expect(w.findAll(".pi")[0].find(".chg").text()).toBe("changed")
    expect(w.findAll(".pi")[1].classes()).not.toContain("changed")
  })

  it("says done when no item is open", () => {
    const w = mount(PlanCard, { props: { items: [items[0], items[3]] } })
    expect(w.find(".plan-h").text()).toContain("📋 Plan done")
  })
})
