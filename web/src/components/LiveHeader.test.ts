import { describe, expect, it } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import LiveHeader from "./LiveHeader.vue"

const HELP = "Help for the pages: docs/wiki/Dashboard.md#pages"

describe("LiveHeader", () => {
  it("shows the time of the last load and emits refresh", async () => {
    const r = render(LiveHeader, {
      props: { updatedAt: new Date(2026, 10, 23, 9, 5), stale: false },
    })
    expect(r.container.textContent).toContain("Updated 09:05")
    const btn = screen.getByRole("button", { name: "↻ Refresh" })
    expect(btn.getAttribute("data-slot")).toBe("button")
    await userEvent.setup().click(btn)
    expect(r.emitted().refresh).toHaveLength(1)
  })

  it("shows no time before the first load, and marks a load as busy", () => {
    const r = render(LiveHeader, { props: { updatedAt: null, stale: true } })
    const root = r.container.firstElementChild!
    expect(root.textContent).not.toContain("Updated")
    expect(root.getAttribute("aria-busy")).toBe("true")
  })

  it("links to the Pages help of the wiki in a new tab", () => {
    render(LiveHeader, { props: { updatedAt: null, stale: false } })
    const a = screen.getByRole("link", { name: HELP })
    expect(a.textContent!.trim()).toBe("?")
    expect(a.getAttribute("href")).toBe(
      "https://github.com/dnwandana/tether-agent/wiki/Dashboard#pages",
    )
    expect(a.getAttribute("target")).toBe("_blank")
    expect(a.getAttribute("rel")).toBe("noreferrer")
    expect(a.getAttribute("title")).toBe("docs/wiki/Dashboard.md#pages")
    expect(a.getAttribute("data-slot")).toBe("button")
  })
})
