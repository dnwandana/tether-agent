import { sessionHeader } from "./client"

export type StreamState = "open" | "reconnecting" | "signed-out"
type Ev = { name: string; data: unknown }

const SILENCE_MS = 45_000
const FIRST_RETRY_MS = 1_000
const MAX_RETRY_MS = 10_000
const STALE_MS = 20_000
const HIDDEN_FLUSH_MS = 100

export function openStream(
  onEvents: (batch: Ev[]) => void,
  onState: (s: StreamState) => void,
): () => void {
  let closed = false
  let state: StreamState | null = null
  let delay = FIRST_RETRY_MS
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let silenceTimer: ReturnType<typeof setTimeout> | null = null
  let flushTimer: ReturnType<typeof setTimeout> | null = null
  let flushQueued = false
  let pending: Ev[] = []
  let ctrl: AbortController | null = null
  let reader: ReadableStreamDefaultReader<Uint8Array> | null = null
  let lastByte = 0
  let hiddenAt = 0
  let gen = 0

  function setState(s: StreamState) {
    if (closed && s !== "signed-out") return
    if (s === state) return
    state = s
    onState(s)
  }

  function clearTimers() {
    if (retryTimer) clearTimeout(retryTimer)
    if (silenceTimer) clearTimeout(silenceTimer)
    if (flushTimer) clearTimeout(flushTimer)
    retryTimer = silenceTimer = flushTimer = null
  }

  function stopRead() {
    if (silenceTimer) clearTimeout(silenceTimer)
    silenceTimer = null
    const r = reader
    reader = null
    if (r) r.cancel().catch(() => {})
    if (ctrl) ctrl.abort()
    ctrl = null
  }

  function armSilence() {
    if (silenceTimer) clearTimeout(silenceTimer)
    silenceTimer = setTimeout(() => {
      stopRead()
      // The read loop of the old stream may not report the end, so retry here.
      retry()
    }, SILENCE_MS)
  }

  function retry() {
    if (closed || retryTimer) return
    setState("reconnecting")
    retryTimer = setTimeout(() => {
      retryTimer = null
      connect()
    }, delay)
    delay = Math.min(delay * 2, MAX_RETRY_MS)
  }

  function flush() {
    flushQueued = false
    flushTimer = null
    if (closed || pending.length === 0) return
    const batch = pending
    pending = []
    onEvents(batch)
  }

  function queueFlush() {
    if (flushQueued) return
    flushQueued = true
    if (document.hidden) flushTimer = setTimeout(flush, HIDDEN_FLUSH_MS)
    else requestAnimationFrame(flush)
  }

  function parseFrame(frame: string) {
    let name = "message"
    const data: string[] = []
    for (let line of frame.split("\n")) {
      if (line.endsWith("\r")) line = line.slice(0, -1)
      if (line.startsWith(":")) continue
      if (line.startsWith("event:")) {
        name = line.slice(6).replace(/^ /, "")
      } else if (line.startsWith("data:")) {
        data.push(line.slice(5).replace(/^ /, ""))
      }
    }
    if (data.length === 0) return
    try {
      pending.push({ name, data: JSON.parse(data.join("\n")) })
    } catch {
      return
    }
    queueFlush()
  }

  async function connect() {
    if (closed) return
    const my = ++gen
    const c = new AbortController()
    ctrl = c
    const headers: Record<string, string> = { Accept: "text/event-stream" }
    const h = sessionHeader()
    if (h) headers["X-Tether-Session"] = h
    let resp: Response
    try {
      resp = await fetch("/api/events", {
        headers,
        signal: c.signal,
        credentials: "same-origin",
        cache: "no-store",
      })
    } catch {
      if (my === gen) retry()
      return
    }
    if (closed || my !== gen) return
    if (resp.status === 401) {
      setState("signed-out")
      close(true)
      return
    }
    if (resp.status !== 200 || !resp.body) {
      retry()
      return
    }
    delay = FIRST_RETRY_MS
    setState("open")
    lastByte = Date.now()
    armSilence()
    const rd = resp.body.getReader()
    reader = rd
    const dec = new TextDecoder()
    let buf = ""
    try {
      for (;;) {
        const { done, value } = await rd.read()
        if (done || closed || my !== gen) break
        lastByte = Date.now()
        armSilence()
        buf += dec.decode(value, { stream: true })
        let i: number
        while ((i = buf.indexOf("\n\n")) >= 0) {
          parseFrame(buf.slice(0, i))
          buf = buf.slice(i + 2)
        }
      }
    } catch {
      // A read error ends the stream. Fall through to retry.
    }
    if (!closed && my === gen) {
      stopRead()
      retry()
    }
  }

  function check() {
    if (closed) return
    if (retryTimer || !ctrl || Date.now() - lastByte > STALE_MS) {
      stopRead()
      if (retryTimer) clearTimeout(retryTimer)
      retryTimer = null
      delay = FIRST_RETRY_MS
      connect()
    }
  }

  function onOnline() {
    check()
  }
  function onVisibility() {
    if (document.hidden) {
      hiddenAt = Date.now()
      // A hidden tab runs no animation frame. Flush the queued events with the timer.
      if (flushQueued && !flushTimer) flushTimer = setTimeout(flush, HIDDEN_FLUSH_MS)
      return
    }
    if (hiddenAt && Date.now() - hiddenAt > STALE_MS) check()
    hiddenAt = 0
  }

  function close(keepState = false) {
    closed = true
    gen++
    clearTimers()
    stopRead()
    pending = []
    window.removeEventListener("online", onOnline)
    document.removeEventListener("visibilitychange", onVisibility)
    void keepState
  }

  window.addEventListener("online", onOnline)
  document.addEventListener("visibilitychange", onVisibility)
  connect()
  return () => close()
}
