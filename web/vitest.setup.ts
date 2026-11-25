import { cleanup } from "@testing-library/vue"
import { afterEach } from "vitest"

// Node 25 and later have a global localStorage. Without --localstorage-file it is undefined, and
// it hides the localStorage of jsdom. This file puts a memory Storage in its place for the tests,
// on each Node version.

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

// The memory Storage is also used when jsdom has a usable Storage (Node 24). The jsdom Storage
// turns a property set into a stored item, so vi.spyOn(localStorage, "setItem") sees no call.
// One Storage class gives the same tests on each Node version.
for (const name of ["localStorage", "sessionStorage"] as const) {
  Object.defineProperty(globalThis, name, {
    value: new MemoryStorage(),
    configurable: true,
    writable: true,
  })
}

// The files with "@vitest-environment node" also load this setup. They have no window.
if (typeof window !== "undefined") {
  // Reka UI v2.11.0, packages/core/vitest.setup.ts:24-32. jsdom throws when getComputedStyle gets
  // a pseudo-element, so the wrapper passes the element only.
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  const originalGetComputedStyle = window.getComputedStyle.bind(window)
  window.getComputedStyle = ((el: Element) =>
    originalGetComputedStyle(el)) as typeof getComputedStyle

  // Reka UI v2.11.0, src/Menu/Menu.test.ts:11-15. jsdom has no ResizeObserver.
  if (typeof globalThis.ResizeObserver === "undefined") {
    globalThis.ResizeObserver = class {
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
    }
  }

  // Reka UI v2.11.0, src/Drawer/Drawer.snap.test.ts:117-135. jsdom 26 has no PointerEvent and no
  // pointer capture methods.
  if (typeof globalThis.PointerEvent === "undefined") {
    globalThis.PointerEvent = class PointerEvent extends MouseEvent {
      readonly pointerId: number
      readonly pointerType: string
      readonly isPrimary: boolean

      constructor(type: string, init: PointerEventInit = {}) {
        super(type, init)
        this.pointerId = init.pointerId ?? 1
        this.pointerType = init.pointerType ?? "mouse"
        this.isPrimary = init.isPrimary ?? true
      }
    } as unknown as typeof PointerEvent
  }
  if (!Element.prototype.setPointerCapture) Element.prototype.setPointerCapture = () => {}
  if (!Element.prototype.releasePointerCapture) Element.prototype.releasePointerCapture = () => {}
  if (!Element.prototype.hasPointerCapture) Element.prototype.hasPointerCapture = () => false

  // Portal content stays on document.body unless each test unmounts its component.
  // One memory Storage serves each test file, so a stored value would leak into the next test.
  afterEach(() => {
    cleanup()
    localStorage.clear()
    sessionStorage.clear()
  })
}
