import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import PlanCard from "./PlanCard.vue"

const items = [
  { text: "Read the store and the test", state: "completed", parent: -1 },
  { text: "Add an offset to Runs", state: "in_progress", parent: -1 },
  { text: "Update the test", state: "pending", parent: 0 },
  { text: "Try a cache", state: "cancelled", parent: -1 },
]
const trigger = () => screen.getByRole("button", { name: /📋 Plan/ })
const rows = () => screen.getAllByRole("listitem")

describe("PlanCard", () => {
  it("shows the items with their marks", () => {
    render(PlanCard, { props: { items } })
    expect(trigger().getAttribute("aria-expanded")).toBe("true")
    expect(trigger().textContent).toContain("📋 Plan")
    expect(trigger().querySelector('[data-slot="trace-description"]')!.textContent!.trim()).toBe(
      "1 of 4 done",
    )
    expect(trigger().querySelector('[data-slot="trace-line"]')!.getAttribute("data-status")).toBe(
      "running",
    )
    expect(rows().map((r) => r.querySelector(".font-mono")!.textContent!.trim())).toEqual([
      "[x]",
      "[>]",
      "[ ]",
      "[~]",
    ])
    expect(
      rows().map((r) => r.querySelector('[data-slot="trace-title"]')!.textContent!.trim()),
    ).toEqual(items.map((i) => i.text))
    expect(rows().map((r) => r.hasAttribute("data-child"))).toEqual([false, false, true, false])
    expect(rows()[2].classList.contains("ml-4")).toBe(true)
    expect(rows().map((r) => r.getAttribute("data-state"))).toEqual([
      "completed",
      "in_progress",
      "pending",
      "cancelled",
    ])
    expect(rows()[1].getAttribute("data-status")).toBe("running")
    expect(rows()[2].getAttribute("data-status")).toBe("pending")
    expect(rows()[3].classList.contains("line-through")).toBe(true)
    expect(screen.getByRole("progressbar").getAttribute("aria-valuenow")).toBe("25")
  })

  it("marks the changed items", () => {
    const before = [{ ...items[0], state: "in_progress" }, items[1]]
    render(PlanCard, { props: { items, before } })
    expect(rows()[0].hasAttribute("data-changed")).toBe(true)
    const chip = screen.getByText("changed")
    expect(chip.getAttribute("data-slot")).toBe("badge")
    expect(rows()[0].contains(chip)).toBe(true)
    expect(rows()[1].hasAttribute("data-changed")).toBe(false)
  })

  it("says done when no item is open", () => {
    render(PlanCard, { props: { items: [items[0], items[3]] } })
    expect(trigger().textContent).toContain("📋 Plan done")
    expect(trigger().querySelector('[data-slot="trace-description"]')).toBeNull()
    expect(
      trigger().querySelector('[data-slot="trace-line"]')!.getAttribute("data-status"),
    ).toBeNull()
  })
})
