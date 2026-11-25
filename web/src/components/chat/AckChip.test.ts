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
  it("shows the text with the tone of its kind and drops a lead mark of that tone", () => {
    const r = render(AckChip, {
      props: { ack: ack("steer", "↪️ Got it. I’ll do this after the current step.") },
    })
    expect(root(r.container).getAttribute("data-tone")).toBe("run")
    expect(root(r.container).classList.contains("text-info")).toBe(true)
    expect(r.container.textContent).toContain("Got it. I’ll do this after the current step.")
    expect(r.container.textContent).not.toContain("↪")
    expect(r.container.querySelector("svg")).toBeNull()
    r.unmount()
    const q = render(AckChip, { props: { ack: ack("queue", "⏳ Queued.") } })
    expect(root(q.container).getAttribute("data-tone")).toBe("pending")
    expect(q.container.textContent!.trim()).toBe("Queued.")
    q.unmount()
    const other = render(AckChip, { props: { ack: ack("odd", "?") } })
    expect(root(other.container).getAttribute("data-tone")).toBeNull()
  })

  it("renders the bold of an ack", () => {
    const r = render(AckChip, {
      props: {
        ack: ack("bad", "⛔ **Queue full**\nThe message is not saved. Wait for the current task."),
      },
    })
    expect(r.container.querySelector("strong")!.textContent).toBe("Queue full")
    expect(r.container.textContent).not.toContain("**")
    expect(r.container.textContent).not.toContain("⛔")
    expect(r.container.querySelector("p")).toBeNull()
  })

  it("keeps a lead mark of another tone as an icon", () => {
    const r = render(AckChip, { props: { ack: ack("busy", "⏳ Busy") } })
    expect(root(r.container).getAttribute("data-tone")).toBe("warn")
    expect(r.container.querySelector("svg.glyph")).not.toBeNull()
    expect(r.container.textContent!.trim()).toBe("Busy")
  })

  it("emits the data of a button and shows its label with no mark", async () => {
    const r = render(AckChip, {
      props: { ack: ack("busy", "⏳ Busy", [[{ text: "⏹ Stop the task", data: "/stop" }]]) },
    })
    const stop = screen.getByRole("button", { name: "Stop the task" })
    expect(stop.getAttribute("data-variant")).toBe("ghost")
    expect(stop.textContent!.trim()).toBe("Stop the task")
    await userEvent.setup().click(stop)
    expect(r.emitted().action).toEqual([["/stop"]])
  })
})
