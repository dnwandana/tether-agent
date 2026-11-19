package security

import "testing"

func FuzzScan(f *testing.F) {
	for _, s := range []string{"4111 1111 1111 1111", "GB82 WEST 1234 5698 7654 32", "+94 77 123 4567",
		`{"a":"\n4111111111111111"}`, "sk-4111111111111111abc", "Order 12 4111 1111 1111 1111 today.",
		"On 2026-11-19 4111-1111-1111-1111 was charged", "Ids 99 88 4111 1111 1111 1111", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fs := Scan(s, nil, nil)
		prev := 0
		for _, x := range fs {
			if x.Start < prev || x.End <= x.Start || x.End > len(s) || x.Label == "" {
				t.Fatalf("bad finding %+v in %q", x, s)
			}
			prev = x.End
		}
		_ = RedactFindings(s, fs)
	})
}
