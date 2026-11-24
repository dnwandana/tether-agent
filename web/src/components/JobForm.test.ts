import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within } from "@testing-library/vue"
import userEvent from "@testing-library/user-event"
import { flushPromises } from "@vue/test-utils"
import { defineComponent, h, nextTick } from "vue"

vi.mock("../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/client")>()),
  api: vi.fn(),
  runCommand: vi.fn(),
}))
vi.mock("../stores/ui", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../stores/ui")>()),
  toast: vi.fn(),
  confirm: vi.fn(),
}))
import { api, runCommand, ApiError } from "../api/client"
import { toast } from "../stores/ui"
import { shortTime } from "../lib/time"
import JobForm from "./JobForm.vue"
import SidePanel from "./SidePanel.vue"

const posts = () => vi.mocked(api).mock.calls.filter((c) => c[0] === "POST")
const created =
  'created job j9\n- j9 · morning-digest · enabled · agent · schedule "0 7 * * 1-5" · next run 2026-11-24 07:00 · deliver origin\n'
const gets: Record<string, unknown> = {
  "/api/models": {
    entries: [{ name: "primary", model: "glm-4.6" }],
    chain: [],
    health: [],
    roles: [],
  },
  "/api/skills": [{ name: "github" }, { name: "notes" }],
  "/api/bootstrap": { toolsets: ["web", "terminal", "file"] },
  "/api/brief": { default_deliver: "telegram:555" },
}
const job = (o: object = {}) =>
  ({
    job: {
      id: "j2",
      name: "gh-notify",
      schedule: "0 7 * * 1-5",
      enabled: true,
      watch: true,
      draft_only: false,
      next_run: "2026-11-24T07:00:00Z",
    },
    prompt: "p",
    prompt_masked: false,
    script: "",
    no_agent: false,
    repeat_left: 0,
    deliver: "telegram:123",
    model: "primary:glm-4.6",
    effort: "",
    skills: ["github"],
    toolsets: ["web"],
    workdir: "/srv",
    continuity: true,
    ...o,
  }) as any

// Renders one form and gives helpers that act inside it only.
async function mk(j: unknown) {
  const r = render(JobForm, { props: { job: j as any } })
  await flushPromises()
  const q = within(r.container)
  const u = userEvent.setup()
  const fill = async (label: string, text: string) => {
    const el = q.getByLabelText(label)
    await u.clear(el)
    if (text !== "") await u.type(el, text)
  }
  const tick = (name: string) => u.click(q.getByRole("checkbox", { name }))
  const submit = () => u.click(q.getByRole("button", { name: "Save" }))
  const save = async () => {
    await submit()
    await flushPromises()
  }
  return { ...r, q, u, fill, tick, submit, save }
}

beforeEach(() => {
  vi.mocked(api)
    .mockReset()
    .mockImplementation(
      async (m: string, p: string) =>
        (m === "GET"
          ? gets[p]
          : { request_id: "", text: created, buttons: [], edit: false }) as any,
    )
  vi.mocked(runCommand)
    .mockReset()
    .mockResolvedValue({ request_id: "r", text: "⏸ Paused.", buttons: [], edit: false })
  vi.mocked(toast).mockReset()
})
afterEach(() => vi.unstubAllGlobals())

