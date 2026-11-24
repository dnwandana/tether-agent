import { watch, type Ref } from "vue"
import type { ReplyView } from "../api/types.gen"
import { useCommand } from "./command"

// Keeps the old API of the chat for useCommand. onOpen runs when a reply with buttons opens.
export function useAct(opts: { onOpen?: () => void } = {}): {
  act: (text: string) => Promise<void>
  reply: Ref<ReplyView | null>
  closeReply: () => void
} {
  const { run, reply, closeReply } = useCommand()
  watch(reply, (r) => {
    if (r) opts.onOpen?.()
  })
  return {
    act: async (text: string) => {
      await run(text)
    },
    reply,
    closeReply,
  }
}
