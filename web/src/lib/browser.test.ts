import { describe, expect, it } from "vitest"
import { deviceLabel } from "./browser"

describe("deviceLabel", () => {
  it.each([
    [
      "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/131.0 Mobile Safari/537.36",
      "Chrome on Android",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 CriOS/131.0 Mobile/15E148 Safari/604.1",
      "Chrome on iPhone",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 FxiOS/133.0 Mobile/15E148 Safari/605.1.15",
      "Firefox on iPhone",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 EdgiOS/131.0 Mobile/15E148 Safari/605.1.15",
      "Edge on iPhone",
    ],
    [
      "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1",
      "Safari on iPhone",
    ],
    [
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0 Safari/537.36 OPR/115.0",
      "Opera on Windows",
    ],
    [
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/131.0 Safari/537.36 Edg/131.0",
      "Edge on Windows",
    ],
    ["curl/8.0", "Browser on an unknown system"],
  ])("labels %s", (ua, want) => {
    expect(deviceLabel(ua)).toBe(want)
  })
})
