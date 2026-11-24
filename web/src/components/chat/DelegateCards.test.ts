import { describe, expect, it } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import DelegateCards from "./DelegateCards.vue"

const two =
  '{"results":[{"task":1,"status":"ok","summary":"Grouped 12 changes.","tool_calls":9,"duration_s":21},{"task":2,"status":"error","summary":"","tool_calls":2,"duration_s":3.5,"error":"timeout"}]}'
const goals =
  '{"tasks":[{"goal":"Group the changes by area"},{"goal":"Find the PR of each change"}]}'

describe("DelegateCards", () => {
  it("shows one card for each child", async () => {
    const r = render(DelegateCards, { props: { text: two, args: goals } })
    const triggers = screen.getAllByRole("button")
    expect(triggers).toHaveLength(2)
    expect(r.container.querySelectorAll('[data-slot="trace-collapsible"]')).toHaveLength(2)
    expect(triggers[0].textContent).toContain("Group the changes by area")
    expect(triggers[0].querySelector('[data-slot="trace-description"]')!.textContent!.trim()).toBe(
      "9 tool calls · 21 s",
    )
    expect(within(triggers[0]).getByText("ok").getAttribute("data-slot")).toBe("badge")
    expect(triggers[0].getAttribute("aria-expanded")).toBe("false")
    expect(screen.queryByText("Grouped 12 changes.")).toBeNull()
    await userEvent.setup().click(triggers[0])
    expect(screen.getByText("Grouped 12 changes.")).toBeTruthy()
    expect(within(triggers[1]).getByText("error")).toBeTruthy()
    expect(triggers[1].querySelector('[data-slot="trace-line"]')!.getAttribute("data-status")).toBe(
      "error",
    )
    expect(triggers[1].getAttribute("aria-expanded")).toBe("true")
    expect(screen.getByText("timeout")).toBeTruthy()
  })

  it("rounds the duration of a child", () => {
    const text =
      '{"results":[{"task":1,"status":"ok","summary":"","tool_calls":1,"duration_s":125.4},{"task":2,"status":"ok","summary":"","tool_calls":2,"duration_s":3.5}]}'
    const r = render(DelegateCards, { props: { text, args: "{}" } })
    const meta = Array.from(r.container.querySelectorAll('[data-slot="trace-description"]')).map(
      (m) => m.textContent!.trim(),
    )
    expect(meta).toEqual(["1 tool call · 2 m 5 s", "2 tool calls · 4 s"])
    expect(r.container.textContent).toContain("Task 1")
  })

  it("shows a background delegation", () => {
    const r = render(DelegateCards, {
      props: { text: '{"batch_id":"b7","children":["changelog-diff","pr-links"]}', args: "{}" },
    })
    expect(r.container.textContent!.replace(/\s+/g, " ")).toContain(
      "Started batch b7 with 2 children: changelog-diff, pr-links",
    )
  })
})
