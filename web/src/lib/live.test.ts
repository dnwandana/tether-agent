import { flushPromises, mount } from "@vue/test-utils"
import { defineComponent, effectScope, h, onMounted, ref } from "vue"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const bus = vi.hoisted(() => ({
  events: new Map<string, ((d: unknown) => void)[]>(),
  reconnect: [] as (() => void)[],
}))
vi.mock("./bus", () => ({
  onEvent: (name: string, fn: (d: unknown) => void) => {
    const list = bus.events.get(name) ?? []
    list.push(fn)
    bus.events.set(name, list)
    return () =>
      bus.events.set(
        name,
        (bus.events.get(name) ?? []).filter((f) => f !== fn),
      )
  },
  onReconnected: (fn: () => void) => {
    bus.reconnect.push(fn)
    return () => (bus.reconnect = bus.reconnect.filter((f) => f !== fn))
  },
}))
vi.mock("../router", () => ({ restorePendingScroll: vi.fn() }))
vi.mock("../api/client", () => {
  class ApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  }
  return { api: vi.fn(), ApiError }
})

import { followTask, useLiveData } from "./live"
import { restorePendingScroll } from "../router"
import { api, ApiError } from "../api/client"

const fire = (name: string, data: unknown = {}) => bus.events.get(name)?.forEach((f) => f(data))
const reconnect = () => bus.reconnect.forEach((f) => f())
const focus = () => document.dispatchEvent(new Event("visibilitychange"))

function host(load: () => Promise<void>, opts?: Parameters<typeof useLiveData>[1]) {
  let live!: ReturnType<typeof useLiveData>
  const w = mount(
    defineComponent({
      setup() {
        live = useLiveData(load, opts)
        return () => h("div")
      },
    }),
  )
  return { w, live: () => live }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date(2026, 10, 23, 9, 0, 0))
  bus.events.clear()
  bus.reconnect = []
  vi.mocked(restorePendingScroll).mockClear()
  vi.mocked(api).mockReset()
  Object.defineProperty(document, "hidden", { configurable: true, get: () => false })
})
afterEach(() => vi.useRealTimers())

