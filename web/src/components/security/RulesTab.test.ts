import { describe, it, expect, beforeEach } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import RulesTab from "./RulesTab.vue"

const fixed = {
  protected_paths: ["~/.ssh/"],
  human_only: ["password change"],
  mcp_classes: { gmail: { send: 2, read: 5 } },
  acp_presets: ["claude", "codex"],
  acp_workdirs: [],
  toolsets: { telegram: ["all"] },
}

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
})

const mk = (f = fixed) =>
  render(RulesTab, { props: { fixed: f as any, processMode: "smart", cronMode: "deny" } })
// Returns the Card that holds the title.
const card = (r: ReturnType<typeof mk>, title: string) =>
  within(r.container).getByText(title).closest('[data-slot="card"]') as HTMLElement

describe("RulesTab", () => {
  it("shows the ACP presets and an empty block as None", () => {
    const r = mk()
    const acp = card(r, "ACP presets")
    expect(acp.textContent).toContain("claude")
    expect(acp.textContent).toContain("codex")
    expect(card(r, "ACP work folders").textContent).toContain("None.")
  })

  it("explains each process mode and cron mode and marks the current one", () => {
    const r = mk()
    const lines = within(card(r, "Process mode")).getAllByRole("listitem")
    expect(lines.map((l) => l.querySelector("code")!.textContent)).toEqual([
      "manual",
      "smart",
      "off",
    ])
    expect(lines[0].textContent).toContain("The gate asks you about each dangerous command.")
    const now = within(lines[1]).getByText("now")
    expect(now.getAttribute("data-slot")).toBe("badge")
    const cron = within(card(r, "Cron mode")).getAllByRole("listitem")
    expect(cron.map((l) => l.querySelector("code")!.textContent)).toEqual(["deny", "approve"])
    expect(within(cron[0]).getByText("now")).toBeTruthy()
  })

  it("stacks the cards and folds the protected paths", async () => {
    const one = mk()
    expect(one.container.firstElementChild!.classList.contains("flex-col")).toBe(true)
    const paths = card(one, "Protected paths")
    const fold = within(paths).getByRole("button", { name: "1 path" })
    expect(fold.getAttribute("aria-expanded")).toBe("false")
    expect(paths.textContent).not.toContain("~/.ssh/")
    await userEvent.setup().click(fold)
    expect(fold.getAttribute("aria-expanded")).toBe("true")
    expect(paths.textContent).toContain("~/.ssh/")
    one.unmount()
    const two = mk({ ...fixed, protected_paths: ["~/.ssh/", "~/.tether/state.db"] })
    expect(
      within(card(two, "Protected paths")).getByRole("button", { name: "2 paths" }),
    ).toBeTruthy()
  })

  it("puts a space between the mode name, the now chip, and the text", () => {
    const r = mk()
    const line = within(card(r, "Process mode")).getAllByRole("listitem")[1]
    expect(line.textContent!.replace(/\s+/g, " ").trim()).toMatch(/^smart now \S/)
  })

  it("names the pager of the MCP classes", () => {
    const classes = Object.fromEntries(
      Array.from({ length: 12 }, (_, i) => ["server" + i, { send: 1 }]),
    )
    mk({ ...fixed, mcp_classes: classes })
    expect(screen.getByRole("navigation", { name: "MCP classes pages" })).toBeTruthy()
    expect(screen.getByText("1–12 of 12")).toBeTruthy()
  })
})
