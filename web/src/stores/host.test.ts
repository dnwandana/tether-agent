import { describe, expect, it } from "vitest"
import { host, setHost } from "./host"

describe("host store", () => {
  it("starts with no zone and offset 0", () => {
    expect(host.zone).toBe("")
    expect(host.offsetMin).toBe(0)
  })

  it("keeps the zone and the offset that the bootstrap gives", () => {
    setHost("WIB", 420)
    expect(host.zone).toBe("WIB")
    expect(host.offsetMin).toBe(420)
    setHost("UTC", 0)
    expect(host).toEqual({ zone: "UTC", offsetMin: 0 })
  })
})
