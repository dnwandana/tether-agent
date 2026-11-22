export interface Unwrapped {
  body: string
  untrusted: boolean
  source: string
}

const untrustedRe =
  /^<untrusted_tool_result source="([^"]*)">\n[^\n]*\n\n([\s\S]*?)\n<\/untrusted_tool_result>\n?([\s\S]*)$/

// Takes the content out of an <untrusted_tool_result> block.
export function unwrapUntrusted(text: string): Unwrapped {
  const m = untrustedRe.exec(text)
  if (!m) return { body: text, untrusted: false, source: "" }
  const after = m[3]
  return { body: after === "" ? m[2] : m[2] + "\n" + after, untrusted: true, source: m[1] }
}

export interface TermResult {
  output: string
  exit: number | null
  notes: string[]
  background: { pid: number; logPath: string } | null
}

const exitRe = /^\[exit code: (-?\d+)\]$/
const failRe = /^\[(?:timed out after .*|killed by signal .*|wait failed: .*)\]$/

// Reads the output, the exit code, and the note lines of a terminal or execute_code result.
export function parseTerminal(text: string): TermResult {
  try {
    const j = JSON.parse(text)
    if (j && typeof j === "object" && typeof j.pid === "number") {
      return {
        output: "",
        exit: null,
        notes: [],
        background: { pid: j.pid, logPath: typeof j.log_path === "string" ? j.log_path : "" },
      }
    }
  } catch {
    // Not JSON. Read it as output.
  }
  const lines = (text.endsWith("\n") ? text.slice(0, -1) : text).split("\n")
  const out: string[] = []
  const notes: string[] = []
  let exit = 0
  let ended = false
  for (const line of lines) {
    if (ended) {
      notes.push(line)
      continue
    }
    const m = exitRe.exec(line)
    if (m) {
      exit = Number(m[1])
      ended = true
    } else if (failRe.test(line)) {
      exit = -1
      notes.push(line)
      ended = true
    } else if (line === "[no output]") {
      notes.push(line)
      ended = true
    } else {
      out.push(line)
    }
  }
  return { output: out.join("\n"), exit, notes, background: null }
}

export interface DiffLine {
  kind: "hunk" | "ctx" | "add" | "del"
  text: string
  a: number | null
  b: number | null
}
export interface DiffResult {
  path: string
  summary: string
  lines: DiffLine[]
  added: number
  removed: number
}

const hunkRe = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/

// Returns the first line number of a hunk side. An empty side starts at the next line.
function startOf(start: string, count: string | undefined): number {
  const s = Number(start)
  return count !== undefined && Number(count) === 0 ? s + 1 : s
}

// Reads the result of the patch tool.
export function parseDiff(text: string): DiffResult {
  const src = text.split("\n")
  if (src.length > 0 && src[src.length - 1] === "") src.pop()
  const res: DiffResult = { path: "", summary: "", lines: [], added: 0, removed: 0 }
  let a = 0
  let b = 0
  let inHunk = false
  for (let i = 0; i < src.length; i++) {
    const line = src[i]
    if (i === 0 && line.startsWith("patched ")) {
      res.summary = line
      continue
    }
    const h = hunkRe.exec(line)
    if (h) {
      a = startOf(h[1], h[2])
      b = startOf(h[3], h[4])
      inHunk = true
      res.lines.push({ kind: "hunk", text: line, a: null, b: null })
      continue
    }
    if (!inHunk) {
      if (line.startsWith("+++ b/")) res.path = line.slice(6)
      continue
    }
    if (line.startsWith("\\")) continue
    const sign = line.charAt(0)
    const rest = line.slice(1)
    if (sign === "-") {
      res.lines.push({ kind: "del", text: rest, a: a++, b: null })
      res.removed++
    } else if (sign === "+") {
      res.lines.push({ kind: "add", text: rest, a: null, b: b++ })
      res.added++
    } else {
      res.lines.push({ kind: "ctx", text: rest, a: a++, b: b++ })
    }
  }
  return res
}

export interface NavResult {
  title: string
  url: string
  status: string
  excerpt: string
}

