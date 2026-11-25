import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"
import type { SetupStep } from "../api/types.gen"

const h = vi.hoisted(() => ({
  live: null as null | { load: () => Promise<void>; opts?: { events?: string[] } },
}))
vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", async () => ({
  ...(await vi.importActual<object>("../stores/ui")),
  toast: vi.fn(),
  confirm: vi.fn(),
  isSecure: vi.fn(() => true),
}))
vi.mock("../lib/errors", () => ({
  friendlyError: (e: unknown) => (e instanceof Error ? e.message : String(e)),
}))
vi.mock("../lib/live", async () => {
  const actual = await vi.importActual<object>("../lib/live")
  const { onMounted, ref } = await import("vue")
  return {
    ...actual,
    useLiveData: (load: () => Promise<void>, opts?: any) => {
      h.live = { load, opts }
      onMounted(() => void load())
      return { stale: ref(false), updatedAt: ref(null), refresh: load }
    },
  }
})
import { api } from "../api/client"
import { isSecure, toast } from "../stores/ui"
import GettingStartedPage from "./GettingStartedPage.vue"

const Blank = { render: () => null }
const unknown = (name: string): SetupStep => ({
  name,
  state: "unknown",
  detail: "Run the doctor on the System page.",
  command: "tether doctor",
})
const steps: SetupStep[] = [
  { name: "Primary provider", state: "done", detail: "", command: "" },
  {
    name: "Fallback entries",
    state: "open",
    detail: "No fallback entry. A failed primary stops the turns.",
    command: "tether setup",
  },
  { name: "Telegram", state: "open", detail: "Telegram is off.", command: "tether setup" },
  unknown("Host programs"),
  unknown("Service unit"),
  { name: "Dashboard", state: "done", detail: "", command: "" },
  {
    name: "HTTPS",
    state: "open",
    detail:
      "Set dashboard.tls_cert and dashboard.tls_key, for example to the files of tailscale cert.",
    command: "tailscale cert <name>.<tailnet>.ts.net",
  },
]

const posts = () => vi.mocked(api).mock.calls.filter((c) => c[0] === "POST")

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/start", name: "start", component: GettingStartedPage },
      { path: "/system", name: "system", component: Blank },
    ],
  })
  await router.push("/start")
  render(GettingStartedPage, {
    global: { plugins: [router], stubs: { SidePanel: true, LiveHeader: true } },
  })
  await flushPromises()
  return { router, user: userEvent.setup() }
}
const rows = () =>
  within(screen.getByRole("list", { name: "Setup steps" })).getAllByRole("listitem")
const markOf = (li: HTMLElement) => li.querySelector("[data-tone]")!
const buttons = (name: string) => screen.queryAllByRole("button", { name })

beforeEach(() => {
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string) => (m === "POST" ? { task_id: "t1" } : steps) as any)
  vi.mocked(isSecure).mockReturnValue(true)
  vi.mocked(toast).mockReset()
})

describe("GettingStartedPage", () => {
  it("shows a done step with a check icon, an open step with its command, and an unknown step with a help icon", async () => {
    await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/setup")
    expect(markOf(rows()[0]).getAttribute("data-tone")).toBe("done")
    expect(markOf(rows()[0]).querySelector("svg.lucide-check")).not.toBeNull()
    expect(markOf(rows()[0]).querySelector(".sr-only")?.textContent).toBe("done")
    expect(markOf(rows()[0]).textContent?.trim()).toBe("done")
    expect(rows()[0].querySelector("code")).toBeNull()
    expect(markOf(rows()[1]).textContent?.trim()).toBe("2")
    expect(markOf(rows()[1]).querySelector("svg")).toBeNull()
    expect(rows()[1].querySelector("code")?.textContent).toBe("tether setup")
    expect(markOf(rows()[3]).getAttribute("data-tone")).toBe("unknown")
    expect(markOf(rows()[3]).querySelector("svg.lucide-circle-question-mark")).not.toBeNull()
    expect(markOf(rows()[3]).querySelector(".sr-only")?.textContent).toBe("unknown")
    expect(markOf(rows()[3]).textContent?.trim()).toBe("unknown")
    expect(rows()[3].textContent).toContain("Run the doctor on the System page.")
    expect(rows()[3].querySelector("code")).toBeNull()
    expect(within(rows()[3]).queryAllByRole("button")).toHaveLength(0)
    expect(within(rows()[4]).queryAllByRole("button")).toHaveLength(0)
    expect(buttons("Run doctor")).toHaveLength(1)
    expect(
      buttons("Run doctor")[0].compareDocumentPosition(rows()[0]) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).not.toBe(0)
    expect(rows()[6].textContent).toContain("How to turn on HTTPS")
    expect(buttons("Copy")).toHaveLength(3)
  })

  it("starts the doctor and opens System", async () => {
    const { router, user } = await mk()
    await user.click(buttons("Run doctor")[0])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/doctor")
    expect(router.currentRoute.value.name).toBe("system")
  })

  it("posts the doctor once on a double tap and disables the buttons", async () => {
    const hold: { done: (v: unknown) => void } = { done: () => {} }
    vi.mocked(api).mockImplementation(async (m: string) =>
      m === "POST" ? new Promise((r) => (hold.done = r)) : (steps as any),
    )
    const { user } = await mk()
    await user.click(buttons("Run doctor")[0])
    await user.click(buttons("Run doctor")[0])
    expect(posts()).toHaveLength(1)
    expect(buttons("Run doctor")[0].hasAttribute("disabled")).toBe(true)
    hold.done({})
    await flushPromises()
    expect(posts()).toHaveLength(1)
  })

  it("shows the server text of a refused doctor run and still opens System", async () => {
    vi.mocked(api).mockImplementation(async (m: string) => {
      if (m === "POST") throw new Error("The doctor ran 12 s ago. Try again in 48 s.")
      return steps as any
    })
    const { router, user } = await mk()
    await user.click(buttons("Run doctor")[0])
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("The doctor ran 12 s ago. Try again in 48 s.", "error")
    expect(router.currentRoute.value.name).toBe("system")
  })

  it("checks the steps again", async () => {
    const { user } = await mk()
    vi.mocked(api).mockClear()
    await user.click(buttons("Check again")[0])
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/setup")
  })

  it("shows the HTTPS note in place of Copy on plain HTTP, but not in the HTTPS row", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    await mk()
    expect(buttons("Copy")).toHaveLength(0)
    expect(
      rows().filter((r) => r.textContent?.includes("Copy needs HTTPS or localhost.")),
    ).toHaveLength(2)
    expect(rows()[6].textContent).not.toContain("Copy needs HTTPS or localhost.")
    expect(within(rows()[6]).getAllByRole("button", { name: "How to turn on HTTPS" })).toHaveLength(
      1,
    )
  })

  it("shows no Run doctor button when no step is unknown", async () => {
    vi.mocked(api).mockImplementation(async (m: string) =>
      m === "POST"
        ? ({ task_id: "t1" } as any)
        : (steps.filter((s) => s.state !== "unknown") as any),
    )
    await mk()
    expect(buttons("Run doctor")).toHaveLength(0)
  })

  it("reloads only on a focus and a reconnect", async () => {
    await mk()
    expect(h.live!.opts?.events).toBeUndefined()
  })
})
