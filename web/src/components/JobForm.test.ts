import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

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

const field = (w: any, name: string) => w.find(`[data-field="${name}"]`)
const save = async (w: any) => {
  await w.find("form").trigger("submit")
  await flushPromises()
}
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

describe("JobForm", () => {
  it("creates a job with only the fields that differ from empty", async () => {
    const w = mount(JobForm, { props: { job: null } })
    await flushPromises()
    await field(w, "name").setValue("morning-digest")
    await field(w, "schedule").setValue("0 7 * * 1-5")
    await field(w, "prompt").setValue("Summarize unread GitHub notifications.")
    await field(w, "model").setValue("primary:glm-4.6")
    await field(w, "effort").setValue("low")
    await field(w, "skill-github").setValue(true)
    await save(w)
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
    expect(w.emitted("saved")).toHaveLength(1)
  })

  it("updates a job with only the changed fields and no prompt", async () => {
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    expect((field(w, "name").element as HTMLInputElement).value).toBe("gh-notify")
    await field(w, "name").setValue("gh-notify-2")
    await save(w)
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "update",
      job_id: "j2",
      name: "gh-notify-2",
    })
  })

  it("sends explicit empty values for the cleared fields", async () => {
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    await field(w, "deliver").setValue("")
    await field(w, "workdir").setValue("")
    await field(w, "model").setValue("")
    await field(w, "skill-github").setValue(false)
    await field(w, "toolset-web").setValue(false)
    await save(w)
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
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    await save(w)
    expect(posts()).toHaveLength(0)
    expect(w.text()).toContain("No changes.")
  })

  it("warns about a masked prompt and still saves", async () => {
    const w = mount(JobForm, { props: { job: job({ prompt: "Use ****.", prompt_masked: true }) } })
    await flushPromises()
    expect(w.text()).not.toContain("masked secret")
    await field(w, "prompt").setValue("Use ****. Then sum up.")
    expect(w.text()).toContain(
      "The prompt holds a masked secret. Type the full prompt again, or the secret is lost.",
    )
    expect(w.find("button[type='submit']").attributes("disabled")).toBeUndefined()
  })

  it("checks the required fields before a request", async () => {
    const w = mount(JobForm, { props: { job: null } })
    await flushPromises()
    await save(w)
    expect(w.text()).toContain("Enter a name.")
    expect(w.text()).toContain("Enter a schedule.")
    expect(w.text()).toContain("Enter a prompt, or turn on No agent.")
    expect(posts()).toHaveLength(0)
    await field(w, "name").setValue("disk")
    await field(w, "schedule").setValue("every 1h")
    await field(w, "no_agent").setValue(true)
    await field(w, "script").setValue("disk.sh")
    await save(w)
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "create",
      name: "disk",
      schedule: "every 1h",
      script: "disk.sh",
      no_agent: true,
    })
  })

  it("shows Saving… while the save runs", async () => {
    let done!: (v: unknown) => void
    vi.mocked(api).mockImplementation(async (m: string, p: string) =>
      m === "GET" ? (gets[p] as any) : (new Promise((r) => (done = r)) as any),
    )
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    await field(w, "name").setValue("x")
    await w.find("form").trigger("submit")
    expect(w.find("button[type='submit']").text()).toBe("Saving…")
    done({ request_id: "", text: "updated job j2\n", buttons: [], edit: false })
    await flushPromises()
    expect(w.find("button[type='submit']").text()).toBe("Save")
  })

  it("pauses the job after the save when Enabled is turned off", async () => {
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    await field(w, "enabled").setValue(false)
    await save(w)
    expect(posts()).toHaveLength(0)
    expect(runCommand).toHaveBeenCalledWith("/jobs pause j2")
    const c = mount(JobForm, { props: { job: null } })
    await flushPromises()
    await field(c, "name").setValue("morning-digest")
    await field(c, "schedule").setValue("0 7 * * 1-5")
    await field(c, "prompt").setValue("p")
    await field(c, "enabled").setValue(false)
    await save(c)
    expect(runCommand).toHaveBeenLastCalledWith("/jobs pause j9")
  })

  it("shows an error reply of the pause and does not say Saved", async () => {
    vi.mocked(runCommand).mockResolvedValue({
      request_id: "r",
      text: "❌ No such job.",
      buttons: [],
      edit: false,
    })
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    await field(w, "enabled").setValue(false)
    await save(w)
    expect(toast).toHaveBeenCalledWith("❌ No such job.", "error")
    expect(toast).not.toHaveBeenCalledWith("Saved the job.", "ok")
  })

  it("still reports the save when the pause after a new job throws", async () => {
    vi.mocked(runCommand).mockRejectedValueOnce(new Error("The gateway did not answer."))
    const c = mount(JobForm, { props: { job: null } })
    await flushPromises()
    await field(c, "name").setValue("morning-digest")
    await field(c, "schedule").setValue("0 7 * * 1-5")
    await field(c, "prompt").setValue("p")
    await field(c, "enabled").setValue(false)
    await save(c)
    expect(c.emitted("saved")).toHaveLength(1)
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
    const c = mount(JobForm, { props: { job: null } })
    await flushPromises()
    await field(c, "name").setValue("morning-digest")
    await field(c, "schedule").setValue("0 7 * * 1-5")
    await field(c, "prompt").setValue("p")
    await field(c, "enabled").setValue(false)
    await save(c)
    expect(runCommand).not.toHaveBeenCalled()
    expect(vi.mocked(toast).mock.calls.some((x) => x[1] === "warn")).toBe(true)
  })

  it("fills the pickers, the placeholders, and the deliver hint", async () => {
    const w = mount(JobForm, { props: { job: null } })
    await flushPromises()
    expect(w.findAll("datalist option").map((o) => o.attributes("value"))).toEqual([
      "primary:glm-4.6",
    ])
    expect(field(w, "skill-notes").exists()).toBe(true)
    expect(field(w, "toolset-terminal").exists()).toBe(true)
    expect(field(w, "name").attributes("placeholder")).toBe("e.g. morning-digest")
    expect(field(w, "model").attributes("placeholder")).toBe("e.g. default")
    expect(field(w, "deliver").attributes("placeholder")).toBeUndefined()
    expect(w.text()).toContain("Empty sends to telegram:555.")
  })

  it("filters the skills with the search box and shows the selected skills as chips", async () => {
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    expect(w.findAll(".skill-chips button").map((b) => b.text())).toEqual(["github ✕"])
    await field(w, "skill-search").setValue("NOT")
    expect(field(w, "skill-notes").exists()).toBe(true)
    expect(field(w, "skill-github").exists()).toBe(false)
    await field(w, "skill-search").setValue("zzz")
    expect(w.text()).toContain("No skill matches.")
    await field(w, "skill-search").trigger("keydown", { key: "Enter" })
    await flushPromises()
    expect(posts()).toHaveLength(0)
    await w.find(".skill-chips button").trigger("click")
    expect(w.find(".skill-chips").exists()).toBe(false)
    await save(w)
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "update",
      job_id: "j2",
      skills: [],
    })
  })

  it("shows the next run in the short format", async () => {
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    expect(w.text()).toContain("Next run: " + shortTime("2026-11-24T07:00:00Z"))
  })

  it("does not show the deliver hint when the default is not known", async () => {
    vi.mocked(api).mockImplementation(async (m: string, p: string) => {
      if (p === "/api/brief") throw new Error("down")
      return (m === "GET" ? gets[p] : {}) as any
    })
    const w = mount(JobForm, { props: { job: null } })
    await flushPromises()
    expect(w.text()).not.toContain("Empty sends to")
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
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    await field(w, "schedule").setValue("every 30s")
    await save(w)
    expect(w.find(".c-bad").text()).toBe(
      'cronjob: schedule "every 30s": the interval must be at least 1 minute.',
    )
    expect(w.emitted("saved")).toBeUndefined()
  })

  it("emits dirty after an edit and emits cancel", async () => {
    const w = mount(JobForm, { props: { job: job() } })
    await flushPromises()
    await field(w, "name").setValue("y")
    expect(w.emitted("dirty")?.at(-1)).toEqual([true])
    await w
      .findAll("button")
      .find((b) => b.text() === "Cancel")!
      .trigger("click")
    expect(w.emitted("cancel")).toHaveLength(1)
  })
})
