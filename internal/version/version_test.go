package version

import (
	"math"
	"testing"
)

func TestBumps(t *testing.T) {
	for _, tt := range []struct {
		bump Bump
		want Core
	}{{Patch, Core{2, 3, 5}}, {Minor, Core{2, 4, 0}}, {Major, Core{3, 0, 0}}} {
		got, err := (Core{2, 3, 4}).Next(tt.bump)
		if err != nil || got != tt.want {
			t.Fatalf("bump %d: %v, %v", tt.bump, got, err)
		}
	}
	for _, tt := range []struct {
		core Core
		bump Bump
	}{{Core{Patch: math.MaxUint64}, Patch}, {Core{Minor: math.MaxUint64}, Minor}, {Core{Major: math.MaxUint64}, Major}, {Core{}, Bump(99)}} {
		if _, err := tt.core.Next(tt.bump); err == nil {
			t.Errorf("expected error for %+v", tt)
		}
	}
	got, err := (Core{1, 2, math.MaxUint64}).Next(Minor)
	if err != nil || got != (Core{1, 3, 0}) {
		t.Fatal("reset must allow overflowing lower component")
	}
}

func TestOrdering(t *testing.T) {
	ordered := []Version{
		{Core: Core{0, 0, 0}}, {Core: Core{0, 0, 1}, RC: 1}, {Core: Core{0, 0, 1}, RC: 2},
		{Core: Core{0, 0, 1}, RC: 10}, {Core: Core{0, 0, 1}}, {Core: Core{0, 0, 2}, RC: 1},
		{Core: Core{0, 1, 0}, RC: 1}, {Core: Core{1, 0, 0}}, {Core: Core{10, 0, 0}},
	}
	for i, a := range ordered {
		for j, b := range ordered {
			want := 0
			if i < j {
				want = -1
			}
			if i > j {
				want = 1
			}
			if got := a.Compare(b); got != want {
				t.Errorf("%+v compare %+v = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestNextRC(t *testing.T) {
	v := Version{Core: Core{1, 2, 3}}
	for i := uint64(1); i <= 3; i++ {
		next, err := v.NextRC()
		if err != nil || next.Core != v.Core || next.RC != i {
			t.Fatal(next, err)
		}
		v = next
	}
	if _, err := (Version{RC: math.MaxUint64}).NextRC(); err == nil {
		t.Fatal("expected overflow")
	}
}
