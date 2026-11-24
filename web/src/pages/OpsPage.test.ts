import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"
import { createRouter, createMemoryHistory } from "vue-router"

const h = vi.hoisted(() => ({
  follow: new Map<string, (t: unknown) => void>(),
  run: vi.fn(),
  live: null as null | {
    load: () => Promise<void>
    opts?: { events?: string[]; filter?: (n: string, d: unknown) => boolean }
  },
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
vi.mock("../lib/command", async () => {
  const actual = await vi.importActual<object>("../lib/command")
  const { ref } = await import("vue")
  return {
    ...actual,
    useCommand: () => ({ run: h.run, reply: ref(null), closeReply: () => {} }),
  }
})
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
    followTask: (id: string, fn: (t: unknown) => void) => {
      h.follow.set(id, fn)
      return () => h.follow.delete(id)
    },
  }
})
import { api } from "../api/client"
import { confirm, isSecure, toast } from "../stores/ui"
import ResultNote from "../components/ResultNote.vue"
import OpsPage from "./OpsPage.vue"

const Blank = { render: () => null }
const local = (hh: number, mm: number) => new Date(2026, 10, 20, hh, mm).toISOString()
let update: Record<string, unknown>
let backups: { dir: string; files: { name: string; size: number; modified: string }[] }
const newFile = {
  name: "tether-backup-2026-11-20.tar.gz",
  size: 13002342,
  modified: local(14, 0),
}
const oldFile = { name: "tether-backup-2026-11-19.tar.gz", size: 1048576, modified: local(9, 0) }
const config =
  "agent:\n  model: glm-4.6\nproviders:\n  primary:\n    api_key: «masked»\ntelegram:\n  enabled: true\n"

async function mk() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/system", name: "system", component: Blank },
      { path: "/system/ops", name: "ops", component: OpsPage },
    ],
  })
  await router.push("/system/ops")
  const w = mount(OpsPage, {
    global: { plugins: [router], stubs: { LogViewer: true, LiveHeader: true } },
  })
  await flushPromises()
  return Object.assign(w, { router })
}
const btn = (w: any, t: string) => w.findAll("button").find((b: any) => b.text() === t)!
const notes = (w: any) =>
  w
    .findAllComponents(ResultNote)
    .map((n: any) => n.props("note"))
    .filter(Boolean)
const task = (o: object) => ({
  id: "t1",
  kind: "backup",
  state: "running",
  text: "",
  subject: "",
  code: "",
  waiting: false,
  ...o,
})

beforeEach(() => {
  h.follow.clear()
  h.live = null
  h.run.mockReset().mockResolvedValue("ok")
  update = {
    current: "v0.23.0",
    latest: "v0.24.0",
    newer: true,
    notes_url: "https://github.com/x/releases/tag/v0.24.0",
    writable: true,
    path: "/home/o/.local/bin/tether",
    checked_at: local(14, 5),
  }
  backups = { dir: "~/.tether/backups", files: [oldFile] }
  vi.mocked(api)
    .mockReset()
    .mockImplementation(async (m: string, p: string) => {
      if (p === "/api/config") return { path: "~/.tether/config.yaml", text: config } as any
      if (p === "/api/bootstrap")
        return {
          commands: [],
          skills: [],
          version: "v0.23.0",
          build_id: "b",
          max_upload_mb: 20,
          tls: false,
          drain_s: 120,
          toolsets: [],
        } as any
      if (p === "/api/update" && m === "GET") return update as any
      if (p === "/api/update" && m === "POST") return { task_id: "t2" } as any
      if (p === "/api/backup" && m === "POST") {
        backups = { dir: backups.dir, files: [newFile, oldFile] }
        return { task_id: "t1" } as any
      }
      if (p === "/api/backup") return backups as any
      return {} as any
    })
  vi.mocked(confirm).mockReset().mockResolvedValue(true)
  vi.mocked(toast).mockReset()
  vi.mocked(isSecure).mockReturnValue(true)
})

afterEach(() => vi.useRealTimers())

