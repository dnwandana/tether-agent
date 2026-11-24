import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"
import HttpsNote from "./HttpsNote.vue"

// A stub panel: a dialog with the title as its name, only while open.
const SidePanel = {
  props: ["open", "title"],
  template: '<div v-if="open" role="dialog" :aria-label="title"><slot /></div>',
}

describe("HttpsNote", () => {
  it("shows one line with the help link, and opens the help in a side panel", async () => {
    const user = userEvent.setup()
    render(HttpsNote, { global: { stubs: { SidePanel } } })
    const line = document.querySelector<HTMLElement>('[data-slot="alert"]')!
    expect(line.textContent?.replace(/\s+/g, " ").trim()).toBe(
      "Voice notes, Copy, and notifications need HTTPS or localhost. How to turn on HTTPS",
    )
    expect(screen.queryByRole("dialog")).toBeNull()
    await user.click(within(line).getByRole("button", { name: "How to turn on HTTPS" }))
    const panel = screen.getByRole("dialog", { name: "Turn on HTTPS" })
    expect(panel.textContent).toContain("Set dashboard.tls_cert and dashboard.tls_key")
  })
})
