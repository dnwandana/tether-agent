import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import ReasoningBlock from "./ReasoningBlock.vue"

describe("ReasoningBlock", () => {
  it("is a closed block with the token count", async () => {
    const r = render(ReasoningBlock, {
      props: { text: "Runs() has no offset. <b>x</b>", tokens: 1812 },
    })
    const trigger = screen.getByRole("button", { name: /Reasoning/ })
    expect(trigger.getAttribute("aria-expanded")).toBe("false")
    expect(trigger.textContent!.trim()).toMatch(/^Reasoning\s*1,812 tokens$/)
    expect(r.container.querySelector('[data-slot="trace-collapsible"]')).not.toBeNull()
    expect(screen.queryByText("Runs() has no offset. <b>x</b>")).toBeNull()
    await userEvent.setup().click(trigger)
    expect(trigger.getAttribute("aria-expanded")).toBe("true")
    expect(screen.getByText("Runs() has no offset. <b>x</b>")).toBeTruthy()
  })

  it("is never a running line", () => {
    const r = render(ReasoningBlock, { props: { text: "a", tokens: 3 } })
    const line = r.container.querySelector('[data-slot="trace-line"]')!
    expect(line.getAttribute("data-status")).toBeNull()
  })

  it("does not show with no text", () => {
    const r = render(ReasoningBlock, { props: { text: "", tokens: 0 } })
    expect(r.container.querySelector('[data-slot="trace"]')).toBeNull()
    expect(screen.queryByRole("button")).toBeNull()
  })
})
