import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({
  toast: vi.fn(),
  confirm: vi.fn(),
  isSecure: vi.fn(() => true),
  prefs: { lastRoute: "" },
}))
import { api } from "../api/client"
import { isSecure } from "../stores/ui"
import GettingStartedPage from "./GettingStartedPage.vue"

const steps = [
  {
    name: "Primary provider",
    state: "ok",
    detail: "zai · glm-4.6 · test call OK",
    command: "tether setup",
  },
  {
    name: "Host programs",
    state: "warn",
    detail: "chrome is missing",
    command: "sudo apt install chromium",
  },
  {
    name: "HTTPS",
    state: "open",
    detail: "plain HTTP on 10.8.0.1:8787",
    command: "tailscale cert agent.tail1234.ts.net",
  },
]
const mk = async () => {
  const w = mount(GettingStartedPage, { global: { stubs: { SidePanel: true } } })
  await flushPromises()
  return w
}

beforeEach(() => {
  vi.mocked(api).mockReset().mockResolvedValue(steps)
  vi.mocked(isSecure).mockReturnValue(true)
})

describe("GettingStartedPage", () => {
  it("shows each step and the command of each open step", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/setup")
    const rows = w.findAll(".li")
    expect(rows[0].find(".lead").classes()).toContain("is-done")
    expect(rows[0].find("code").exists()).toBe(false)
    expect(rows[1].find("code").text()).toBe("sudo apt install chromium")
    expect(rows[2].text()).toContain("How to turn on HTTPS")
    expect(w.findAll("button").filter((b) => b.text() === "Copy")).toHaveLength(2)
  })

  it("hides Copy without a secure context", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    const w = await mk()
    expect(w.findAll("button").filter((b) => b.text() === "Copy")).toHaveLength(0)
  })
})
