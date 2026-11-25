import { shallowReactive } from "vue"
import type { AckView, PlanItem, QueueItem, ToolView, TurnDone, TurnView } from "../api/types.gen"

type Batch = { name: string; data: unknown }[]

const MAX_ACKS = 5

export const turn = shallowReactive({
  view: null as TurnView | null,
  acks: [] as AckView[],
  queue: [] as QueueItem[],
  done: null as TurnDone | null,
})

function emptyView(): TurnView {
  return { running: true, text: "", tools: [], plan: [], queue: [] }
}

// Applies the events of one batch. It assigns turn.view once at the end.
export function applyTurnEvents(batch: Batch): void {
  let v: TurnView | null = turn.view
    ? { ...turn.view, tools: [...turn.view.tools], plan: [...turn.view.plan] }
    : null
  let changed = false
  const live = (): TurnView => {
    if (!v) v = emptyView()
    changed = true
    turn.done = null
    return v
  }
  for (const e of batch) {
    switch (e.name) {
      case "hello": {
        const d = e.data as { turn: TurnView | null }
        v = d.turn
          ? { ...d.turn, tools: [...(d.turn.tools ?? [])], plan: [...(d.turn.plan ?? [])] }
          : null
        changed = true
        turn.queue = d.turn?.queue ?? []
        turn.acks = []
        if (d.turn) turn.done = null
        break
      }
      case "delta":
        live().text += (e.data as { text: string }).text
        break
      case "boundary": {
        const x = live()
        if (x.text !== "" && !x.text.endsWith("\n\n")) x.text += "\n\n"
        break
      }
      case "tool.start": {
        const d = e.data as Partial<ToolView>
        live().tools.push({
          call: d.call ?? "",
          name: d.name ?? "",
          emoji: d.emoji ?? "",
          preview: d.preview ?? "",
          args: d.args ?? "",
          state: "running",
          duration_ms: 0,
          text: "",
          cut: false,
        })
        break
      }
      case "tool.end": {
        // The server sends the whole ToolView, with state done or error.
        const d = e.data as ToolView
        const x = live()
        const i = x.tools.findIndex((t) => t.call === d.call)
        if (i >= 0) {
          x.tools[i] = {
            ...x.tools[i],
            state: d.state === "error" ? "error" : "done",
            duration_ms: d.duration_ms,
            text: d.text,
            cut: d.cut,
          }
        }
        break
      }
      case "progress": {
        const d = e.data as { child: string; tool: string; preview: string }
        const x = live()
        for (let i = x.tools.length - 1; i >= 0; i--) {
          if (x.tools[i].state !== "running") continue
          const q = d.preview ? ` "${d.preview}"` : ""
          x.tools[i] = { ...x.tools[i], preview: `[${d.child}] ${d.tool}${q}` }
          break
        }
        break
      }
      case "plan":
        live().plan = (e.data as { after: PlanItem[] }).after ?? []
        break
      case "queue":
        turn.queue = (e.data as { items: QueueItem[] }).items ?? []
        break
      case "ack": {
        const a = e.data as AckView
        const i = turn.acks.findIndex((x) => x.id === a.id)
        const list = [...turn.acks]
        if (i >= 0) list[i] = a
        else list.push(a)
        turn.acks = list.slice(-MAX_ACKS)
        break
      }
      case "turn.done":
        v = null
        changed = true
        turn.queue = []
        turn.acks = turn.acks.filter((a) => a.request_id !== "")
        turn.done = e.data as TurnDone
        break
    }
  }
  if (changed) turn.view = v
}
