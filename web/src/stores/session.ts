import { markRaw, reactive } from "vue"
import { api } from "../api/client"
import type { MessagePage, MessageView, SessionRow, SessionView, Snapshot } from "../api/types.gen"

type Batch = { name: string; data: unknown }[]

const PAGE = 50

export const session = reactive({
  id: "",
  title: "",
  model: "",
  entry: "",
  effort: "",
  goalLine: "",
  context: null as { tokens: number; window: number; threshold: number } | null,
  messages: [] as MessageView[],
  // The smallest and the largest saved message ids of the loaded pages. A tool result merges
  // into the view of its call, so these ids can differ from the ids of the views. They are the
  // cursors of the older and the newer pages.
  firstId: 0,
  lastId: 0,
  // True when the session has saved messages before firstId.
  hasOlder: false,
})

// The newest message id that the server reported.
let latestKnown = 0

// The server saves the user message before the first live event of a turn. The page loads
// the new messages at that event, so the user message shows before the turn ends.
let turnSynced = false

// Sets a new raw list. A row with a positive id sorts by id. A later row with the same positive
// id replaces the earlier row, because a newer page can send a tool call again with its results.
// A row with a negative id is a compaction summary: it keeps the server order, after the row
// that comes before it in the list. A repeated summary is dropped.
function setMessages(list: MessageView[]): void {
  const seen = new Set<number>()
  const real = new Map<number, MessageView>()
  const after = new Map<number, MessageView[]>()
  let anchor = 0
  for (const m of list) {
    if (m.id > 0) {
      real.set(m.id, m)
      anchor = m.id
      continue
    }
    if (seen.has(m.id)) continue
    seen.add(m.id)
    const g = after.get(anchor) ?? []
    g.push(m)
    after.set(anchor, g)
  }
  const sorted = [...real.values()].sort((a, b) => a.id - b.id)
  const out: MessageView[] = [...(after.get(0) ?? [])]
  for (const m of sorted) {
    out.push(m, ...(after.get(m.id) ?? []))
  }
  session.messages = markRaw(out)
}

// Adds one page. An older page goes before the loaded rows, and a newer page goes after them.
// The cursors come from the page, not from the views.
function addPage(page: MessagePage, older: boolean): void {
  const rows = page.rows ?? []
  setMessages(older ? [...rows, ...session.messages] : [...session.messages, ...rows])
  if (older || session.firstId === 0) {
    if (page.first_id > 0) {
      session.firstId =
        session.firstId === 0 ? page.first_id : Math.min(session.firstId, page.first_id)
    }
    session.hasOlder = page.has_older
  }
  session.lastId = Math.max(session.lastId, page.last_id)
}

function resetList(): void {
  session.messages = markRaw([])
  session.firstId = 0
  session.lastId = 0
  session.hasOlder = false
}

function warn(e: unknown): void {
  console.warn("session load failed", e)
}

function setFields(v: SessionView): void {
  session.title = v.title
  session.model = v.model
  session.entry = v.entry
  session.effort = v.effort
  session.goalLine = v.goal_line
  session.context = v.context ?? null
}

async function loadTitle(id: string): Promise<void> {
  try {
    const row = await api<SessionRow>("GET", "/api/sessions/" + encodeURIComponent(id))
    if (session.id === id) session.title = row.title
  } catch {
    // Keep the title as it is.
  }
}

let newerRunning: Promise<void> | null = null
let newerQueued = false

function base(): string {
  return "/api/sessions/" + encodeURIComponent(session.id) + "/messages?"
}

async function loadNewer(): Promise<void> {
  const id = session.id
  if (session.messages.length === 0) {
    const page = await api<MessagePage>("GET", base() + "limit=" + PAGE)
    if (session.id === id) addPage(page, false)
    return
  }
  // A page can have fewer views than PAGE when tool results merge into their calls. Only
  // has_newer tells if more messages exist.
  for (;;) {
    const from = session.lastId
    const page = await api<MessagePage>("GET", base() + "after=" + from + "&limit=" + PAGE)
    if (session.id !== id) return
    addPage(page, false)
    if (!page.has_newer || session.lastId <= from) return
  }
}

// Loads the newest page, the newer messages, or one older page. An older page starts before
// the smallest saved id of the loaded pages when that id is smaller than before.
export async function loadMessages(opts?: { before?: number }): Promise<void> {
  if (session.id === "") return
  if (opts?.before !== undefined) {
    const id = session.id
    const before = session.firstId > 0 ? Math.min(opts.before, session.firstId) : opts.before
    const page = await api<MessagePage>("GET", base() + "before=" + before + "&limit=" + PAGE)
    if (session.id === id) addPage(page, true)
    return
  }
  if (newerRunning) {
    newerQueued = true
    return newerRunning
  }
  const run = async (): Promise<void> => {
    try {
      do {
        newerQueued = false
        if (session.id === "") return
        await loadNewer()
      } while (newerQueued)
    } finally {
      newerRunning = null
    }
  }
  newerRunning = run()
  return newerRunning
}

function load(): void {
  loadMessages().catch(warn)
}

export function applySessionEvents(batch: Batch): void {
  for (const e of batch) {
    switch (e.name) {
      case "hello": {
        const d = e.data as Snapshot
        turnSynced = d.turn != null
        if (d.session_id !== session.id) {
          session.id = d.session_id
          resetList()
          if (!d.session?.id) void loadTitle(d.session_id)
        }
        if (d.session?.id) setFields(d.session)
        latestKnown = d.last_message_id
        if (session.messages.length === 0 || latestKnown > session.lastId) load()
        break
      }
      case "session": {
        const d = e.data as SessionView
        const changed = d.id !== session.id
        session.id = d.id
        setFields(d)
        if (changed) {
          resetList()
          latestKnown = 0
          load()
        }
        break
      }
      case "goal":
        session.goalLine = (e.data as { line: string }).line
        break
      case "delta":
      case "tool.start":
      case "progress":
      case "plan":
        if (!turnSynced) {
          turnSynced = true
          load()
        }
        break
      case "turn.done": {
        turnSynced = false
        const d = e.data as { message_id: number }
        latestKnown = Math.max(latestKnown, d.message_id)
        // The message id can be 0. The page then loads what follows its last id.
        if (latestKnown > session.lastId || d.message_id === 0) load()
        break
      }
    }
  }
}
