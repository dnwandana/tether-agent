import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { flushPromises } from "@vue/test-utils"
import { defineComponent, h as hh, type PropType } from "vue"
import { cleanup, render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import type { ResultNoteData } from "../lib/command"
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
import OpsPage from "./OpsPage.vue"
import { fullTime, shortTime } from "../lib/time"

const Blank = { render: () => null }
const local = (hour: number, min: number) => new Date(2026, 10, 20, hour, min).toISOString()
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

const NoteStub = defineComponent({
  props: { note: { type: Object as PropType<ResultNoteData | null>, default: null } },
  setup: (p) => () =>
    p.note
      ? hh("output", { "data-title": p.note.title, "data-kind": p.note.kind }, p.note.text)
      : null,
})
const notes = () =>
  [...document.querySelectorAll("output")].map((o) => ({
    title: (o as HTMLElement).dataset.title,
    kind: (o as HTMLElement).dataset.kind,
    text: o.textContent,
  }))

// Renders the page on /system/ops with today's two routes (system: Blank, ops: OpsPage).
async function mk(user = userEvent.setup()) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/system", name: "system", component: Blank },
      { path: "/system/ops", name: "ops", component: OpsPage },
    ],
  })
  await router.push("/system/ops")
  const r = render(OpsPage, {
    global: {
      plugins: [router],
      stubs: { LogViewer: true, LiveHeader: true, ResultNote: NoteStub },
    },
  })
  await flushPromises()
  return Object.assign(r, { router, user })
}
const button = (name: string) => screen.getByRole("button", { name })
const backupRows = () => within(screen.getByRole("table")).getAllByRole("row").slice(1)
const upd = () => button("Update and restart") as HTMLButtonElement
const bodyText = () => document.body.textContent ?? ""
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
  it("shows the backup date in the short format, on one line, with the full time in the title", async () => {
    await mk()
    const span = within(backupRows()[0]).getAllByRole("cell")[2].querySelector("[title]")!
    expect(span.textContent).toBe(shortTime(oldFile.modified))
    expect(span.textContent).toContain("09:00")
    expect(span.getAttribute("title")).toBe(fullTime(oldFile.modified))
    expect(span.classList).toContain("whitespace-nowrap")
  })

  it("shows the masked config and filters it by a top-level key", async () => {
    const { user } = await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/config")
    expect(screen.getByText(/api_key: «masked»/)).toBeTruthy()
    await user.selectOptions(screen.getByRole("combobox", { name: "Config group" }), "providers")
    const text = screen.getByText(/primary:/).textContent!
    expect(text).not.toContain("agent:")
    expect(text).not.toContain("telegram:")
  })

  it("checks for an update at load and shows the version and the check time", async () => {
    await mk()
    expect(api).toHaveBeenCalledWith("GET", "/api/bootstrap")
    expect(api).toHaveBeenCalledWith("GET", "/api/update")
    expect(bodyText()).toContain("tether v0.23.0")
    expect(bodyText()).toContain("v0.24.0 is available")
    expect(bodyText()).toContain("Checked 14:05")
  })

  it("shows the reason that Update and restart is disabled", async () => {
    update = { ...update, newer: false }
    await mk()
    expect((button("Update and restart") as HTMLButtonElement).disabled).toBe(true)
    expect(bodyText()).toContain("You have the newest version.")
    cleanup()
    update = { ...update, newer: true, writable: false }
    await mk()
    expect(bodyText()).toContain(
      "The binary at /home/o/.local/bin/tether is not writable by this user. Run `tether update` as a user who can write it.",
    )
  })

  it("updates after a danger confirm with the drain time, and shows a failure", async () => {
    const { user } = await mk()
    await user.click(button("Update and restart"))
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
    expect(bodyText()).not.toContain("Restarting…")
    h.follow.get("t2")!(
      task({ id: "t2", kind: "update", state: "error", text: "checksum mismatch" }),
    )
    await flushPromises()
    expect(notes()[0]).toMatchObject({ title: "Update", kind: "error", text: "checksum mismatch" })
  })

  it("stops an update follow that starts after the page unmounts", async () => {
    const { user, unmount } = await mk()
    let post!: (v: unknown) => void
    const base = vi.mocked(api).getMockImplementation()!
    vi.mocked(api).mockImplementation((m: string, p: string, ...rest: unknown[]) =>
      m === "POST" && p === "/api/update"
        ? (new Promise((r) => (post = r)) as never)
        : (base as (...a: unknown[]) => never)(m, p, ...rest),
    )
    await user.click(button("Update and restart"))
    await vi.waitFor(() => expect(post).toBeTypeOf("function"))
    unmount()
    post({ task_id: "t2" })
    await flushPromises()
    expect(h.follow.has("t2")).toBe(false)
  })

  it("restarts through useCommand after a danger confirm, with no overlay", async () => {
    const { user } = await mk()
    expect(bodyText()).toContain("Running turns finish first (up to 120 s).")
    await user.click(button("Restart the gateway"))
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
    expect(document.querySelector('[aria-busy="true"]')).toBeNull()
  })

  it("disables Restart while /restart runs", async () => {
    const hold: { end: (v: string) => void } = { end: () => {} }
    h.run.mockReturnValue(new Promise<string>((r) => (hold.end = r)))
    const { user } = await mk()
    await user.click(button("Restart the gateway"))
    await vi.waitFor(() =>
      expect((button("Restart the gateway") as HTMLButtonElement).disabled).toBe(true),
    )
    hold.end("ok")
    await vi.waitFor(() =>
      expect((button("Restart the gateway") as HTMLButtonElement).disabled).toBe(false),
    )
  })

  it("disables Update and restart until the task ends, and stops the follow on done", async () => {
    const { user } = await mk()
    await user.click(upd())
    await vi.waitFor(() => expect(h.follow.has("t2")).toBe(true))
    expect(upd().disabled).toBe(true)
    h.follow.get("t2")!(task({ id: "t2", kind: "update", state: "running" }))
    await flushPromises()
    expect(upd().disabled).toBe(true)
    h.follow.get("t2")!(task({ id: "t2", kind: "update", state: "done" }))
    await flushPromises()
    expect(h.follow.has("t2")).toBe(false)
    expect(upd().disabled).toBe(false)
  })

  it("lists the backups with the size, the date, and Download", async () => {
    await mk()
    expect(backupRows()).toHaveLength(1)
    expect(backupRows()[0].textContent).toContain("tether-backup-2026-11-19.tar.gz")
    expect(backupRows()[0].textContent).toContain("1.0 MB")
    expect(
      within(backupRows()[0])
        .getAllByRole("button")
        .map((b) => b.textContent?.trim()),
    ).toEqual(["Download"])
  })

  it("shows the report of a new backup and lists it", async () => {
    const { user } = await mk()
    await user.click(button("Create backup"))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/backup")
    expect((button("Create backup") as HTMLButtonElement).disabled).toBe(true)
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
    expect(notes()[0]).toMatchObject({
      title: "Backup",
      kind: "info",
      text: "Saved tether-backup-2026-11-20.tar.gz (13002342 bytes).\nSessions: 12 · Messages: 3,400 · Cron jobs: 4\nReplaced keys: providers.primary.api_key",
    })
    expect(backupRows()).toHaveLength(2)
    expect((button("Create backup") as HTMLButtonElement).disabled).toBe(false)
  })

  it("shows the error of a failed backup task", async () => {
    const { user } = await mk()
    await user.click(button("Create backup"))
    await flushPromises()
    h.follow.get("t1")!(task({ state: "error", text: "disk full" }))
    await flushPromises()
    expect(notes()[0]).toMatchObject({ title: "Backup", kind: "error", text: "disk full" })
    expect((button("Create backup") as HTMLButtonElement).disabled).toBe(false)
  })

  it("shows the lost text when the gateway restarted during a backup", async () => {
    const { user } = await mk()
    await user.click(button("Create backup"))
    await flushPromises()
    h.follow.get("t1")!(task({ state: "lost" }))
    await flushPromises()
    expect(notes()[0]).toMatchObject({
      text: "The gateway restarted. The result of the task is lost.",
    })
  })

  it("shows the restore steps as three numbered lines with the real file", async () => {
    const writeText = vi.fn(async () => {})
    const { user } = await mk()
    // user-event installs its own clipboard stub in setup, so replace it after mk().
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true })
    expect(screen.getByText(/^1\. systemctl --user stop tether\.service/).textContent).toBe(
      "1. systemctl --user stop tether.service\n2. tether restore ~/.tether/backups/tether-backup-2026-11-19.tar.gz\n3. systemctl --user start tether.service",
    )
    expect(bodyText()).toContain("The dashboard goes offline until step 3.")
    await user.click(button("Copy all 3"))
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith(
      "systemctl --user stop tether.service\ntether restore ~/.tether/backups/tether-backup-2026-11-19.tar.gz\nsystemctl --user start tether.service",
    )
  })

  it("shows the HTTPS note in place of Copy all 3 on plain HTTP", async () => {
    vi.mocked(isSecure).mockReturnValue(false)
    await mk()
    expect(screen.queryByRole("button", { name: "Copy all 3" })).toBeNull()
    expect(screen.getByText("Copy needs HTTPS or localhost.")).toBeTruthy()
  })

  it("marks the Operations tab with the tabs component", async () => {
    const { user, router } = await mk()
    const tabs = screen.getAllByRole("tab")
    expect(tabs.map((t) => t.textContent?.trim())).toEqual(["Health", "Operations"])
    expect(tabs[1].getAttribute("aria-selected")).toBe("true")
    await user.click(tabs[0])
    await flushPromises()
    expect(router.currentRoute.value.name).toBe("system")
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
      const { user, unmount } = await mk(userEvent.setup({ advanceTimers: vi.advanceTimersByTime }))
      await user.click(button("Download"))
      await vi.advanceTimersByTimeAsync(0)
      expect(click).toHaveBeenCalled()
      expect(revoke).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(30_000)
      expect(revoke).toHaveBeenCalledWith("blob:x")
      click.mockRestore()
      unmount()
    } finally {
      URL.createObjectURL = createURL
      URL.revokeObjectURL = revokeURL
      vi.unstubAllGlobals()
    }
  })

  it("pages the backups with their own stored size and keeps Download at the right", async () => {
    localStorage.setItem("tether.rows.backups", "10")
    backups = {
      dir: "~/.tether/backups",
      files: Array.from({ length: 12 }, (_, i) => ({
        name: `tether-backup-${i + 1}.tar.gz`,
        size: 1048576,
        modified: local(9, i),
      })),
    }
    const { container, user } = await mk()
    const nav = screen.getByRole("navigation", { name: "Backups pages" })
    expect(screen.getByText("1–10 of 12")).toBeTruthy()
    expect(backupRows()).toHaveLength(10)
    const cols = [...container.querySelectorAll("colgroup col")] as HTMLElement[]
    expect(cols[cols.length - 1].style.width).toBe("7rem")
    const cell = screen.getAllByRole("button", { name: "Download" })[0].closest("td")!
    expect(cell.classList).toContain("text-right")
    await user.click(within(nav).getByRole("button", { name: "Next Page" }))
    expect(screen.getByText("11–12 of 12")).toBeTruthy()
    expect(backupRows()).toHaveLength(2)
  })
})
