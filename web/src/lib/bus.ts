// The event bus of the page. main.ts feeds it with each batch of the main stream, and stream.ts
// adds reconnected after each new connection that follows the first one.
type Fn = (data: unknown) => void

const listeners = new Map<string, Set<Fn>>()

// Adds a listener for one event name. Returns a function that removes it.
export function onEvent(name: string, fn: Fn): () => void {
  let set = listeners.get(name)
  if (!set) {
    set = new Set()
    listeners.set(name, set)
  }
  set.add(fn)
  return () => {
    listeners.get(name)?.delete(fn)
  }
}

// Calls the listeners of each event in batch order. An error of a listener does not stop the rest.
export function emitEvents(batch: { name: string; data: unknown }[]): void {
  for (const e of batch) {
    const set = listeners.get(e.name)
    if (!set) continue
    for (const fn of Array.from(set)) {
      try {
        fn(e.data)
      } catch (err) {
        console.error(err)
      }
    }
  }
}

// Calls fn after each new connection of the stream that follows the first one.
export function onReconnected(fn: () => void): () => void {
  return onEvent("reconnected", () => fn())
}
