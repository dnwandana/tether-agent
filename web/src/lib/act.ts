import { ref, type Ref } from "vue"
import type { Button, ReplyView } from "../api/types.gen"
import { ApiError, runCommand } from "../api/client"
import { confirm, toast } from "../stores/ui"

// Gives the shared handler for a typed command and the reply that it opens.
// A reply with a button whose data ends in "confirm" opens the confirm dialog first.
// A reply with other buttons sets `reply`, and the caller shows it as a popover.
// Other replies show as a toast. `onOpen` runs when a reply with buttons opens.
export function useAct(opts: { onOpen?: () => void } = {}): {
  act: (text: string) => Promise<void>
  reply: Ref<ReplyView | null>
  closeReply: () => void
} {
  const reply = ref<ReplyView | null>(null)

  function closeReply(): void {
    reply.value = null
  }

  async function act(text: string): Promise<void> {
    let r: ReplyView | { request_id: string }
    try {
      r = await runCommand(text)
    } catch (e) {
      toast(e instanceof ApiError || e instanceof Error ? e.message : String(e), "error")
      return
    }
    if (!("text" in r)) {
      toast("The command runs. The reply comes later.", "info")
      return
    }
    const buttons: Button[] = (r.buttons ?? []).flat()
    const sure = buttons.find((b) => b.data.endsWith("confirm"))
    if (sure) {
      closeReply()
      const nl = r.text.indexOf("\n")
      const ok = await confirm({
        title: nl < 0 ? r.text : r.text.slice(0, nl),
        body: nl < 0 ? "" : r.text.slice(nl + 1),
        confirmLabel: sure.text,
        danger: true,
      })
      if (ok) await act(sure.data)
      return
    }
    if (buttons.length > 0) {
      reply.value = r
      opts.onOpen?.()
      return
    }
    closeReply()
    toast(r.text, "ok")
  }

  return { act, reply, closeReply }
}
