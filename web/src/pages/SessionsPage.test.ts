import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { api, runCommand } from "../api/client"
import { session } from "../stores/session"
import SessionsPage from "./SessionsPage.vue"
import StateBlock from "../components/StateBlock.vue"

const Blank = { render: () => null }
const row = (o: object) => ({
  id: "",
  title: "",
  kind: "chat",
  source: "dashboard",
  source_label: "Dashboard",
  last_active: "2026-11-20T09:00:00Z",
  started: "2026-11-19T09:00:00Z",
  messages: 4,
  cost_usd: 0.0123,
  archived: false,
  read_only: false,
  children: [],
  ...o,
})
const page = {
  total: 3,
  rows: [
    row({ id: "s1", title: "Release notes" }),
    row({
      id: "s2",
      title: "Photos",
      source: "telegram",
      source_label: "Telegram › DM",
      read_only: true,
      children: [row({ id: "c1", title: "subagent run", kind: "subagent", read_only: true })],
    }),
    row({ id: "s3", title: "Old chat" }),
  ],
}

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/", name: "chat", component: Blank },
      { path: "/sessions", name: "sessions", component: SessionsPage },
      { path: "/sessions/:id", name: "transcript", component: Blank },
    ],
  })
  await router.push("/sessions")
  const w = mount(SessionsPage, { global: { plugins: [router] } })
  await flushPromises()
  return { w, router }
}
const btn = (w: any, t: string) => w.findAll("button").filter((b: any) => b.text() === t)

beforeEach(() => {
  window.matchMedia = ((q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
  })) as any
  vi.mocked(api).mockReset().mockResolvedValue(page)
  vi.mocked(runCommand)
    .mockReset()
    .mockResolvedValue({ request_id: "r", text: "Done.", buttons: [], edit: false })
  session.id = "s1"
})

describe("SessionsPage", () => {
  it("loads the first page with all filter keys", async () => {
    const { w } = await mk()
    expect(api).toHaveBeenCalledWith(
      "GET",
      "/api/sessions?source=&kind=chat&search=&archived=0&limit=50&offset=0",
    )
    expect(w.text()).toContain("Release notes")
    expect(w.text()).toContain("Telegram › DM")
    expect(w.find(".pager").text()).toContain("1–3 of 3")
  })

  it("shows children under the parent as read-only rows", async () => {
    const { w } = await mk()
    const child = w.find(".cellrow.is-child")
    expect(child.text()).toContain("↳")
    expect(child.text()).toContain("subagent run")
  })

  it("opens the active session and resumes another one", async () => {
    const { w, router } = await mk()
    expect(btn(w, "Open")).toHaveLength(1)
    await btn(w, "Resume")[0].trigger("click")
    await flushPromises()
    expect(runCommand).toHaveBeenCalledWith("/resume s3")
    expect(router.currentRoute.value.name).toBe("chat")
  })

  it("forks a read-only session into the dashboard", async () => {
    const { w } = await mk()
    await btn(w, "Fork into dashboard")[0].trigger("click")
    expect(runCommand).toHaveBeenCalledWith("/fork from s2")
  })

  it("shows the error state and retries", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("HTTP 500"))
    const { w } = await mk()
    expect(w.findComponent(StateBlock).props("state")).toBe("error")
    w.findComponent(StateBlock).vm.$emit("retry")
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(2)
    expect(w.text()).toContain("Release notes")
  })

  it("revokes the export URL after a delay, not at once", async () => {
    vi.useFakeTimers()
    const createURL = URL.createObjectURL
    const revokeURL = URL.revokeObjectURL
    try {
      const revoke = vi.fn()
      URL.createObjectURL = vi.fn(() => "blob:x")
      URL.revokeObjectURL = revoke
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ({ ok: true, status: 200, blob: async () => new Blob(["x"]) })),
      )
      const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
      const { w } = await mk()
      await btn(w, "Export")[0].trigger("click")
      await vi.advanceTimersByTimeAsync(0)
      expect(click).toHaveBeenCalled()
      expect(revoke).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(30_000)
      expect(revoke).toHaveBeenCalledWith("blob:x")
      click.mockRestore()
      w.unmount()
    } finally {
      URL.createObjectURL = createURL
      URL.revokeObjectURL = revokeURL
      vi.unstubAllGlobals()
      vi.useRealTimers()
    }
  })
})
