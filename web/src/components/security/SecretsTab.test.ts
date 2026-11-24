import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

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
import ResultNote from "../ResultNote.vue"
import SecretsTab from "./SecretsTab.vue"

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
  it("shows each pin with its path and a short hash", async () => {
    const w = mount(SecretsTab, { props: { secrets } })
    expect(w.text()).toContain("/usr/bin/gh")
    expect(w.text()).toContain(sha.slice(0, 12))
    expect(w.find("details").text()).toContain(sha)
    expect(w.text()).toContain("No allowed programs.")
  })

  it("unpins one program by its path, also with a space in it", async () => {
    vi.mocked(runCommand).mockResolvedValueOnce(
      reply(
        "Removed the allowed program `/opt/my tools/gh` of GITHUB_TOKEN. The next use asks again.",
      ),
    )
    const w = mount(SecretsTab, { props: { secrets } })
    await w
      .findAll("button")
      .filter((b) => b.text() === "Unpin")[1]
      .trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/secrets unpin GITHUB_TOKEN /opt/my tools/gh")
    expect(w.findComponent(ResultNote).props("note").text).toContain("Removed the allowed program")
    expect(w.emitted("changed")).toHaveLength(1)
  })

  it("opens the tray after Set value", async () => {
    const w = mount(SecretsTab, { props: { secrets } })
    await w
      .findAll("button")
      .filter((b) => b.text() === "Set value…")[1]
      .trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/secrets set PYPI_TOKEN")
    expect(ui.trayOpen).toBe(true)
    expect(w.findComponent(ResultNote).props("note").text).toBe(
      "Type the value in the 🔑 card in ⏳ Waiting.",
    )
  })

  it("shows the empty text with no bound secrets", () => {
    const w = mount(SecretsTab, { props: { secrets: [] } })
    expect(w.text()).toContain("No bound secrets.")
  })
})