// Reads the result of browser_navigate.
export function parseNavigate(text: string): NavResult {
  const res: NavResult = { title: "", url: "", status: "", excerpt: "" }
  const lines = text.split("\n")
  let i = 0
  for (; i < lines.length && lines[i] !== ""; i++) {
    const l = lines[i]
    if (l.startsWith("title: ")) res.title = l.slice(7)
    else if (l.startsWith("final_url: ")) res.url = l.slice(11)
    else if (l.startsWith("status: ")) res.status = l.slice(8)
  }
  res.excerpt = lines.slice(i + 1).join("\n")
  return res
}

// Returns the first /api/files/<id> path in the text, or "".
export function fileLink(text: string): string {
  const m = /\/api\/files\/[A-Za-z0-9_-]+/.exec(text)
  return m ? m[0] : ""
}

export interface ReadResult {
  start: number
  lines: string[]
  next: number | null
  note: string
}

const codeLineRe = /^\s*(\d+)\t(.*)$/
const moreRe = /^\[more lines follow; next_offset: (\d+)\]$/

// Reads the result of read_file.
export function parseReadFile(text: string): ReadResult {
  const body = unwrapUntrusted(text).body
  const src = body.split("\n")
  if (src.length > 0 && src[src.length - 1] === "") src.pop()
  const res: ReadResult = { start: 0, lines: [], next: null, note: "" }
  const notes: string[] = []
  for (const line of src) {
    const c = codeLineRe.exec(line)
    if (c) {
      if (res.lines.length === 0) res.start = Number(c[1])
      res.lines.push(c[2])
      continue
    }
    const m = moreRe.exec(line)
    if (m) res.next = Number(m[1])
    else if (line.startsWith("[")) notes.push(line)
  }
  res.note = notes.join("\n")
  return res
}

export interface WebHit {
  title: string
  url: string
  snippet: string
}

// Reads the result of web_search. No hit gives [].
export function parseWebSearch(text: string): WebHit[] {
  const lines = unwrapUntrusted(text).body.split("\n")
  const hits: WebHit[] = []
  for (let i = 0; i < lines.length; i++) {
    const m = /^\d+\. (.*)$/.exec(lines[i])
    if (!m) continue
    const url = (lines[i + 1] ?? "").replace(/^ {3}/, "").trim()
    const next = lines[i + 2] ?? ""
    const snippet = next.startsWith("   ") ? next.slice(3).trim() : ""
    hits.push({ title: m[1], url, snippet })
    i += snippet ? 2 : 1
  }
  return hits
}

export interface SessionHit {
  sessionId: string
  title: string
  date: string
  messageId: number
  snippet: string
}

const sessionHitRe = /^session (\S+) · (.*) · (.*?) · msg (\d+): (.*)$/

// Reads the hit lines of session_search.
export function parseSessionSearch(text: string): SessionHit[] {
  const hits: SessionHit[] = []
  for (const line of unwrapUntrusted(text).body.split("\n")) {
    const m = sessionHitRe.exec(line)
    if (m)
      hits.push({
        sessionId: m[1],
        title: m[2],
        date: m[3],
        messageId: Number(m[4]),
        snippet: m[5],
      })
  }
  return hits
}

export interface ChildResult {
  task: number
  status: string
  summary: string
  tool_calls: number
  duration_s: number
  error: string
}
export interface DelegateResult {
  results: ChildResult[]
  batch: { id: string; children: string[] } | null
}

// Reads the result JSON of delegate_task, or returns null when it is not that JSON.
export function parseDelegate(text: string): DelegateResult | null {
  let j: unknown
  try {
    j = JSON.parse(unwrapUntrusted(text).body)
  } catch {
    return null
  }
  if (!j || typeof j !== "object" || Array.isArray(j)) return null
  const o = j as Record<string, unknown>
  if (Array.isArray(o.results)) {
    const results = o.results.map((r) => {
      const c = (r && typeof r === "object" ? r : {}) as Record<string, unknown>
      return {
        task: Number(c.task ?? 0),
        status: typeof c.status === "string" ? c.status : "",
        summary: typeof c.summary === "string" ? c.summary : "",
        tool_calls: Number(c.tool_calls ?? 0),
        duration_s: Number(c.duration_s ?? 0),
        error: typeof c.error === "string" ? c.error : "",
      }
    })
    return { results, batch: null }
  }
  if (typeof o.batch_id === "string") {
    const children = Array.isArray(o.children)
      ? o.children.filter((c): c is string => typeof c === "string")
      : []
    return { results: [], batch: { id: o.batch_id, children } }
  }
  return null
}
