// Package version implements Verver's stable and numbered prerelease versions.
// Numeric components are limited to uint64; overflow is reported, never wrapped.
package version

import (
	"cmp"
	"fmt"
	"math"
)

// Core is the numeric target shared by a stable release and its candidates.
type Core struct {
	Major, Minor, Patch uint64
}

type Bump uint8

const (
	Patch Bump = iota
	Minor
	Major
)

// Next increments one component and resets the less significant components.
func (c Core) Next(b Bump) (Core, error) {
	switch b {
	case Patch:
		if c.Patch != math.MaxUint64 {
			return Core{c.Major, c.Minor, c.Patch + 1}, nil
		}
	case Minor:
		if c.Minor != math.MaxUint64 {
			return Core{c.Major, c.Minor + 1, 0}, nil
		}
	case Major:
		if c.Major != math.MaxUint64 {
			return Core{c.Major + 1, 0, 0}, nil
		}
	default:
		return Core{}, fmt.Errorf("unknown bump: %d", b)
	}
	return Core{}, fmt.Errorf("version component overflow")
}

func (c Core) String() string { return fmt.Sprintf("%d.%d.%d", c.Major, c.Minor, c.Patch) }

func (c Core) Compare(other Core) int {
	if n := cmp.Compare(c.Major, other.Major); n != 0 {
		return n
	}
	if n := cmp.Compare(c.Minor, other.Minor); n != 0 {
		return n
	}
	return cmp.Compare(c.Patch, other.Patch)
}

// Version has RC zero for a stable release, otherwise a positive candidate
// counter. The prerelease label belongs to the Pattern, not the release policy.
// Compare versions only within the same pattern namespace.
type Version struct {
	Core Core
	RC   uint64
}

func (v Version) Compare(other Version) int {
	if n := v.Core.Compare(other.Core); n != 0 {
		return n
	}
	if v.RC == other.RC {
		return 0
	}
	if v.RC == 0 {
		return 1
	}
	if other.RC == 0 {
		return -1
	}
	return cmp.Compare(v.RC, other.RC)
}

// NextRC advances the counter; on a stable target it starts at one.
func (v Version) NextRC() (Version, error) {
	if v.RC == math.MaxUint64 {
		return Version{}, fmt.Errorf("RC counter overflow")
	}
	v.RC++
	return v, nil
}
