import { onBeforeUnmount, onMounted, ref, type Ref } from "vue"
import { api, ApiError } from "../api/client"
import type { TaskView } from "../api/types.gen"
import { restorePendingScroll } from "../router"
import { onEvent, onReconnected } from "./bus"

// A reconnect does not load when a reconnect or a focus load started less than this long ago.
const JOIN_MS = 10_000
// A focus does not load when any load started less than this long ago.
const FOCUS_MS = 10_000

export function useLiveData(
  load: () => Promise<void>,
  opts?: { events?: string[]; filter?: (name: string, data: unknown) => boolean; focus?: boolean },
): { stale: Ref<boolean>; updatedAt: Ref<Date | null>; refresh: () => Promise<void> } {
  const stale = ref(false)
  const updatedAt = ref<Date | null>(null)
  let running: Promise<void> | null = null
  let again = false
  let firstDone = false
  const life = { gone: false }
  let lastStart = -Infinity
  let lastJoinStart = -Infinity

  // One load runs at a time. Triggers during a load set one follow-up flag.
  async function drain(): Promise<void> {
    do {
      again = false
      lastStart = Date.now()
      stale.value = true
      try {
        await load()
        updatedAt.value = new Date()
      } catch {
        // The page shows its own error. The queue goes on.
      }
      stale.value = false
      if (!firstDone) {
        firstDone = true
        restorePendingScroll()
      }
    } while (again && !life.gone)
    running = null
  }

  function refresh(): Promise<void> {
    if (life.gone) return Promise.resolve()
    if (running) {
      again = true
      return running
    }
    running = drain()
    return running
  }

  const offs: (() => void)[] = []
  for (const name of opts?.events ?? []) {
    offs.push(
      onEvent(name, (data) => {
        if (!opts?.filter || opts.filter(name, data)) void refresh()
      }),
    )
  }
  // A reconnect joins a focus load of the same window. stream.ts reconnects after a long hidden tab.
  offs.push(
    onReconnected(() => {
      if (Date.now() - lastJoinStart < JOIN_MS) return
      lastJoinStart = Date.now()
      void refresh()
    }),
  )
  const onVisible = () => {
    if (document.hidden || opts?.focus === false) return
    if (Date.now() - lastStart < FOCUS_MS) return
    lastJoinStart = Date.now()
    void refresh()
  }
  // The listener starts on mount, so that a component that never mounts leaves none behind.
  onMounted(() => {
    document.addEventListener("visibilitychange", onVisible)
    offs.push(() => document.removeEventListener("visibilitychange", onVisible))
    // The first load starts when the page mounts.
    void refresh()
  })
  // No listener stays and no load starts after unmount.
  onBeforeUnmount(() => {
    life.gone = true
    again = false
    offs.forEach((f) => f())
  })

  return { stale, updatedAt, refresh }
}

const LOST_TEXT = "The gateway restarted. The result of the task is lost."

export function followTask(id: string, onUpdate: (t: TaskView) => void): () => void {
  let stopped = false
  let last: TaskView | null = null
  // Counts the task events and the reads. A read that started before a newer event or a newer
  // read is older, so it is dropped.
  let seq = 0
  const read = async () => {
    const mine = ++seq
    try {
      const t = await api<TaskView>("GET", "/api/tasks/" + encodeURIComponent(id))
      if (stopped || mine !== seq) return
      last = t
      onUpdate(t)
    } catch (e) {
      if (stopped || mine !== seq) return
      // A 404 means the task is gone. Other errors wait for the next event.
      if (e instanceof ApiError && e.status === 404) {
        onUpdate({
          ...(last ?? { id, kind: "", state: "", text: "" }),
          id,
          state: "lost",
          text: LOST_TEXT,
        } as TaskView)
      }
    }
  }
  // A fast task can end before the page knows its id, so read once at the start.
  void read()
  const offEvent = onEvent("task", (data) => {
    const t = data as TaskView
    if (stopped || t?.id !== id) return
    seq++
    last = t
    onUpdate(t)
  })
  const offReconnect = onReconnected(() => void read())
  return () => {
    stopped = true
    offEvent()
    offReconnect()
  }
}