describe("OpsPage", () => {
  it("shows the masked config and filters it by a top-level key", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/config")
    expect(w.find(".config-view").text()).toContain("api_key: «masked»")
    await w.find('select[aria-label="Config group"]').setValue("providers")
    const text = w.find(".config-view").text()
    expect(text).toContain("primary:")
    expect(text).not.toContain("agent:")
    expect(text).not.toContain("telegram:")
  })

  it("checks for an update at load and shows the version and the check time", async () => {
    const w = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/bootstrap")
    expect(api).toHaveBeenCalledWith("GET", "/api/update")
    expect(w.text()).toContain("tether v0.23.0")
    expect(w.text()).toContain("v0.24.0 is available")
    expect(w.text()).toContain("Checked 14:05")
  })

  it("shows the reason that Update and restart is disabled", async () => {
    update = { ...update, newer: false }
    let w = await mk()
    expect(btn(w, "Update and restart").attributes("disabled")).toBeDefined()
    expect(w.text()).toContain("You have the newest version.")
    update = { ...update, newer: true, writable: false }
    w = await mk()
    expect(w.text()).toContain(
      "The binary at /home/o/.local/bin/tether is not writable by this user. Run `tether update` as a user who can write it.",
    )
  })

  it("updates after a danger confirm with the drain time, and shows a failure", async () => {
    const w = await mk()
    await btn(w, "Update and restart").trigger("click")
    await vi.waitFor(() =>
      expect(api).toHaveBeenCalledWith("POST", "/api/update", { confirm: true }),
    )
    await vi.waitFor(() => expect(h.follow.has("t2")).toBe(true))
    expect(confirm).toHaveBeenCalledWith({
      title: "Update to v0.24.0 and restart?",
      body: "The gateway downloads the release, checks its SHA-256, runs it once, swaps the binary, and restarts. Running turns finish first (up to 120 s).",
      confirmLabel: "Update and restart",
      danger: true,
    })
    expect(api).toHaveBeenCalledWith("POST", "/api/update", { confirm: true })
    expect(w.text()).not.toContain("Restarting…")
    h.follow.get("t2")!(
      task({ id: "t2", kind: "update", state: "error", text: "checksum mismatch" }),
    )
    await flushPromises()
    expect(notes(w)[0]).toMatchObject({ title: "Update", kind: "error", text: "checksum mismatch" })
  })

  it("stops an update follow that starts after the page unmounts", async () => {
    const w = await mk()
    let post!: (v: unknown) => void
    const base = vi.mocked(api).getMockImplementation()!
    vi.mocked(api).mockImplementation((m: string, p: string, ...rest: unknown[]) =>
      m === "POST" && p === "/api/update"
        ? (new Promise((r) => (post = r)) as never)
        : (base as (...a: unknown[]) => never)(m, p, ...rest),
    )
    await btn(w, "Update and restart").trigger("click")
    await vi.waitFor(() => expect(post).toBeTypeOf("function"))
    w.unmount()
    post({ task_id: "t2" })
    await flushPromises()
    expect(h.follow.has("t2")).toBe(false)
  })

  it("restarts through useCommand after a danger confirm, with no overlay", async () => {
    const w = await mk()
    expect(w.text()).toContain("Running turns finish first (up to 120 s).")
    await btn(w, "Restart the gateway").trigger("click")
    await vi.waitFor(() =>
      expect(h.run).toHaveBeenCalledWith("/restart", {
        result: expect.anything(),
        title: "Restart",
      }),
    )
    expect(confirm).toHaveBeenCalledWith({
      title: "Restart the gateway?",
      body: "Running turns finish first (up to 120 s). This page reconnects by itself.",
      confirmLabel: "Restart",
      danger: true,
    })
    expect(w.find(".state .spin").exists()).toBe(false)
  })

  it("disables Restart while /restart runs", async () => {
    const hold: { end: (v: string) => void } = { end: () => {} }
    h.run.mockReturnValue(new Promise<string>((r) => (hold.end = r)))
    const w = await mk()
    await btn(w, "Restart the gateway").trigger("click")
    await vi.waitFor(() =>
      expect(btn(w, "Restart the gateway").attributes("disabled")).toBeDefined(),
    )
    hold.end("ok")
    await vi.waitFor(() =>
      expect(btn(w, "Restart the gateway").attributes("disabled")).toBeUndefined(),
    )
  })

  it("disables Update and restart until the task ends, and stops the follow on done", async () => {
    const w = await mk()
    await btn(w, "Update and restart").trigger("click")
    await vi.waitFor(() => expect(h.follow.has("t2")).toBe(true))
    expect(btn(w, "Update and restart").attributes("disabled")).toBeDefined()
    h.follow.get("t2")!(task({ id: "t2", kind: "update", state: "running" }))
    await flushPromises()
    expect(btn(w, "Update and restart").attributes("disabled")).toBeDefined()
    h.follow.get("t2")!(task({ id: "t2", kind: "update", state: "done" }))
    await flushPromises()
    expect(h.follow.has("t2")).toBe(false)
    expect(btn(w, "Update and restart").attributes("disabled")).toBeUndefined()
  })

  it("lists the backups with the size, the date, and Download", async () => {
    const w = await mk()
    const rows = w.findAll(".backups tbody tr")
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain("tether-backup-2026-11-19.tar.gz")
    expect(rows[0].text()).toContain("1.0 MB")
    expect(rows[0].findAll("button").map((b: any) => b.text())).toEqual(["Download"])
  })

  it("shows the report of a new backup and lists it", async () => {
    const w = await mk()
    await btn(w, "Create backup").trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/backup")
    expect(btn(w, "Create backup").attributes("disabled")).toBeDefined()
    h.follow.get("t1")!(
      task({
        state: "done",
        text: "Saved tether-backup-2026-11-20.tar.gz (13002342 bytes).",
        backup: {
          size: 13002342,
          sessions: 12,
          messages: 3400,
          cron_jobs: 4,
          replaced: ["providers.primary.api_key"],
          warnings: [],
          skipped: [],
        },
      }),
    )
    await flushPromises()
    expect(notes(w)[0]).toMatchObject({
      title: "Backup",
      kind: "info",
      text: "Saved tether-backup-2026-11-20.tar.gz (13002342 bytes).\nSessions: 12 · Messages: 3,400 · Cron jobs: 4\nReplaced keys: providers.primary.api_key",
    })
    expect(w.findAll(".backups tbody tr")).toHaveLength(2)
    expect(btn(w, "Create backup").attributes("disabled")).toBeUndefined()
  })

  it("shows the error of a failed backup task", async () => {
    const w = await mk()
    await btn(w, "Create backup").trigger("click")
    await flushPromises()
    h.follow.get("t1")!(task({ state: "error", text: "disk full" }))
    await flushPromises()
    expect(notes(w)[0]).toMatchObject({ title: "Backup", kind: "error", text: "disk full" })
    expect(btn(w, "Create backup").attributes("disabled")).toBeUndefined()
  })

  it("shows the lost text when the gateway restarted during a backup", async () => {
    const w = await mk()
    await btn(w, "Create backup").trigger("click")
    await flushPromises()
    h.follow.get("t1")!(task({ state: "lost" }))
    await flushPromises()
    expect(notes(w)[0]).toMatchObject({
      text: "The gateway restarted. The result of the task is lost.",
    })
  })

  it("shows the restore steps as three numbered lines with the real file", async () => {
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
    const w = await mk()
    expect(w.find(".restore-steps").text()).toBe(
      "1. systemctl --user stop tether.service\n2. tether restore ~/.tether/backups/tether-backup-2026-11-19.tar.gz\n3. systemctl --user start tether.service",
    )
    expect(w.text()).toContain("The dashboard goes offline until step 3.")
    await btn(w, "Copy all 3").trigger("click")
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith(
      "systemctl --user stop tether.service\ntether restore ~/.tether/backups/tether-backup-2026-11-19.tar.gz\nsystemctl --user start tether.service",
    )
  })

  it("shows the HTTPS note in place of Copy all 3 on plain HTTP", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    const w = await mk()
    expect(btn(w, "Copy all 3")).toBeUndefined()
    expect(w.text()).toContain("Copy needs HTTPS or localhost.")
  })

  it("marks the Operations tab with the tabs component", async () => {
    const w = await mk()
    const tabs = w.findAll('[role="tab"]')
    expect(tabs.map((t: any) => t.text())).toEqual(["Health", "Operations"])
    expect(tabs[1].attributes("aria-selected")).toBe("true")
    await tabs[0].trigger("click")
    await flushPromises()
    expect(w.router.currentRoute.value.name).toBe("system")
  })

  it("reloads on the end of a backup or update task and on a restart event", async () => {
    await mk()
    expect(h.live!.opts!.events).toEqual(["task", "restart"])
    const f = h.live!.opts!.filter!
    expect(f("task", task({ state: "done" }))).toBe(true)
    expect(f("task", task({ kind: "update", state: "error" }))).toBe(true)
    expect(f("task", task({ state: "running" }))).toBe(false)
    expect(f("task", task({ kind: "doctor", state: "done" }))).toBe(false)
    expect(f("restart", { reason: "stopping" })).toBe(true)
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
      await btn(w, "Download").trigger("click")
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
