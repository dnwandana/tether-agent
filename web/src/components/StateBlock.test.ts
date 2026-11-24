import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"
import StateBlock from "./StateBlock.vue"

const hint = () => document.querySelector<HTMLElement>("[data-hint]")

describe("StateBlock", () => {
  it("shows 5 skeleton rows while loading", () => {
    render(StateBlock, { props: { state: "loading" } })
    const box = screen.getByRole("status", { name: "Loading" })
    expect(box.getAttribute("aria-busy")).toBe("true")
    expect(box.querySelectorAll('[data-slot="skeleton-row"]')).toHaveLength(5)
    expect(box.querySelectorAll('[data-slot="skeleton"]')).toHaveLength(20)
  })

  it("shows the help line and the next action when empty", async () => {
    const user = userEvent.setup()
    const { emitted } = render(StateBlock, {
      props: { state: "empty", text: "No jobs yet", action: "Create a job" },
    })
    expect(screen.getByText("No jobs yet")).toBeTruthy()
    await user.click(screen.getByRole("button", { name: "Create a job" }))
    expect(emitted().action).toHaveLength(1)
  })

  it("shows the cause and Retry on an error", async () => {
    const user = userEvent.setup()
    const { emitted } = render(StateBlock, {
      props: { state: "error", text: "GET /api/jobs: HTTP 502" },
    })
    const alert = screen.getByRole("alert")
    expect(alert.getAttribute("data-slot")).toBe("alert")
    expect(alert.querySelector("[data-cause]")?.textContent).toBe("GET /api/jobs: HTTP 502")
    await user.click(screen.getByRole("button", { name: "Retry" }))
    expect(emitted().action).toHaveLength(1)
  })

  it("emits only action, not retry", async () => {
    const user = userEvent.setup()
    const empty = render(StateBlock, {
      props: { state: "empty", text: "No jobs yet.", action: "+ New job" },
    })
    await user.click(screen.getByRole("button", { name: "+ New job" }))
    expect(empty.emitted().action).toHaveLength(1)
    expect(empty.emitted().retry).toBeUndefined()
    empty.unmount()
    const error = render(StateBlock, { props: { state: "error", text: "HTTP 502" } })
    await user.click(screen.getByRole("button", { name: "Retry" }))
    expect(error.emitted().action).toHaveLength(1)
    expect(error.emitted().retry).toBeUndefined()
  })

  it("uses a title that is not a heading", () => {
    render(StateBlock, { props: { state: "empty", text: "No jobs yet." } })
    expect(screen.getByText("No jobs yet.").getAttribute("data-slot")).toBe("empty-title")
    expect(screen.queryByRole("heading")).toBeNull()
  })

  it("shows a title, the text, and a one-line hint when empty", () => {
    render(StateBlock, {
      props: {
        state: "empty",
        title: "No runs yet.",
        text: "The first run is at 09:00.",
        hint: "Run history keeps the last runs.",
        action: "⚡ Run now",
      },
    })
    expect(screen.getByText("No runs yet.").getAttribute("data-slot")).toBe("empty-title")
    expect(screen.getByText("The first run is at 09:00.")).toBeTruthy()
    expect(hint()?.textContent).toBe("Run history keeps the last runs.")
    expect(screen.getByRole("button", { name: "⚡ Run now" })).toBeTruthy()
  })

  it("shows no button when an empty state has no action", () => {
    render(StateBlock, { props: { state: "empty", text: "No messages yet." } })
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("makes an error an alert with Could not load as the default title", () => {
    render(StateBlock, {
      props: {
        state: "error",
        text: "Cannot reach the gateway. Check the network, then try again.",
      },
    })
    const alert = screen.getByRole("alert")
    expect(alert.querySelector('[data-slot="alert-title"]')?.textContent?.trim()).toBe(
      "Could not load",
    )
  })

  it("uses the title prop of an error", async () => {
    const user = userEvent.setup()
    const { emitted } = render(StateBlock, {
      props: {
        state: "error",
        title: "The doctor failed",
        text: "HTTP 502",
        hint: "See the log on System operations.",
        action: "Run doctor",
      },
    })
    const alert = screen.getByRole("alert")
    expect(alert.querySelector('[data-slot="alert-title"]')?.textContent?.trim()).toBe(
      "The doctor failed",
    )
    expect(alert.querySelector("[data-cause]")?.textContent).toBe("HTTP 502")
    expect(hint()?.textContent).toBe("See the log on System operations.")
    await user.click(screen.getByRole("button", { name: "Run doctor" }))
    expect(emitted().action).toHaveLength(1)
  })
})
