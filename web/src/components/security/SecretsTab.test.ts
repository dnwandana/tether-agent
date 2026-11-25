import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"

vi.mock("../../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/client")>()),
  api: vi.fn(),
  runCommand: vi.fn(),
}))
vi.mock("../../stores/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../stores/ui")>()),
  toast: vi.fn(),
  confirm: vi.fn(),
}))
import { runCommand } from "../../api/client"
import { ui } from "../../stores/ui"
import SecretsTab from "./SecretsTab.vue"
import { fullTime } from "../../lib/time"

const sha = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
const secrets = [
  {
    name: "GITHUB_TOKEN",
    state: "set",
    commands: ["gh"],
    failed_at: "0001-01-01T00:00:00Z",
    pins: [
      { path: "/usr/bin/gh", sha256: sha },
      { path: "/opt/my tools/gh", sha256: sha },
    ],
    set: "/secrets set GITHUB_TOKEN",
    unpin: "/secrets unpin GITHUB_TOKEN",
  },
  {
    name: "PYPI_TOKEN",
    state: "not set",
    commands: ["twine upload"],
    failed_at: "0001-01-01T00:00:00Z",
    pins: [],
    set: "/secrets set PYPI_TOKEN",
    unpin: "/secrets unpin PYPI_TOKEN",
  },
]
const reply = (text: string) => ({ request_id: "r", text, buttons: [], edit: false })

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(runCommand).mockReset().mockResolvedValue(reply("Done."))
  ui.trayOpen = false
})

describe("SecretsTab", () => {
  it("shows the time of a failed check in the short format, with the full time in the title", () => {
    const at = new Date(new Date().getFullYear(), 10, 20, 9, 12).toISOString()
    render(SecretsTab, { props: { secrets: [{ ...secrets[0], state: "check", failed_at: at }] } })
    const chip = screen.getByText("✓ set · auth failed Nov 20 09:12")
    expect(chip.getAttribute("data-slot")).toBe("badge")
    expect(chip.getAttribute("title")).toBe(fullTime(at))
  })

  it("shows each pin with its path and a short hash", async () => {
    const r = render(SecretsTab, { props: { secrets } })
    expect(r.container.textContent).toContain("/usr/bin/gh")
    const folds = screen.getAllByRole("button", { name: sha.slice(0, 12) })
    expect(folds).toHaveLength(2)
    expect(screen.queryByText(sha)).toBeNull()
    await userEvent.setup().click(folds[0])
    expect(screen.getByText(sha)).toBeTruthy()
    expect(screen.getByText("No allowed programs.")).toBeTruthy()
  })

  it("unpins one program by its path, also with a space in it", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply(
        "Removed the allowed program `/opt/my tools/gh` of GITHUB_TOKEN. The next use asks again.",
      ),
    )
    const r = render(SecretsTab, { props: { secrets } })
    await userEvent.setup().click(screen.getAllByRole("button", { name: "Unpin" })[1])
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/secrets unpin GITHUB_TOKEN /opt/my tools/gh")
    expect(screen.getByText(/Removed the allowed program/)).toBeTruthy()
    expect(r.emitted().changed).toHaveLength(1)
  })

  it("opens the tray after Set value", async () => {
    render(SecretsTab, { props: { secrets } })
    await userEvent.setup().click(screen.getAllByRole("button", { name: "Set value…" })[1])
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/secrets set PYPI_TOKEN")
    expect(ui.trayOpen).toBe(true)
    expect(screen.getByText("Type the value in the 🔑 card in ⏳ Waiting.")).toBeTruthy()
  })

  it("shows the empty text with no bound secrets", () => {
    const r = render(SecretsTab, { props: { secrets: [] } })
    expect(r.container.textContent).toContain("No bound secrets.")
  })

  it("lets the pins wrap, puts Set value at the right edge, and names the pager", () => {
    const many = Array.from({ length: 12 }, (_, i) => ({ ...secrets[1], name: "TOKEN_" + i }))
    render(SecretsTab, { props: { secrets: [secrets[0], ...many] } })
    const pin = screen.getByText("/usr/bin/gh").closest("td")!
    expect(pin.classList.contains("whitespace-normal")).toBe(true)
    const set = screen.getAllByRole("button", { name: "Set value…" })[0]
    expect(set.closest("td")!.classList.contains("text-right")).toBe(true)
    expect(screen.getByRole("navigation", { name: "Secrets pages" })).toBeTruthy()
  })
})
