import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class extends Error {},
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { api, runCommand } from "../api/client"
import { confirm, toast } from "../stores/ui"
import { status } from "../stores/status"
import OpsPage from "./OpsPage.vue"

const Blank = { render: () => null }
let update = {
  current: "v0.23.0",
  latest: "v0.24.0",
  newer: true,
  notes_url: "https://github.com/x/releases/tag/v0.24.0",
  writable: true,
  path: "/home/o/.local/bin/tether",
}
let backups: { dir: string; files: { name: string; size: number; modified: string }[] }
const newFile = {
  name: "tether-backup-2026-11-20.tar.gz",
  size: 13002342,
  modified: "2026-11-20T14:00:00Z",
}

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/system", name: "system", component: Blank },
      { path: "/system/ops", name: "ops", component: OpsPage },
    ],
  })
  await router.push("/system/ops")
  const w = mount(OpsPage, { global: { plugins: [router], stubs: { LogViewer: true } } })
  await flushPromises()
  return w
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!

beforeEach(() => {
  update = { ...update, newer: true, writable: true }
  backups = { dir: "~/.tether/backups", files: [] }
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string, p: string) => {
      if (p === "/api/config")
        return { path: "~/.tether/config.yaml", text: "api_key: «masked»" } as any
      if (p === "/api/update" && m === "GET") return update as any
      if (p === "/api/update" && m === "POST") return { task_id: "t2" } as any
      if (p === "/api/tasks/t2")
        return { id: "t2", kind: "update", state: "running", text: "" } as any
      if (p === "/api/backup" && m === "POST") {
        backups = { dir: backups.dir, files: [newFile] }
        return { task_id: "t1" } as any
      }
      if (p === "/api/backup") return backups as any
      if (p === "/api/tasks/t1")
        return {
          id: "t1",
          kind: "backup",
          state: "done",
          text: "Saved tether-backup-2026-11-20.tar.gz (13002342 bytes).",
        } as any
      return {} as any
    })
  vi.mocked(runCommand)
    .mockReset()
    .mockResolvedValue({ request_id: "r", text: "♻️ Restarting.", buttons: [], edit: false })
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  status.stream = "open"
})

afterEach(() => vi.useRealTimers())

describe("OpsPage", () => {
  it("shows the masked config", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/config")
    expect(w.find(".code-block").text()).toContain("api_key: «masked»")
  })

  it("checks for an update and updates after a confirm", async () => {
    const w = await mk()
    expect(btn(w, "Update and restart").attributes("disabled")).toBeDefined()
    await btn(w, "Check for update").trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/update")
    expect(w.text()).toContain("v0.24.0 is available")
    await btn(w, "Update and restart").trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Update to v0.24.0 and restart?",
      body: "The gateway downloads the release, checks its SHA-256, runs it once, swaps the binary, and restarts. Running turns finish first.",
      confirmLabel: "Update and restart",
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/update", { confirm: true })
    expect(w.text()).toContain("Restarting…")
  })

  it("refuses an update of a binary that is not writable", async () => {
    update = { ...update, writable: false }
    const w = await mk()
    await btn(w, "Check for update").trigger("click")
    await flushPromises()
    expect(w.text()).toContain(
      "The binary at /home/o/.local/bin/tether is not writable by this user. Run `tether update` as a user who can write it.",
    )
    expect(btn(w, "Update and restart").attributes("disabled")).toBeDefined()
  })

  it("creates a backup, polls the task, and offers the download", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    const w = await mk()
    await btn(w, "Create backup").trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/backup")
    expect(api).toHaveBeenCalledWith("GET", "/api/tasks/t1")
    expect(w.text()).toContain("Saved tether-backup-2026-11-20.tar.gz (13002342 bytes).")
    expect(w.text()).toContain("tether-backup-2026-11-20.tar.gz")
    expect(btn(w, "Download (12.4 MB)")).toBeTruthy()
  })

  it("shows the error text of a failed backup task", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    const base = vi.mocked(api).getMockImplementation()!
    vi.mocked(api).mockImplementation(async (m: string, p: string, ...r: any[]) =>
      p === "/api/tasks/t1"
        ? ({ id: "t1", kind: "backup", state: "error", text: "disk full" } as any)
        : base(m as any, p, ...r),
    )
    const w = await mk()
    await btn(w, "Create backup").trigger("click")
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("disk full", "error")
    expect(btn(w, "Create backup").attributes("disabled")).toBeUndefined()
  })

  it("closes the restart overlay when the update task fails", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
    const base = vi.mocked(api).getMockImplementation()!
    vi.mocked(api).mockImplementation(async (m: string, p: string, ...r: any[]) =>
      p === "/api/tasks/t2"
        ? ({ id: "t2", kind: "update", state: "error", text: "checksum mismatch" } as any)
        : base(m as any, p, ...r),
    )
    const w = await mk()
    await btn(w, "Check for update").trigger("click")
    await flushPromises()
    await btn(w, "Update and restart").trigger("click")
    await flushPromises()
    expect(w.text()).toContain("Restarting…")
    expect(w.find(".state .spin").exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()
    expect(toast).toHaveBeenCalledWith("checksum mismatch", "error")
    expect(w.text()).not.toContain("Restarting…")
  })

  it("marks the active tab with is-on", async () => {
    const w = await mk()
    expect(
      w
        .findAll("a.tab")
        .find((a) => a.text() === "Operations")!
        .classes(),
    ).toContain("is-on")
  })

  it("restarts after a confirm and waits for the stream", async () => {
    const w = await mk()
    await btn(w, "Restart the gateway").trigger("click")
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith({
      title: "Restart the gateway?",
      body: "Running turns finish first (up to 180 s). This page reconnects by itself.",
      confirmLabel: "Restart",
    })
    expect(runCommand).toHaveBeenCalledWith("/restart")
    expect(w.text()).toContain("Restarting…")
    status.stream = "reconnecting"
    await flushPromises()
    status.stream = "open"
    await flushPromises()
    expect(w.text()).not.toContain("Restarting…")
  })

  it("revokes the download URL after a delay, not at once", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"],
    })
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
      const w = await mk()
      await btn(w, "Create backup").trigger("click")
      await vi.advanceTimersByTimeAsync(2000)
      await flushPromises()
      await btn(w, "Download (12.4 MB)").trigger("click")
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
    }
  })
})