describe("useLiveData", () => {
  it("loads on mount, sets the time, and restores the scroll after the first load", async () => {
    const load = vi.fn(async () => {})
    const { live } = host(load)
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(1)
    expect(live().updatedAt.value).toEqual(new Date(2026, 10, 23, 9, 0, 0))
    expect(restorePendingScroll).toHaveBeenCalledTimes(1)
    await live().refresh()
    expect(restorePendingScroll).toHaveBeenCalledTimes(1)
  })

  it("reloads on a listed event that passes the filter", async () => {
    const load = vi.fn(async () => {})
    host(load, {
      events: ["job.run"],
      filter: (_n, d) => (d as { job_id: string }).job_id === "j1",
    })
    await flushPromises()
    fire("job.run", { job_id: "j2", status: "ok" })
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(1)
    fire("job.run", { job_id: "j1", status: "ok" })
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(2)
  })

  it("runs one follow-up load for many triggers during a load", async () => {
    let done!: () => void
    const load = vi.fn(() => new Promise<void>((r) => (done = r)))
    host(load, { events: ["held"] })
    fire("held")
    fire("held")
    fire("held")
    expect(load).toHaveBeenCalledTimes(1)
    done()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(2)
    done()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(2)
  })

  it("waits 10 s after the last load before a focus reload", async () => {
    const load = vi.fn(async () => {})
    host(load)
    await flushPromises()
    vi.advanceTimersByTime(9_999)
    focus()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(1)
    focus()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(2)
  })

  it("joins a reconnect and a focus in the same 10 s", async () => {
    const load = vi.fn(async () => {})
    host(load)
    await flushPromises()
    vi.advanceTimersByTime(30_000)
    reconnect()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(2)
    vi.advanceTimersByTime(2_000)
    focus()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(2)
  })

  it("adds no focus listener before the component mounts", async () => {
    const load = vi.fn(async () => {})
    const scope = effectScope()
    scope.run(() => useLiveData(load))
    vi.advanceTimersByTime(60_000)
    focus()
    await flushPromises()
    expect(load).not.toHaveBeenCalled()
    scope.stop()
  })

  it("does not reload on focus when focus is false", async () => {
    const load = vi.fn(async () => {})
    host(load, { focus: false })
    await flushPromises()
    vi.advanceTimersByTime(60_000)
    focus()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(1)
  })

  it("keeps the old data, sets stale, and keeps a mounted child component", async () => {
    const mounts = vi.fn()
    const Child = defineComponent({
      setup() {
        onMounted(mounts)
        return () => h("form", { class: "child" })
      },
    })
    let done!: () => void
    const rows = ref(["a"])
    let live!: ReturnType<typeof useLiveData>
    const w = mount(
      defineComponent({
        setup() {
          live = useLiveData(
            () =>
              new Promise<void>((r) => {
                done = () => {
                  rows.value = [...rows.value, "b"]
                  r()
                }
              }),
            { events: ["held"] },
          )
          return () =>
            h("div", { class: { dim: live.stale.value } }, [
              h(Child),
              ...rows.value.map((x) => h("p", x)),
            ])
        },
      }),
    )
    done()
    await flushPromises()
    fire("held")
    await flushPromises()
    expect(live.stale.value).toBe(true)
    expect(w.findAll("p").map((p) => p.text())).toEqual(["a", "b"])
    done()
    await flushPromises()
    expect(live.stale.value).toBe(false)
    expect(w.findAll("p")).toHaveLength(3)
    expect(mounts).toHaveBeenCalledTimes(1)
  })

  it("removes its listeners on unmount", async () => {
    const load = vi.fn(async () => {})
    const { w } = host(load, { events: ["held"] })
    await flushPromises()
    w.unmount()
    fire("held")
    reconnect()
    vi.advanceTimersByTime(60_000)
    focus()
    await flushPromises()
    expect(load).toHaveBeenCalledTimes(1)
  })
})

const view = (state: string) => ({ id: "t1", kind: "skills", state, text: "" })

describe("followTask", () => {
  it("reads the task once, then follows the task event and each reconnect", async () => {
    vi.mocked(api).mockResolvedValue(view("running") as never)
    const seen: string[] = []
    const stop = followTask("t1", (t) => seen.push(t.state))
    await flushPromises()
    expect(api).toHaveBeenCalledWith("GET", "/api/tasks/t1")
    fire("task", { ...view("done"), id: "t9" })
    fire("task", view("done"))
    reconnect()
    await flushPromises()
    expect(api).toHaveBeenCalledTimes(2)
    expect(seen).toEqual(["running", "done", "running"])
    stop()
    fire("task", view("error"))
    expect(seen).toHaveLength(3)
  })

  it("drops a read that a newer task event overtook", async () => {
    let resolve!: (v: unknown) => void
    vi.mocked(api).mockReturnValue(new Promise((r) => (resolve = r)) as never)
    const seen: string[] = []
    followTask("t1", (t) => seen.push(t.state))
    fire("task", view("done"))
    resolve(view("running"))
    await flushPromises()
    expect(seen).toEqual(["done"])
  })

  it("drops an older read that ends after a newer read", async () => {
    const done: ((v: unknown) => void)[] = []
    vi.mocked(api).mockImplementation(() => new Promise((r) => done.push(r)) as never)
    const seen: string[] = []
    followTask("t1", (t) => seen.push(t.state))
    reconnect()
    done[1](view("done"))
    await flushPromises()
    done[0](view("running"))
    await flushPromises()
    expect(seen).toEqual(["done"])
  })

  it("reports a lost task on a 404", async () => {
    vi.mocked(api).mockRejectedValue(new ApiError(404, "not found"))
    const onUpdate = vi.fn()
    followTask("t1", onUpdate)
    await flushPromises()
    expect(onUpdate).toHaveBeenCalledWith(
      expect.objectContaining({
        id: "t1",
        state: "lost",
        text: "The gateway restarted. The result of the task is lost.",
      }),
    )
  })
})
