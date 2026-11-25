import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import QueueStrip from "./QueueStrip.vue"

describe("QueueStrip", () => {
  it("lists the queue and runs it now", async () => {
    const r = render(QueueStrip, {
      props: {
        items: [
          { kind: "media", preview: "📎 notes.md" },
          { kind: "skill", preview: "/release-notes" },
        ],
      },
    })
    const head = screen.getByText("Waiting · 2 / 20").closest('[data-slot="item"]')!
    expect(head.textContent).toContain("They run after the current task, one at a time.")
    expect(screen.getAllByRole("listitem").map((q) => q.textContent!.replace(/\s+/g, ""))).toEqual([
      "1📎notes.md",
      "2/release-notes",
    ])
    expect(screen.getAllByRole("listitem")[0].getAttribute("data-slot")).toBe("item")
    const now = screen.getByRole("button", { name: "Do this now" })
    expect(now.textContent!.trim()).toBe("Do this now")
    expect(head.contains(now)).toBe(true)
    await userEvent.setup().click(now)
    expect(r.emitted().now).toHaveLength(1)
  })

  it("shows the goal line of a goal turn with its icon and keeps a user preview", () => {
    render(QueueStrip, {
      props: {
        items: [
          { kind: "goal", preview: "↻ Goal 3/20: fix the test" },
          { kind: "text", preview: "ship it 🚀" },
        ],
      },
    })
    const rows = screen.getAllByRole("listitem")
    expect(rows[0].textContent!.replace(/\s+/g, "")).toBe("1Goal3/20:fixthetest")
    expect(rows[0].querySelector("svg.lucide-refresh-cw")).not.toBeNull()
    expect(rows[1].textContent).toContain("ship it 🚀")
    expect(rows[1].querySelector("svg")).toBeNull()
  })

  it("shows nothing when the queue is empty", () => {
    const r = render(QueueStrip, { props: { items: [] } })
    expect(r.container.querySelector('[data-slot="item"]')).toBeNull()
  })
})
