// Makes the device label of a user agent, for example "Chrome on Android". The order of the
// checks matters: an Android agent also names Linux, and an iPhone agent also names Mac OS X.
export function deviceLabel(ua: string): string {
  let browser = "Browser"
  // The iOS browsers name Safari, and Opera and Edge name Chrome, so their own tokens come first.
  if (/Edg(A|iOS)?\//.test(ua)) browser = "Edge"
  else if (ua.includes("OPR/")) browser = "Opera"
  else if (ua.includes("Firefox/") || ua.includes("FxiOS/")) browser = "Firefox"
  else if (ua.includes("Chrome/") || ua.includes("CriOS/")) browser = "Chrome"
  else if (ua.includes("Safari/")) browser = "Safari"
  let system = "an unknown system"
  if (ua.includes("Android")) system = "Android"
  else if (ua.includes("iPhone")) system = "iPhone"
  else if (ua.includes("iPad")) system = "iPad"
  else if (ua.includes("Mac OS X")) system = "macOS"
  else if (ua.includes("Windows")) system = "Windows"
  else if (ua.includes("Linux")) system = "Linux"
  return `${browser} on ${system}`
}
