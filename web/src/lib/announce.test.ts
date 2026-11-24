import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { announce } from "./announce"

beforeEach(() => {
  vi.useFakeTimers()
  document.body.innerHTML = '<div class="announce sr" aria-live="polite"></div>'
})
afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ""
})

describe("announce", () => {
  it("puts the text in the live region after a short clear", () => {
    const region = document.querySelector(".announce")!
    region.textContent = "Question: Which branch?"
    announce("Question: Which branch?")
    expect(region.textContent).toBe("")
    vi.advanceTimersByTime(50)
    expect(region.textContent).toBe("Question: Which branch?")
  })

  it("ends on the newest text after two fast calls", () => {
    const region = document.querySelector(".announce")!
    announce("first")
    vi.advanceTimersByTime(30)
    announce("second")
    vi.advanceTimersByTime(25)
    expect(region.textContent).toBe("")
    vi.advanceTimersByTime(100)
    expect(region.textContent).toBe("second")
    announce("third")
    announce("fourth")
    vi.advanceTimersByTime(60)
    expect(region.textContent).toBe("fourth")
  })

  it("does nothing when no live region exists", () => {
    document.body.innerHTML = ""
    expect(() => announce("Approval needed: ls")).not.toThrow()
  })
})
