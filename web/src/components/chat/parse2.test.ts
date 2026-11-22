import { describe, expect, it } from "vitest"
import { parseDelegate, parseReadFile, parseSessionSearch, parseWebSearch } from "./parse"

describe("parseReadFile", () => {
  it("reads the numbered lines and the next offset", () => {
    expect(
      parseReadFile(
        "   310\tfunc (s *Store) Runs() {\n   311\t\treturn nil\n[more lines follow; next_offset: 312]\n",
      ),
    ).toEqual({
      start: 310,
      lines: ["func (s *Store) Runs() {", "\treturn nil"],
      next: 312,
      note: "",
    })
  })

  it("reads an empty file", () => {
    expect(parseReadFile("[/tmp/a.md is an empty file]")).toEqual({
      start: 0,
      lines: [],
      next: null,
      note: "[/tmp/a.md is an empty file]",
    })
  })
})

describe("parseWebSearch", () => {
  it("reads the result blocks", () => {
    const t =
      "1. Keep a Changelog 1.1.0\n   https://keepachangelog.com/en/1.1.0/\n   Keep an Unreleased section at the top.\n\n2. Semantic Versioning\n   https://semver.org\n"
    expect(parseWebSearch(t)).toEqual([
      {
        title: "Keep a Changelog 1.1.0",
        url: "https://keepachangelog.com/en/1.1.0/",
        snippet: "Keep an Unreleased section at the top.",
      },
      { title: "Semantic Versioning", url: "https://semver.org", snippet: "" },
    ])
    expect(parseWebSearch('no results for "zzz"')).toEqual([])
  })
})

describe("parseSessionSearch", () => {
  it("reads the hits", () => {
    const t =
      '2 hits for "flaky", newest first:\nsession s1 · Fix the cron test · Nov 19 · msg 42: the runs test is flaky\nsession s2 · (no title) · Nov 18 · msg 7: flaky again'
    expect(parseSessionSearch(t)).toEqual([
      {
        sessionId: "s1",
        title: "Fix the cron test",
        date: "Nov 19",
        messageId: 42,
        snippet: "the runs test is flaky",
      },
      {
        sessionId: "s2",
        title: "(no title)",
        date: "Nov 18",
        messageId: 7,
        snippet: "flaky again",
      },
    ])
  })
})

describe("parseDelegate", () => {
  it("reads the results and the background start", () => {
    expect(
      parseDelegate(
        '{"results":[{"task":1,"status":"ok","summary":"Grouped 12 commits.","tool_calls":9,"duration_s":21}]}',
      ),
    ).toEqual({
      results: [
        {
          task: 1,
          status: "ok",
          summary: "Grouped 12 commits.",
          tool_calls: 9,
          duration_s: 21,
          error: "",
        },
      ],
      batch: null,
    })
    expect(parseDelegate('{"batch_id":"b7","children":["changelog-diff","pr-links"]}')).toEqual({
      results: [],
      batch: { id: "b7", children: ["changelog-diff", "pr-links"] },
    })
    expect(parseDelegate("not json")).toBeNull()
  })
})
