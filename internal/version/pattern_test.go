package version

import (
	"math"
	"testing"
)

func TestPatterns(t *testing.T) {
	for _, tt := range []struct{ main, feature, stable, rc string }{
		{"", "", "v1.2.3", "v1.2.3-rc.12"},
		{"MAJOR.MINOR.PATCH", "-rc.RC", "1.2.3", "1.2.3-rc.12"},
		{"release-MAJOR.MINOR.PATCH", "-beta.RC", "release-1.2.3", "release-1.2.3-beta.12"},
		{"api/vMAJOR.MINOR.PATCH", "-preview-2.RC", "api/v1.2.3", "api/v1.2.3-preview-2.12"},
	} {
		p, err := NewPattern(tt.main, tt.feature)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			v   Version
			tag string
		}{{Version{Core: Core{1, 2, 3}}, tt.stable}, {Version{Core: Core{1, 2, 3}, RC: 12}, tt.rc}} {
			tag, err := p.Format(tc.v)
			if err != nil || tag != tc.tag {
				t.Fatal(tag, err)
			}
			got, err := p.Parse(tc.tag)
			if err != nil || got != tc.v {
				t.Fatal(got, err)
			}
		}
	}
}

func TestInvalidPatterns(t *testing.T) {
	for _, main := range []string{"v0.0.0", "vMAJOR.MINOR", "vMINOR.MAJOR.PATCH", "vMAJOR.MINOR.PATCH.BUILD", "YYYY.MM.DD", "MAJOR-MAJOR.MINOR.PATCH", "a//MAJOR.MINOR.PATCH", ".a/MAJOR.MINOR.PATCH", "a.lock/MAJOR.MINOR.PATCH", "a..MAJOR.MINOR.PATCH", "a@{MAJOR.MINOR.PATCH", "a MAJOR.MINOR.PATCH", "/MAJOR.MINOR.PATCH", "a\\MAJOR.MINOR.PATCH", "a\x7fMAJOR.MINOR.PATCH", "a~MAJOR.MINOR.PATCH", "a[MAJOR.MINOR.PATCH"} {
		if _, err := NewPattern(main, ""); err == nil {
			t.Errorf("accepted %q", main)
		}
	}
	for _, feature := range []string{"rc.RC", "-rc.0", "-RC.RC", "-.RC", "-1rc.RC", "-rc.RC.foo", "-rc.RC+meta", "-rc_name.RC", "-rc.x.RC"} {
		if _, err := NewPattern("", feature); err == nil {
			t.Errorf("accepted %q", feature)
		}
	}
}

func TestInvalidTags(t *testing.T) {
	p, _ := NewPattern("", "")
	for _, tag := range []string{"", "1.2.3", "v1.2", "v1.2.3.4", "v01.2.3", "v1.02.3", "v1.2.03", "v+1.2.3", "v1.2.-3", "v1.2.3+meta", "v1.2.3-rc.0", "v1.2.3-rc.01", "v1.2.3-rc.", "v1.2.3-beta.1", "v1.2.3-rc.1.2", "v1.2.3-rc.1+meta", "v1.2.3-rc.-1", "v1.2.3-rc.1\n", "v18446744073709551616.0.0", "v1.2.3-rc.18446744073709551616"} {
		if _, err := p.Parse(tag); err == nil {
			t.Errorf("accepted %q", tag)
		}
	}
	v := Version{Core: Core{math.MaxUint64, math.MaxUint64, math.MaxUint64}, RC: math.MaxUint64}
	tag, err := p.Format(v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Parse(tag)
	if err != nil || got != v {
		t.Fatal(got, err)
	}
	var zero Pattern
	if _, err := zero.Parse("1.2.3"); err == nil {
		t.Fatal("zero pattern parsed")
	}
	if _, err := zero.Format(Version{}); err == nil {
		t.Fatal("zero pattern formatted")
	}
}

func TestParseCore(t *testing.T) {
	for _, value := range []string{"0.0.0", "1.2.3", "18446744073709551615.0.9"} {
		core, err := ParseCore(value)
		if err != nil || core.String() != value {
			t.Fatalf("%q: %+v, %v", value, core, err)
		}
	}
	for _, value := range []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.-2.3", "18446744073709551616.0.0"} {
		if _, err := ParseCore(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	for _, tag := range []string{"v0.0.0", "v1.2.3-rc.1", "v1.2.3-rc.10", "v01.2.3", "v1.2.3+meta"} {
		f.Add(tag)
	}
	p, _ := NewPattern("", "")
	f.Fuzz(func(t *testing.T, tag string) {
		v, err := p.Parse(tag)
		if err != nil {
			return
		}
		got, err := p.Format(v)
		if err != nil || got != tag {
			t.Fatalf("%q -> %+v -> %q (%v)", tag, v, got, err)
		}
	})
}
