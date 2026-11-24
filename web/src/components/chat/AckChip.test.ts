import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import AckChip from "./AckChip.vue"

const ack = (kind: string, text: string, buttons: { text: string; data: string }[][] = []) => ({
  id: "a-" + kind,
  kind,
  text,
  request_id: "",
  buttons,
})
const root = (c: Element) => c.querySelector('[data-slot="marker"]')!

describe("AckChip", () => {
  it("shows the text with the tone of its kind", () => {
    const r = render(AckChip, {
      props: { ack: ack("steer", "↪️ Got it. I’ll do this after the current step.") },
    })
    expect(root(r.container).getAttribute("data-tone")).toBe("run")
    expect(root(r.container).classList.contains("text-info")).toBe(true)
    expect(r.container.textContent).toContain("↪️ Got it.")
    r.unmount()
    const q = render(AckChip, { props: { ack: ack("queue", "⏳ Queued.") } })
    expect(root(q.container).getAttribute("data-tone")).toBe("pending")
    q.unmount()
    const other = render(AckChip, { props: { ack: ack("odd", "?") } })
    expect(root(other.container).getAttribute("data-tone")).toBeNull()
  })

  it("emits the data of a button", async () => {
    const r = render(AckChip, {
      props: { ack: ack("busy", "⏳ Busy", [[{ text: "⏹ Stop the task", data: "/stop" }]]) },
    })
    expect(root(r.container).getAttribute("data-tone")).toBe("warn")
    const stop = screen.getByRole("button", { name: "⏹ Stop the task" })
    expect(stop.getAttribute("data-variant")).toBe("ghost")
    await userEvent.setup().click(stop)
    expect(r.emitted().action).toEqual([["/stop"]])
  })
})
