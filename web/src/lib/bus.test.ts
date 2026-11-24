import { describe, expect, it, vi } from "vitest"
import { emitEvents, onEvent, onReconnected } from "./bus"

describe("bus", () => {
  it("calls the listeners of each event name in order", () => {
    const jobs: unknown[] = []
    const off = onEvent("job.run", (d) => jobs.push(d))
    emitEvents([
      { name: "job.run", data: { job_id: "j1", status: "ok" } },
      { name: "status", data: {} },
      { name: "job.run", data: { job_id: "j2", status: "error" } },
    ])
    expect(jobs).toEqual([
      { job_id: "j1", status: "ok" },
      { job_id: "j2", status: "error" },
    ])
    off()
    emitEvents([{ name: "job.run", data: {} }])
    expect(jobs).toHaveLength(2)
  })

  it("keeps calling the other listeners when one throws", () => {
    const err = vi.spyOn(console, "error").mockImplementation(() => {})
    const ok = vi.fn()
    const off1 = onEvent("held", () => {
      throw new Error("boom")
    })
    const off2 = onEvent("held", ok)
    emitEvents([{ name: "held", data: { id: "h1" } }])
    expect(ok).toHaveBeenCalledWith({ id: "h1" })
    off1()
    off2()
    err.mockRestore()
  })

  it("lets a listener of reconnected run with no data", () => {
    const fn = vi.fn()
    const off = onReconnected(fn)
    emitEvents([{ name: "reconnected", data: null }])
    expect(fn).toHaveBeenCalledTimes(1)
    expect(fn).toHaveBeenCalledWith()
    off()
  })
})
