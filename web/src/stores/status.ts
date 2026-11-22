import { reactive } from "vue"
import type { StreamState } from "../api/stream"
import type { Status } from "../api/types.gen"

export const status = reactive<Status & { stream: StreamState }>({
  model: "",
  entry: "",
  fallback: false,
  fallback_cause: "",
  mode: "",
  waiting: 0,
  running: 0,
  spend24h: 0,
  unpriced: false,
  tls: false,
  stream: "reconnecting",
})

// Merges the status of a hello event or a status event. It never changes stream.
export function applyStatusEvents(batch: { name: string; data: unknown }[]): void {
  for (const ev of batch) {
    if (ev.name === "hello") {
      const s = (ev.data as { status?: Partial<Status> } | null)?.status
      if (s) Object.assign(status, s)
    } else if (ev.name === "status") {
      if (ev.data && typeof ev.data === "object") Object.assign(status, ev.data as Partial<Status>)
    }
  }
}
