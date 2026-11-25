import { ref, type Ref } from "vue"
import { runCommand } from "../api/client"
import type { Button, ReplyView } from "../api/types.gen"
import { confirm, toast, type ToastKind } from "../stores/ui"
import { onEvent } from "./bus"
import { friendlyError } from "./errors"
import { splitGlyph } from "./glyphs"

export type ResultNoteData = {
  title: string
  at: Date
  text: string
  kind: "ok" | "info" | "warn" | "error"
}

const FOLLOW_MS = 60_000
const TEXT_PENDING = "Still running…"
const TEXT_LOST = "The reply did not come. See the chat or the log."

// The keys of glyphs.json that start a reply of each kind.
const ERROR_KEYS = ["denied", "refused", "human-only"]
const WARN_KEYS = ["warning", "waiting", "info"]
const OK_KEYS = ["ok", "done", "saved", "archived", "renamed", "delegate", "new", "undo", "fork"]

// Reads the kind of a reply from the key of its first mark. An unknown start is never ok.
export function replyKind(text: string): ToastKind {
  const t = text.trimStart()
  const bold = /\*\*(.+?)\*\*/s.exec(t)
  if (bold && bold[1].includes("failed")) return "error"
  const key = splitGlyph(t).glyph?.key ?? ""
  if (ERROR_KEYS.includes(key)) return "error"
  if (t.startsWith("Usage:") || /^\/[a-z][a-z_-]*( [a-z][a-z_-]*)*: /.test(t)) return "error"
  if (WARN_KEYS.includes(key)) return "warn"
  if (OK_KEYS.includes(key)) return "ok"
  return "info"
}

type RunOpts = { result?: Ref<ResultNoteData | null>; button?: number; title?: string }

function show(text: string, kind: ToastKind, opts: RunOpts): void {
  const res = opts.result
  if (res) {
    res.value = {
      title: opts.title ?? text,
      at: new Date(),
      text,
      kind,
    }
    return
  }
  toast(text, kind)
}

// Collects the ack events from before the request starts. The ack of a slow command can arrive
// before the 202 reply is handled, so the listener must exist before the request.
function watchAcks(): { off: () => void; wait: (id: string) => Promise<ReplyView | null> } {
  const got = new Map<string, ReplyView>()
  let waiting: { id: string; resolve: (r: ReplyView) => void } | null = null
  const off = onEvent("ack", (data) => {
    const d = data as Partial<ReplyView> | null
    if (!d || typeof d.request_id !== "string" || typeof d.text !== "string") return
    const rv: ReplyView = {
      request_id: d.request_id,
      text: d.text,
      buttons: d.buttons ?? [],
      edit: false,
    }
    if (waiting && waiting.id === rv.request_id) waiting.resolve(rv)
    else got.set(rv.request_id, rv)
  })
  // Waits for the ack event with the request id. Returns null after 60 s.
  const wait = (id: string): Promise<ReplyView | null> =>
    new Promise((done) => {
      const early = got.get(id)
      if (early) {
        off()
        done(early)
        return
      }
      const timer = setTimeout(() => {
        off()
        done(null)
      }, FOLLOW_MS)
      waiting = {
        id,
        resolve: (r) => {
          clearTimeout(timer)
          off()
          done(r)
        },
      }
    })
  return { off, wait }
}

// Gives one handler for a typed command, and the reply that needs a popover.
// A reply with a confirm button opens the confirm dialog first. A reply with 3 or more buttons
// sets `reply`, and the caller shows it. Other replies show as a toast or in a result note.
export function useCommand(): {
  run: (text: string, opts?: RunOpts) => Promise<ToastKind>
  reply: Ref<ReplyView | null>
  closeReply: () => void
} {
  const reply = ref<ReplyView | null>(null)

  function closeReply(): void {
    reply.value = null
  }

  async function run(text: string, opts: RunOpts = {}): Promise<ToastKind> {
    let r: ReplyView | { request_id: string }
    const acks = watchAcks()
    try {
      r = opts.button === undefined ? await runCommand(text) : await runCommand(text, opts.button)
    } catch (e) {
      acks.off()
      show(friendlyError(e, "/api/command"), "error", opts)
      return "error"
    }
    let rv: ReplyView
    if ("text" in r) {
      acks.off()
      rv = r as ReplyView
    } else {
      show(TEXT_PENDING, "info", opts)
      const late = await acks.wait(r.request_id)
      if (!late) {
        show(TEXT_LOST, "warn", opts)
        return "warn"
      }
      rv = late
    }
    const buttons: Button[] = (rv.buttons ?? []).flat()
    const yes = buttons.find(
      (b) => b.data.endsWith("confirm") || b.data.startsWith("/goal gate ok "),
    )
    if (yes) {
      closeReply()
      const nl = rv.text.indexOf("\n")
      const ok = await confirm({
        title: nl < 0 ? rv.text : rv.text.slice(0, nl),
        body: nl < 0 ? "" : rv.text.slice(nl + 1),
        confirmLabel: yes.text,
        danger: true,
        markdown: true,
      })
      if (ok) return run(yes.data, opts)
      const no = buttons.find((b) => b.data.startsWith("/goal gate no "))
      if (no) await run(no.data, opts)
      return "info"
    }
    const kind = replyKind(rv.text)
    if (buttons.length > 2) {
      reply.value = rv
      return kind
    }
    closeReply()
    if (opts.result) {
      show(rv.text, kind, opts)
      return kind
    }
    toast(rv.text, kind, {
      actions: buttons.map((b) => ({ label: b.text, data: b.data })),
      markdown: true,
    })
    return kind
  }

  return { run, reply, closeReply }
}
