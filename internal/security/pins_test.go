package security

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestPinStore(t *testing.T) {
	ctx := context.Background()
	d := openGateDB(t)
	s := &PinStore{DB: d}
	twine := PinTarget{Path: "/home/user/.local/pipx/venvs/twine/bin/twine", SHA256: "3f2a" + strings.Repeat("0", 56) + "91c0"}
	py := PinTarget{Path: "/usr/bin/python3.12", SHA256: strings.Repeat("a", 64), Interpreter: true}
	if ok, err := s.Allowed(ctx, "PYPI_TOKEN", twine); err != nil || ok {
		t.Fatal("no row must not allow:", ok, err)
	}
	for _, pt := range []PinTarget{twine, py} {
		if err := s.Allow(ctx, "PYPI_TOKEN", pt); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.Allowed(ctx, "PYPI_TOKEN", twine); err != nil || !ok {
		t.Fatal("the pin must allow:", ok, err)
	}
	if ok, _ := s.Allowed(ctx, "GH_TOKEN", twine); ok {
		t.Fatal("a pin is for one name")
	}
	// A new hash of the same path asks again.
	newer := twine
	newer.SHA256 = strings.Repeat("b", 64)
	if ok, _ := s.Allowed(ctx, "PYPI_TOKEN", newer); ok {
		t.Fatal("a new hash must not be allowed")
	}
	// Allow with the new hash replaces the old row.
	if err := s.Allow(ctx, "PYPI_TOKEN", newer); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Allowed(ctx, "PYPI_TOKEN", twine); ok {
		t.Fatal("the old hash must be gone")
	}
	got, err := s.List(ctx, "PYPI_TOKEN")
	want := []PinTarget{{Path: newer.Path, SHA256: newer.SHA256}, {Path: py.Path, SHA256: py.SHA256}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %+v, %v; want %+v", got, err, want)
	}
	var at int64
	if err := d.QueryRowContext(ctx, "SELECT min(created_at) FROM secret_pins").Scan(&at); err != nil || at <= 0 {
		t.Fatal("created_at must be Unix ms:", at, err)
	}
	if n, err := s.Unpin(ctx, "PYPI_TOKEN"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if got, err := s.List(ctx, "PYPI_TOKEN"); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if n, err := s.Unpin(ctx, "PYPI_TOKEN"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
