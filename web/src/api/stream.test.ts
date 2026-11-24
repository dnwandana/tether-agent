import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { openStream, retryNow, setFollowLogs, type StreamState } from "./stream"
import { onReconnected } from "../lib/bus"

function feed() {
  let ctrl!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      ctrl = c
    },
  })
  const enc = new TextEncoder()
  return { body, push: (s: string) => ctrl.enqueue(enc.encode(s)) }
}

let feeds: ReturnType<typeof feed>[]
let fetchMock: ReturnType<typeof vi.fn>

function openFetch() {
  fetchMock = vi.fn(async () => {
    const f = feed()
    feeds.push(f)
    return { ok: true, status: 200, body: f.body }
  })
  vi.stubGlobal("fetch", fetchMock)
}

beforeEach(() => {
  vi.useFakeTimers()
  localStorage.clear()
  feeds = []
  Object.defineProperty(document, "hidden", { configurable: true, get: () => false })
  vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => setTimeout(() => cb(0), 16))
})
afterEach(() => {
  setFollowLogs(false)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe("openStream", () => {
  it("emits reconnected after each new connection that follows the first", async () => {
    openFetch()
    const rec = vi.fn()
    const off = onReconnected(rec)
    const stop = openStream(
      () => {},
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    expect(rec).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(45_000) // silence drops the stream
    await vi.advanceTimersByTimeAsync(1_000) // the retry connects again
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(rec).toHaveBeenCalledTimes(1)
    off()
    stop()
  })

  it("connects at once on retryNow while it waits to reconnect", async () => {
    fetchMock = vi.fn(async () => {
      throw new TypeError("network")
    })
    vi.stubGlobal("fetch", fetchMock)
    const stop = openStream(
      () => {},
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    retryNow()
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    stop()
    retryNow() // no stream: nothing happens
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("reconnects with follow=logs and emits no reconnected for the switch", async () => {
    openFetch()
    const rec = vi.fn()
    const off = onReconnected(rec)
    const stop = openStream(
      () => {},
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    setFollowLogs(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock.mock.calls[1][0]).toBe("/api/events?follow=logs")
    setFollowLogs(true) // no change: no new connection
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    setFollowLogs(false)
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock.mock.calls[2][0]).toBe("/api/events")
    expect(rec).not.toHaveBeenCalled()
    off()
    stop()
  })

  it("emits reconnected when the follow changes while the stream is offline", async () => {
    openFetch()
    const rec = vi.fn()
    const off = onReconnected(rec)
    const stop = openStream(
      () => {},
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(45_000) // silence drops the stream, a retry is pending
    setFollowLogs(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(rec).toHaveBeenCalledTimes(1)
    off()
    stop()
  })

  it("sends the session header to /api/events", async () => {
    localStorage.setItem("tether.session", "h123")
    openFetch()
    const stop = openStream(
      () => {},
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe("/api/events")
    expect(init.headers["X-Tether-Session"]).toBe("h123")
    expect(init.credentials).toBe("same-origin")
    stop()
  })

  it("batches the events of one animation frame", async () => {
    openFetch()
    const got: { name: string; data: unknown }[][] = []
    const stop = openStream(
      (b) => got.push(b),
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    feeds[0].push(
      'event: hello\ndata: {"session_id":"s1"}\n\nevent: delta\ndata: {"text":"Hi"}\n\n',
    )
    feeds[0].push(": heartbeat\n\nevent: del")
    feeds[0].push('ta\ndata: {"text":" there"}\n\n')
    await vi.advanceTimersByTimeAsync(16)
    expect(got).toEqual([
      [
        { name: "hello", data: { session_id: "s1" } },
        { name: "delta", data: { text: "Hi" } },
        { name: "delta", data: { text: " there" } },
      ],
    ])
    stop()
  })

  it("joins the data lines of one frame", async () => {
    openFetch()
    const got: { name: string; data: unknown }[][] = []
    const stop = openStream(
      (b) => got.push(b),
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    feeds[0].push('event: card\ndata: {"id":"c1",\ndata: "kind":"approval"}\n\n')
    await vi.advanceTimersByTimeAsync(16)
    expect(got).toEqual([[{ name: "card", data: { id: "c1", kind: "approval" } }]])
    stop()
  })

  it("stops with signed-out on a 401", async () => {
    fetchMock = vi.fn(async () => ({ ok: false, status: 401, body: null }))
    vi.stubGlobal("fetch", fetchMock)
    const states: StreamState[] = []
    openStream(
      () => {},
      (s) => states.push(s),
    )
    await vi.advanceTimersByTimeAsync(20_000)
    expect(states).toEqual(["signed-out"])
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("connects again after 1 s, then backs off up to 10 s", async () => {
    fetchMock = vi.fn(async () => {
      throw new TypeError("network")
    })
    vi.stubGlobal("fetch", fetchMock)
    const states: StreamState[] = []
    const stop = openStream(
      () => {},
      (s) => states.push(s),
    )
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(999)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    for (const [ms, n] of [
      [1, 2],
      [2000, 3],
      [4000, 4],
      [8000, 5],
      [10_000, 6],
      [10_000, 7],
    ]) {
      await vi.advanceTimersByTimeAsync(ms)
      expect(fetchMock).toHaveBeenCalledTimes(n)
    }
    expect(states).toEqual(["reconnecting"])
    stop()
  })

  it("drops a stream that is silent for 45 s", async () => {
    openFetch()
    const states: StreamState[] = []
    const stop = openStream(
      () => {},
      (s) => states.push(s),
    )
    await vi.advanceTimersByTimeAsync(0)
    feeds[0].push(": heartbeat\n\n")
    await vi.advanceTimersByTimeAsync(44_999)
    expect(states).toEqual(["open"])
    await vi.advanceTimersByTimeAsync(1)
    expect(states).toEqual(["open", "reconnecting"])
    await vi.advanceTimersByTimeAsync(1000)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(states).toEqual(["open", "reconnecting", "open"])
    stop()
  })

  it("checks the stream at once on online", async () => {
    openFetch()
    const stop = openStream(
      () => {},
      () => {},
    )
    await vi.advanceTimersByTimeAsync(21_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    window.dispatchEvent(new Event("online"))
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    stop()
  })

  it("does nothing after close", async () => {
    openFetch()
    const states: StreamState[] = []
    const stop = openStream(
      () => {},
      (s) => states.push(s),
    )
    await vi.advanceTimersByTimeAsync(0)
    stop()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(states).toEqual(["open"])
  })
  it("flushes with the timer when the tab hides after a frame was queued", async () => {
    openFetch()
    vi.stubGlobal("requestAnimationFrame", () => 1)
    const got: { name: string; data: unknown }[][] = []
    const stop = openStream(
      (b) => got.push(b),
      () => {},
    )
    await vi.advanceTimersByTimeAsync(0)
    feeds[0].push('event: delta\ndata: {"text":"Hi"}\n\n')
    await vi.advanceTimersByTimeAsync(50)
    expect(got).toEqual([])
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true })
    document.dispatchEvent(new Event("visibilitychange"))
    await vi.advanceTimersByTimeAsync(100)
    expect(got).toEqual([[{ name: "delta", data: { text: "Hi" } }]])
    stop()
  })
})
