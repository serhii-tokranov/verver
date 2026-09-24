package version

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	DefaultMainPattern    = "vMAJOR.MINOR.PATCH"
	DefaultFeaturePattern = "-rc.RC"
	corePattern           = "MAJOR.MINOR.PATCH"
)

// Pattern is an exact tag formatter/parser. Construct it with NewPattern.
type Pattern struct {
	prefix string
	label  string
}

// NewPattern accepts a literal prefix followed by MAJOR.MINOR.PATCH and a
// suffix of -<label>.RC. Empty inputs select the respective defaults.
func NewPattern(main, feature string) (Pattern, error) {
	if main == "" {
		main = DefaultMainPattern
	}
	if feature == "" {
		feature = DefaultFeaturePattern
	}
	if !strings.HasSuffix(main, corePattern) {
		return Pattern{}, fmt.Errorf("main pattern must end with %s", corePattern)
	}
	prefix := strings.TrimSuffix(main, corePattern)
	for _, token := range []string{"MAJOR", "MINOR", "PATCH", "RC"} {
		if strings.Contains(prefix, token) {
			return Pattern{}, fmt.Errorf("repeated or misplaced token %s", token)
		}
	}
	if !strings.HasPrefix(feature, "-") || !strings.HasSuffix(feature, ".RC") {
		return Pattern{}, fmt.Errorf("feature pattern must be -<label>.RC")
	}
	label := strings.TrimSuffix(strings.TrimPrefix(feature, "-"), ".RC")
	if !validLabel(label) {
		return Pattern{}, fmt.Errorf("feature label must start with a lowercase letter and contain only lowercase letters, digits, or hyphens")
	}
	if !validTag(prefix + "0.0.0") {
		return Pattern{}, fmt.Errorf("main pattern produces an invalid Git tag")
	}
	return Pattern{prefix: prefix, label: label}, nil
}

func validLabel(s string) bool {
	if len(s) == 0 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// validTag checks the ref-name restrictions for refs/tags/<tag>.
func validTag(tag string) bool {
	if strings.Contains(tag, "..") || strings.Contains(tag, "@{") || strings.HasSuffix(tag, ".") {
		return false
	}
	for _, r := range tag {
		if r <= ' ' || r == 127 || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, part := range strings.Split(tag, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (p Pattern) Format(v Version) (string, error) {
	if p.label == "" {
		return "", fmt.Errorf("uninitialized pattern")
	}
	tag := p.prefix + v.Core.String()
	if v.RC != 0 {
		tag += fmt.Sprintf("-%s.%d", p.label, v.RC)
	}
	return tag, nil
}

// Parse accepts only canonical tags in this namespace, including stable tags.
// It rejects metadata, extra prerelease fields, leading zeros, and RC zero.
func (p Pattern) Parse(tag string) (Version, error) {
	if p.label == "" {
		return Version{}, fmt.Errorf("uninitialized pattern")
	}
	if !strings.HasPrefix(tag, p.prefix) {
		return Version{}, fmt.Errorf("tag does not match prefix %q", p.prefix)
	}
	base := strings.TrimPrefix(tag, p.prefix)
	core, suffix, hasSuffix := strings.Cut(base, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("tag must contain three numeric components")
	}
	var values [3]uint64
	for i, part := range parts {
		n, err := number(part)
		if err != nil {
			return Version{}, fmt.Errorf("invalid core component: %w", err)
		}
		values[i] = n
	}
	v := Version{Core: Core{values[0], values[1], values[2]}}
	if hasSuffix {
		prefix := p.label + "."
		if !strings.HasPrefix(suffix, prefix) {
			return Version{}, fmt.Errorf("tag does not match prerelease label %q", p.label)
		}
		n, err := number(strings.TrimPrefix(suffix, prefix))
		if err != nil {
			return Version{}, fmt.Errorf("invalid RC counter: %w", err)
		}
		if n == 0 {
			return Version{}, fmt.Errorf("RC counter must start at one")
		}
		v.RC = n
	}
	return v, nil
}

func number(s string) (uint64, error) {
	if s == "" || len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("expected integer without leading zeros")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("expected decimal integer")
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("integer exceeds uint64 range")
	}
	return n, nil
}
