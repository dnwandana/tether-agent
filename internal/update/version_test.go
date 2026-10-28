package update

import "testing"

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.4.0", "0.4.1", -1}, {"v0.4.1", "0.4.1", 0}, {"1.0.0", "0.9.9", 1},
		{"1.2.0-rc1", "1.2.0", -1}, {"1.2.0", "1.2.0-rc1", 1}, {"1.2.0-rc1", "1.2.0-rc2", -1},
		{"0.10.0", "0.9.0", 1},
	} {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range [][2]string{
		{"dev", "0.1.0"}, {"1.2", "1.2.0"}, {"1.2.x", "1.2.0"}, {"0.1.0", ""},
		{"1..0", "1.0.0"}, {"+1.0.0", "1.0.0"}, {"1.0.0", "1.-1.0"}, {"1.2.0-", "1.2.0"},
		{"1.2.0.0", "1.2.0"},
	} {
		if _, err := Compare(bad[0], bad[1]); err == nil {
			t.Errorf("Compare(%q, %q) must fail", bad[0], bad[1])
		}
	}
}

func TestIsDev(t *testing.T) {
	if !IsDev("dev") || !IsDev("") || IsDev("0.1.0") {
		t.Fatal("IsDev")
	}
}
