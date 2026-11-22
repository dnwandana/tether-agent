import { describe, it, expect, vi, beforeEach } from "vitest"
import { mount, flushPromises } from "@vue/test-utils"

vi.mock("../api/client", () => ({
  api: vi.fn(),
  runCommand: vi.fn(),
  ApiError: class ApiError extends Error {
    status: number
    constructor(s: number, m: string) {
      super(m)
      this.status = s
    }
  },
}))
vi.mock("../stores/ui", () => ({ toast: vi.fn(), confirm: vi.fn() }))
import { api, ApiError } from "../api/client"
import JobForm from "./JobForm.vue"

const field = (w: any, label: string) => w.find(`[data-field="${label}"]`)

beforeEach(() => {
  vi.mocked(api).mockReset()
})
const ok = () => vi.mocked(api).mockResolvedValue({ text: "Created job morning-digest." })

describe("JobForm", () => {
  it("creates a job with the cronjob arguments", async () => {
    ok()
    const w = mount(JobForm, { props: { job: null } })
    await field(w, "name").setValue("morning-digest")
    await field(w, "schedule").setValue("0 7 * * 1-5")
    await field(w, "prompt").setValue("Summarize unread GitHub notifications.")
    await field(w, "model").setValue("primary:glm-4.6")
    await field(w, "effort").setValue("low")
    await field(w, "skills").setValue("github, ")
    await w
      .findAll("button")
      .find((b) => b.text() === "Save")!
      .trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "create",
      name: "morning-digest",
      schedule: "0 7 * * 1-5",
      prompt: "Summarize unread GitHub notifications.",
      effort: "low",
      watch: false,
      draft_only: false,
      continuity: false,
      skills: ["github"],
      model: { provider: "primary", model: "glm-4.6" },
    })
    expect(w.emitted("saved")).toHaveLength(1)
  })

  it("shows the tool error and stays open", async () => {
    vi.mocked(api)
      .mockReset()
      .mockRejectedValue(
        new (ApiError as any)(
          400,
          'cronjob: schedule "every 30s": the interval must be at least 1 minute.',
        ),
      )
    const w = mount(JobForm, { props: { job: null } })
    await field(w, "schedule").setValue("every 30s")
    await w
      .findAll("button")
      .find((b) => b.text() === "Save")!
      .trigger("click")
    await flushPromises()
    expect(w.find(".c-bad").text()).toBe(
      'cronjob: schedule "every 30s": the interval must be at least 1 minute.',
    )
    expect(w.emitted("saved")).toBeUndefined()
  })

  it("updates a job and keeps its values", async () => {
    const job = {
      job: { id: "j2", name: "gh-notify", schedule: "0 7 * * 1-5", watch: true, draft_only: false },
      prompt: "p",
      deliver: "telegram:123",
      model: "",
      effort: "",
      skills: ["github"],
      toolsets: ["web", "terminal"],
      workdir: "",
      continuity: true,
    } as any
    ok()
    const w = mount(JobForm, { props: { job } })
    expect((field(w, "name").element as HTMLInputElement).value).toBe("gh-notify")
    await w
      .findAll("button")
      .find((b) => b.text() === "Save")!
      .trigger("click")
    await flushPromises()
    expect(api).toHaveBeenCalledWith("POST", "/api/jobs", {
      action: "update",
      job_id: "j2",
      name: "gh-notify",
      schedule: "0 7 * * 1-5",
      prompt: "p",
      effort: "",
      watch: true,
      draft_only: false,
      continuity: true,
      skills: ["github"],
      deliver: "telegram:123",
      toolsets: ["web", "terminal"],
    })
  })

  it("emits cancel", async () => {
    const w = mount(JobForm, { props: { job: null } })
    await w
      .findAll("button")
      .find((b) => b.text() === "Cancel")!
      .trigger("click")
    expect(w.emitted("cancel")).toHaveLength(1)
  })
})
