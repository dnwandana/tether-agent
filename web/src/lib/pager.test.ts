import { describe, expect, it, vi } from "vitest"
import {
  DEFAULT_SIZE,
  PAGE_SIZES,
  clampPage,
  lastPage,
  pageRange,
  pagerVisible,
  parsePage,
  readSize,
  writeSize,
} from "./pager"

describe("sizes", () => {
  it("has the four choices and a default of 20", () => {
    expect([...PAGE_SIZES]).toEqual([10, 20, 50, 100])
    expect(DEFAULT_SIZE).toBe(20)
  })
})

describe("readSize and writeSize", () => {
  it("gives 20 when no value is stored", () => {
    expect(readSize("sessions")).toBe(20)
  })

  it("reads a size that writeSize stored, under the key of the table", () => {
    writeSize("sessions", 50)
    expect(localStorage.getItem("tether.rows.sessions")).toBe("50")
    expect(readSize("sessions")).toBe(50)
    expect(readSize("skills-installed")).toBe(20)
  })

  it("gives 20 for a value that is not a choice", () => {
    for (const bad of ["30", "abc", "", "-10", "20.5"]) {
      localStorage.setItem("tether.rows.jobs", bad)
      expect(readSize("jobs")).toBe(20)
    }
  })

  it("gives 20 when the storage throws", () => {
    const spy = vi.spyOn(localStorage, "getItem").mockImplementation(() => {
      throw new Error("blocked")
    })
    try {
      expect(readSize("sessions")).toBe(20)
    } finally {
      spy.mockRestore()
    }
  })

  it("does not throw when the storage refuses a write", () => {
    const spy = vi.spyOn(localStorage, "setItem").mockImplementation(() => {
      throw new Error("blocked")
    })
    try {
      expect(() => writeSize("sessions", 100)).not.toThrow()
    } finally {
      spy.mockRestore()
    }
  })
})

describe("test storage", () => {
  it("stores values in one test", () => {
    writeSize("leak-check", 50)
    sessionStorage.setItem("leak-check", "x")
    expect(readSize("leak-check")).toBe(50)
  })

  it("starts the next test with empty storage", () => {
    expect(localStorage.length).toBe(0)
    expect(sessionStorage.length).toBe(0)
    expect(readSize("leak-check")).toBe(20)
  })
})

describe("page math", () => {
  it("gives the last page, with 1 for a total of 0", () => {
    expect(lastPage(0, 20)).toBe(1)
    expect(lastPage(20, 20)).toBe(1)
    expect(lastPage(21, 20)).toBe(2)
    expect(lastPage(86, 20)).toBe(5)
  })

  it("keeps the page inside 1 to the last page", () => {
    expect(clampPage(3, 86, 20)).toBe(3)
    expect(clampPage(9, 86, 20)).toBe(5)
    expect(clampPage(0, 86, 20)).toBe(1)
    expect(clampPage(-2, 86, 20)).toBe(1)
    expect(clampPage(4, 0, 20)).toBe(1)
  })

  it("shows the pager only for a total over 10", () => {
    expect(pagerVisible(0)).toBe(false)
    expect(pagerVisible(10)).toBe(false)
    expect(pagerVisible(11)).toBe(true)
  })

  it("gives the range of a page from the page, the size, and the total", () => {
    expect(pageRange(1, 20, 86)).toEqual({ start: 1, end: 20 })
    expect(pageRange(2, 20, 86)).toEqual({ start: 21, end: 40 })
    expect(pageRange(5, 20, 86)).toEqual({ start: 81, end: 86 })
    expect(pageRange(1, 20, 4)).toEqual({ start: 1, end: 4 })
    expect(pageRange(1, 20, 0)).toEqual({ start: 0, end: 0 })
  })
})

describe("parsePage", () => {
  it("reads a string of digits with a value of 1 or more", () => {
    expect(parsePage("3")).toBe(3)
    expect(parsePage("1")).toBe(1)
  })

  it("gives 1 for any other value", () => {
    for (const v of ["abc", "0", "-1", "2.5", "", ["2"], undefined, null]) {
      expect(parsePage(v)).toBe(1)
    }
  })
})
