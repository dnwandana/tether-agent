import { reactive } from "vue"
import { ApiError, api } from "../api/client"
import type { BulkResult, Card, CardAnswer } from "../api/types.gen"

type Batch = { name: string; data: unknown }[]

export const cards = reactive({ open: [] as Card[] })

function close(id: string): void {
  cards.open = cards.open.filter((c) => c.id !== id)
}

export function applyCardEvents(batch: Batch): void {
  for (const e of batch) {
    switch (e.name) {
      case "hello":
        cards.open = (e.data as { cards?: Card[] }).cards ?? []
        break
      case "card": {
        const c = e.data as Card
        if (!cards.open.some((x) => x.id === c.id)) cards.open = [...cards.open, c]
        break
      }
      case "card.done":
        close((e.data as { id: string }).id)
        break
    }
  }
}

// Posts an answer. A 409 means another page answered first, so the card closes here too.
export async function answerCard(id: string, a: CardAnswer): Promise<void> {
  try {
    await api("POST", "/api/cards/" + encodeURIComponent(id), a)
  } catch (e) {
    if (e instanceof ApiError && e.status === 409) close(id)
    throw e
  }
}

// Returns the count of cards that the server answered. A card that closed before the post is not in it.
export async function bulkAnswer(ids: string[], answer: "deny" | "once"): Promise<number> {
  const r = await api<BulkResult>("POST", "/api/cards/bulk", { answer, ids })
  return r.answered
}