describe("JobForm", () => {
  it("creates a job with only the fields that differ from empty", async () => {
    const w = await mk(null)
    await w.fill("Name", "morning-digest")
    await w.fill("Schedule", "0 7 * * 1-5")
    await w.fill("Prompt", "Summarize unread GitHub notifications.")
    await w.fill("Model", "primary:glm-4.6")
    await w.u.selectOptions(w.q.getByRole("combobox", { name: "Effort" }), "low")
    await w.tick("github")
    await w.save()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "create",
      name: "morning-digest",
      schedule: "0 7 * * 1-5",
      prompt: "Summarize unread GitHub notifications.",
      effort: "low",
      skills: ["github"],
      model: { provider: "primary", model: "glm-4.6" },
    })
    expect(toast).toHaveBeenCalledWith("Saved the job. Next run: 2026-11-24 07:00.", "ok")
    expect(w.emitted().saved).toHaveLength(1)
  })

  it("updates a job with only the changed fields and no prompt", async () => {
    const w = await mk(job())
    expect((w.q.getByLabelText("Name") as HTMLInputElement).value).toBe("gh-notify")
    await w.fill("Name", "gh-notify-2")
    await w.save()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "update",
      job_id: "j2",
      name: "gh-notify-2",
    })
  })

  it("shows 0 for a job that repeats with no end, and does not send the repeat again", async () => {
    // The server keeps -1 for a job with no end. The form shows 0, which means the same.
    const w = await mk(job({ repeat_left: -1 }))
    const repeat = w.q.getByLabelText("Repeat") as HTMLInputElement
    expect(repeat.value).toBe("0")
    expect(repeat.checkValidity()).toBe(true)
    await w.fill("Name", "gh-notify-2")
    await w.save()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "update",
      job_id: "j2",
      name: "gh-notify-2",
    })
  })

  it("sends explicit empty values for the cleared fields", async () => {
    const w = await mk(job())
    await w.fill("Deliver to", "")
    await w.fill("Workdir", "")
    await w.fill("Model", "")
    await w.tick("github")
    await w.tick("web")
    await w.save()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "update",
      job_id: "j2",
      deliver: "",
      workdir: "",
      toolsets: [],
      skills: [],
      model: { provider: "", model: "" },
    })
  })

  it("calls no server when nothing changed", async () => {
    const w = await mk(job())
    await w.save()
    expect(posts()).toHaveLength(0)
    expect(w.q.getByRole("status").textContent!.trim()).toBe("No changes.")
  })

  it("warns about a masked prompt and still saves", async () => {
    const w = await mk(job({ prompt: "Use ****.", prompt_masked: true }))
    expect(w.container.textContent).not.toContain("masked secret")
    await w.fill("Prompt", "Use ****. Then sum up.")
    expect(w.q.getByRole("status").textContent!.trim()).toBe(
      "The prompt holds a masked secret. Type the full prompt again, or the secret is lost.",
    )
    expect(w.q.getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(false)
  })

  it("checks the required fields before a request", async () => {
    const w = await mk(null)
    await w.save()
    expect(w.q.getByText("Enter a name.").getAttribute("data-slot")).toBe("field-error")
    expect(w.q.getByText("Enter a schedule.")).toBeTruthy()
    expect(w.q.getByText("Enter a prompt, or turn on No agent.")).toBeTruthy()
    const name = w.q.getByLabelText("Name")
    expect(name.getAttribute("aria-invalid")).toBe("true")
    expect(name.closest('[data-slot="field"]')!.getAttribute("data-invalid")).toBe("true")
    expect(posts()).toHaveLength(0)
    await w.fill("Name", "disk")
    await w.fill("Schedule", "every 1h")
    await w.tick("No agent: run only the script")
    await w.fill("Script", "disk.sh")
    await w.save()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "create",
      name: "disk",
      schedule: "every 1h",
      script: "disk.sh",
      no_agent: true,
    })
    expect(w.q.getByLabelText("Name").getAttribute("aria-invalid")).not.toBe("true")
  })

  it("shows Saving… while the save runs", async () => {
    let done!: (v: unknown) => void
    vi.mocked(api).mockImplementation(async (m: string, p: string) =>
      m === "GET" ? (gets[p] as any) : (new Promise((r) => (done = r)) as any),
    )
    const w = await mk(job())
    await w.fill("Name", "x")
    await w.submit()
    const busy = w.q.getByRole("button", { name: "Saving…" })
    expect(busy.hasAttribute("disabled")).toBe(true)
    done({ request_id: "", text: "updated job j2\n", buttons: [], edit: false })
    await flushPromises()
    expect(w.q.getByRole("button", { name: "Save" }).hasAttribute("disabled")).toBe(false)
  })

  it("pauses the job after the save when Enabled is turned off", async () => {
    const w = await mk(job())
    await w.tick("Enabled")
    await w.save()
    expect(posts()).toHaveLength(0)
    expect(runCommand).toHaveBeenCalledWith("/jobs pause j2")
    const c = await mk(null)
    await c.fill("Name", "morning-digest")
    await c.fill("Schedule", "0 7 * * 1-5")
    await c.fill("Prompt", "p")
    await c.tick("Enabled")
    await c.save()
    expect(runCommand).toHaveBeenLastCalledWith("/jobs pause j9")
  })

  it("shows an error reply of the pause and does not say Saved", async () => {
    vi.mocked(runCommand).mockResolvedValue({
      request_id: "r",
      text: "❌ No such job.",
      buttons: [],
      edit: false,
    })
    const w = await mk(job())
    await w.tick("Enabled")
    await w.save()
    expect(toast).toHaveBeenCalledWith("❌ No such job.", "error")
    expect(toast).not.toHaveBeenCalledWith("Saved the job.", "ok")
  })

  it("still reports the save when the pause after a new job throws", async () => {
    vi.mocked(runCommand).mockRejectedValueOnce(new Error("The gateway did not answer."))
    const c = await mk(null)
    await c.fill("Name", "morning-digest")
    await c.fill("Schedule", "0 7 * * 1-5")
    await c.fill("Prompt", "p")
    await c.tick("Enabled")
    await c.save()
    expect(c.emitted().saved).toHaveLength(1)
    expect(toast).toHaveBeenCalledWith(
      expect.stringMatching(/^Saved the job\. Its Enabled state did not change: /),
      "warn",
    )
  })

  it("warns when the id of the new job cannot be read", async () => {
    vi.mocked(api).mockImplementation(
      async (m: string, p: string) =>
        (m === "GET" ? gets[p] : { request_id: "", text: "ok", buttons: [], edit: false }) as any,
    )
    const c = await mk(null)
    await c.fill("Name", "morning-digest")
    await c.fill("Schedule", "0 7 * * 1-5")
    await c.fill("Prompt", "p")
    await c.tick("Enabled")
    await c.save()
    expect(runCommand).not.toHaveBeenCalled()
    expect(vi.mocked(toast).mock.calls.some((x) => x[1] === "warn")).toBe(true)
  })

  it("fills the pickers, the placeholders, and the deliver hint", async () => {
    const w = await mk(null)
    expect(
      Array.from(w.container.querySelectorAll("datalist option")).map((o) =>
        o.getAttribute("value"),
      ),
    ).toEqual(["primary:glm-4.6"])
    expect(w.q.getByRole("checkbox", { name: "notes" }).getAttribute("data-slot")).toBe("checkbox")
    expect(w.q.getByRole("checkbox", { name: "terminal" })).toBeTruthy()
    expect(w.q.getByLabelText("Name").getAttribute("placeholder")).toBe("e.g. morning-digest")
    expect(w.q.getByLabelText("Model").getAttribute("placeholder")).toBe("e.g. default")
    expect(w.q.getByLabelText("Deliver to").getAttribute("placeholder")).toBeNull()
    expect(w.q.getByText("Empty sends to telegram:555.")).toBeTruthy()
    expect(w.q.getByRole("group", { name: "Skills" }).tagName).toBe("FIELDSET")
    expect(w.q.getByRole("group", { name: "Toolsets" }).tagName).toBe("FIELDSET")
  })

  it("filters the skills with the search box and shows the selected skills as chips", async () => {
    const w = await mk(job())
    const chips = () =>
      w.q.queryAllByRole("button", { name: /^Remove / }).map((b) => b.textContent!.trim())
    expect(chips()).toEqual(["github ✕"])
    const search = w.q.getByRole("searchbox", { name: "Search skills" })
    await w.u.type(search, "NOT")
    expect(w.q.getByRole("checkbox", { name: "notes" })).toBeTruthy()
    expect(w.q.queryByRole("checkbox", { name: "github" })).toBeNull()
    await w.u.clear(search)
    await w.u.type(search, "zzz")
    expect(w.q.getByText("No skill matches.")).toBeTruthy()
    await w.u.type(search, "{Enter}")
    await flushPromises()
    expect(posts()).toHaveLength(0)
    await w.u.click(w.q.getByRole("button", { name: "Remove github" }))
    expect(chips()).toEqual([])
    await w.save()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "update",
      job_id: "j2",
      skills: [],
    })
  })

  it("shows the next run in the short format", async () => {
    const w = await mk(job())
    expect(w.container.textContent).toContain("Next run: " + shortTime("2026-11-24T07:00:00Z"))
  })

  it("does not show the deliver hint when the default is not known", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (p === "/api/brief") throw new Error("down")
      return (m === "GET" ? gets[p] : {}) as any
    })
    const w = await mk(null)
    expect(w.container.textContent).not.toContain("Empty sends to")
  })

  it("shows the tool error and stays open", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (m === "GET") return gets[p] as any
      throw Object.assign(Object.create(ApiError.prototype), {
        status: 400,
        serverText: 'cronjob: schedule "every 30s": the interval must be at least 1 minute.',
        message: 'cronjob: schedule "every 30s": the interval must be at least 1 minute.',
      })
    })
    const w = await mk(job())
    await w.fill("Schedule", "every 30s")
    await w.save()
    expect(w.q.getByRole("alert").textContent!.trim()).toBe(
      'cronjob: schedule "every 30s": the interval must be at least 1 minute.',
    )
    expect(w.emitted().saved).toBeUndefined()
  })

  it("emits dirty after an edit and emits cancel", async () => {
    const w = await mk(job())
    await w.fill("Name", "y")
    expect(w.emitted().dirty?.at(-1)).toEqual([true])
    await w.u.click(w.q.getByRole("button", { name: "Cancel" }))
    expect(w.emitted().cancel).toHaveLength(1)
  })

  it("keeps the edits of a dirty form when a resize crosses 1440 px", async () => {
    let wide = false
    const fns: ((e: { matches: boolean }) => void)[] = []
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q === "(min-width: 1440px)" && wide,
      media: q,
      addEventListener: (_t: string, f: (e: { matches: boolean }) => void) => {
        if (q === "(min-width: 1440px)") fns.push(f)
      },
      removeEventListener() {},
      addListener() {},
      removeListener() {},
    }))
    const Host = defineComponent({
      setup: () => () =>
        h(SidePanel, { open: true, title: "Edit job", dirty: true }, () =>
          h(JobForm, { job: job() }),
        ),
    })
    // No teleport stub: the stub of @vue/test-utils mounts its children again when the mode changes.
    render(Host)
    await flushPromises()
    const u = userEvent.setup({ pointerEventsCheck: 0 })
    const name = screen.getByLabelText("Name") as HTMLInputElement
    await u.clear(name)
    await u.type(name, "edited")
    for (const on of [true, false]) {
      wide = on
      fns.forEach((f) => f({ matches: on }))
      await nextTick()
      const now = screen.getByLabelText("Name") as HTMLInputElement
      expect(now).toBe(name)
      expect(now.value).toBe("edited")
    }
  })
})
