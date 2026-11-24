import { reactive } from "vue"
import { ApiError, api } from "../api/client"
import type { AnsweredCard, BulkResult, Card, CardAnswer, CardsView } from "../api/types.gen"

type Batch = { name: string; data: unknown }[]

export const cards = reactive({ open: [] as Card[], answered: [] as AnsweredCard[] })

function close(id: string): void {
  cards.open = cards.open.filter((c) => c.id !== id)
}

// The server ring holds 30 answers. The page keeps the same count.
const ANSWERED_MAX = 30

// Merges records into answered: one record for each id, newest first, at most 30.
function addAnswered(list: AnsweredCard[]): void {
  const byId = new Map<string, AnsweredCard>()
  for (const r of [...cards.answered, ...list]) byId.set(r.id, r)
  cards.answered = [...byId.values()]
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
    .slice(0, ANSWERED_MAX)
}

export function applyCardEvents(batch: Batch): void {
  for (const e of batch) {
    switch (e.name) {
      case "hello": {
        const d = e.data as { cards?: Card[]; answered?: AnsweredCard[] }
        cards.open = d.cards ?? []
        addAnswered(d.answered ?? [])
        break
      }
      case "card": {
        const c = e.data as Card
        if (!cards.open.some((x) => x.id === c.id)) cards.open = [...cards.open, c]
        break
      }
      case "card.done": {
        const r = e.data as AnsweredCard
        close(r.id)
        addAnswered([r])
        break
      }
    }
  }
}

// Posts an answer. A 409 means another page answered first. The card then moves to answered,
// from its card.done or from GET /api/cards, and the call does not throw.
export async function answerCard(id: string, a: CardAnswer): Promise<void> {
  try {
    await api("POST", "/api/cards/" + encodeURIComponent(id), a)
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 409) throw e
    if (cards.answered.some((r) => r.id === id)) {
      close(id)
      return
    }
    try {
      const v = await api<CardsView>("GET", "/api/cards")
      cards.open = v.open ?? []
      addAnswered(v.answered ?? [])
    } catch {
      close(id)
    }
  }
}

// Returns the count of cards that the server answered. A card that closed before the post is not in it.
export async function bulkAnswer(ids: string[], answer: "deny" | "once"): Promise<number> {
  const r = await api<BulkResult>("POST", "/api/cards/bulk", { answer, ids })
  return r.answered
}
