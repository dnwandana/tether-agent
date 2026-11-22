// Node 25 and later have a global localStorage. Without --localstorage-file it is undefined, and
// it hides the localStorage of jsdom. This file puts a memory Storage in its place for the tests.

class MemoryStorage implements Storage {
  private items = new Map<string, string>()

  get length(): number {
    return this.items.size
  }

  clear(): void {
    this.items.clear()
  }

  getItem(key: string): string | null {
    return this.items.has(key) ? (this.items.get(key) as string) : null
  }

  key(index: number): string | null {
    return [...this.items.keys()][index] ?? null
  }

  removeItem(key: string): void {
    this.items.delete(key)
  }

  setItem(key: string, value: string): void {
    this.items.set(key, String(value))
  }
}

function usable(s: Storage | undefined): boolean {
  try {
    if (!s) return false
    s.setItem("__tether_probe", "1")
    s.removeItem("__tether_probe")
    return true
  } catch {
    return false
  }
}

for (const name of ["localStorage", "sessionStorage"] as const) {
  let current: Storage | undefined
  try {
    current = globalThis[name]
  } catch {
    current = undefined
  }
  if (!usable(current)) {
    Object.defineProperty(globalThis, name, {
      value: new MemoryStorage(),
      configurable: true,
      writable: true,
    })
  }
}
