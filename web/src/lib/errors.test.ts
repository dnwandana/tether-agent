import { describe, expect, it } from "vitest"
import { friendlyError } from "./errors"

// An object with the shape of ApiError. The mapper reads the shape, not the class.
const apiErr = (status: number, serverText: string | null, message = `HTTP ${status}`) =>
  Object.assign(new Error(message), { status, serverText })

describe("friendlyError", () => {
  it("uses the server sentence when the body has one", () => {
    expect(friendlyError(apiErr(429, "Too many sign-in requests. Wait one minute."))).toBe(
      "Too many sign-in requests. Wait one minute.",
    )
    expect(friendlyError(apiErr(413, "The file is larger than 20 MB."))).toBe(
      "The file is larger than 20 MB.",
    )
    expect(
      friendlyError(apiErr(401, "The code is not valid or has expired."), "/auth/signin"),
    ).toBe("The code is not valid or has expired.")
  })

  it("uses the table when the body has no sentence", () => {
    expect(friendlyError(apiErr(0, null))).toBe(
      "Cannot reach the gateway. Check the network, then try again.",
    )
    expect(friendlyError(apiErr(401, null), "/api/jobs")).toBe(
      "Your session ended. Request access again.",
    )
    expect(friendlyError(apiErr(401, null), "/auth/signin")).toBe(
      "The code is not valid, or it expired.",
    )
    expect(friendlyError(apiErr(413, null))).toBe("The upload is too large.")
    expect(friendlyError(apiErr(429, null))).toBe("Too many requests. Wait, then try again.")
    expect(friendlyError(apiErr(502, null))).toBe(
      "The gateway failed. See the log on System operations.",
    )
    expect(friendlyError(apiErr(404, null))).toBe("HTTP 404")
  })

  it("maps a fetch TypeError and the microphone errors", () => {
    expect(friendlyError(new TypeError("Failed to fetch"))).toBe(
      "Cannot reach the gateway. Check the network, then try again.",
    )
    expect(friendlyError(new DOMException("denied", "NotAllowedError"))).toBe(
      "The browser blocked the microphone.",
    )
    expect(friendlyError(new DOMException("none", "NotFoundError"))).toBe("No microphone found.")
  })

  it("keeps the text of other errors", () => {
    expect(friendlyError(new Error("Pick a file first."))).toBe("Pick a file first.")
    expect(friendlyError("plain")).toBe("plain")
  })
})
