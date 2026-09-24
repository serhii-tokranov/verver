// Package release calculates assignments without network access or writes.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"

	"verver/internal/repository"
	"verver/internal/version"
)

const annotationPrefix = "verver/v1\n"

type Config struct {
	MainBranch     string `json:"main_branch"`
	MainPattern    string `json:"main_pattern"`
	FeaturePattern string `json:"feature_pattern"`
}

func (c Config) Normalize() (Config, error) {
	if c.MainBranch == "" {
		c.MainBranch = "main"
	}
	if c.MainPattern == "" {
		c.MainPattern = version.DefaultMainPattern
	}
	if c.FeaturePattern == "" {
		c.FeaturePattern = version.DefaultFeaturePattern
	}
	if err := ValidateRef("refs/heads/" + c.MainBranch); err != nil {
		return c, err
	}
	_, err := version.NewPattern(c.MainPattern, c.FeaturePattern)
	return c, err
}

func ValidateRef(ref string) error {
	if !strings.HasPrefix(ref, "refs/heads/") || plumbing.ReferenceName(ref).Validate() != nil {
		return fmt.Errorf("expected a full branch ref, got %q", ref)
	}
	return nil
}

func ParseHash(s string) (plumbing.Hash, error) {
	if len(s) != 40 {
		return plumbing.ZeroHash, fmt.Errorf("expected a full 40-character commit SHA")
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return plumbing.ZeroHash, fmt.Errorf("invalid commit SHA")
		}
	}
	h := plumbing.NewHash(s)
	if h.IsZero() {
		return h, fmt.Errorf("zero commit SHA is invalid")
	}
	return h, nil
}

// Source identifies original history integrated by a rewritten merge. Pull is
// optional for other integrations; GitHub uses it to retrieve preserved heads.
type Source struct {
	Head string `json:"head"`
	Pull int    `json:"pull,omitempty"`
}

type Assignment struct {
	Config   Config   `json:"config"`
	Ref      string   `json:"ref"`
	Commit   string   `json:"commit"`
	Tag      string   `json:"tag"`
	Sources  []Source `json:"sources,omitempty"`
	Consumed []string `json:"consumed,omitempty"`
}

func (a Assignment) Annotation() (string, error) {
	b, err := json.Marshal(a)
	return annotationPrefix + string(b) + "\n", err
}

func Decode(tag repository.Tag) (*Assignment, error) {
	if !strings.HasPrefix(tag.Annotation, "verver/") {
		return nil, nil
	}
	if !strings.HasPrefix(tag.Annotation, annotationPrefix) {
		return nil, fmt.Errorf("tag %s has an unsupported Verver annotation", tag.Name)
	}
	var a Assignment
	dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(tag.Annotation, annotationPrefix)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("tag %s has invalid metadata: %w", tag.Name, err)
	}
	// Require one complete JSON value, not a valid prefix followed by garbage.
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("tag %s has trailing metadata", tag.Name)
	}
	normalized, err := a.Config.Normalize()
	if err != nil || normalized != a.Config || a.Tag != tag.Name || a.Commit != tag.Commit.String() {
		return nil, fmt.Errorf("tag %s has inconsistent metadata", tag.Name)
	}
	if err := ValidateRef(a.Ref); err != nil {
		return nil, err
	}
	if _, err := ParseHash(a.Commit); err != nil {
		return nil, err
	}
	p, _ := version.NewPattern(a.Config.MainPattern, a.Config.FeaturePattern)
	v, err := p.Parse(tag.Name)
	if err != nil || (v.RC == 0) != (a.Ref == "refs/heads/"+a.Config.MainBranch) || v.RC != 0 && (len(a.Sources) > 0 || len(a.Consumed) > 0) {
		return nil, fmt.Errorf("tag %s has inconsistent release kind", tag.Name)
	}
	for _, src := range a.Sources {
		if _, err := ParseHash(src.Head); err != nil || src.Pull < 0 {
			return nil, fmt.Errorf("tag %s has invalid source metadata", tag.Name)
		}
	}
	for _, id := range a.Consumed {
		if _, err := ParseHash(id); err != nil {
			return nil, fmt.Errorf("tag %s has invalid consumed commit metadata", tag.Name)
		}
	}
	return &a, nil
}

type History interface {
	Commit(plumbing.Hash) (repository.Commit, error)
	Range(context.Context, plumbing.Hash, []plumbing.Hash, []plumbing.Hash) ([]repository.Commit, error)
}

type State struct {
	Tags   []repository.Tag
	Main   plumbing.Hash
	Branch plumbing.Hash
}

type Request struct {
	Config  Config
	Ref     string
	Commit  plumbing.Hash
	Adopt   bool
	Sources []Source
}

type Result struct {
	Tag        string     `json:"tag"`
	Commit     string     `json:"sha"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Assignment Assignment `json:"-"`
}

// Inspect validates metadata and returns the latest stable release and consumed
// original commit IDs. Matching unmanaged tags require explicit adoption.
func Inspect(tags []repository.Tag, c Config, adopt bool) (*repository.Tag, []plumbing.Hash, error) {
	p, err := version.NewPattern(c.MainPattern, c.FeaturePattern)
	if err != nil {
		return nil, nil, err
	}
	var stable *repository.Tag
	var consumed []plumbing.Hash
	identities := map[string]string{}
	managed, matchingNamespace := false, false
	for _, tag := range tags {
		a, err := Decode(tag)
		if err != nil {
			return nil, nil, err
		}
		v, parseErr := p.Parse(tag.Name)
		if a != nil {
			managed = true
			if a.Config.MainPattern == c.MainPattern {
				matchingNamespace = true
			}
		}
		if a != nil && a.Config.MainPattern == c.MainPattern && a.Config != c {
			return nil, nil, fmt.Errorf("configuration changed in namespace %s; explicit migration is required", c.MainPattern)
		}
		if parseErr != nil {
			continue
		}
		if a == nil && !adopt {
			return nil, nil, fmt.Errorf("tag %s was not created by Verver; use --adopt-existing to accept existing history", tag.Name)
		}
		if a != nil {
			if a.Config != c {
				return nil, nil, fmt.Errorf("tag %s belongs to another configuration", tag.Name)
			}
			key := a.Ref + "\n" + a.Commit
			if previous, ok := identities[key]; ok {
				return nil, nil, fmt.Errorf("conflicting assignments %s and %s", previous, tag.Name)
			}
			identities[key] = tag.Name
			for _, id := range a.Consumed {
				consumed = append(consumed, plumbing.NewHash(id))
			}
		}
		if v.RC == 0 && (stable == nil || v.Compare(stable.Version) > 0) {
			copy := tag
			copy.Version = v
			stable = &copy
		}
	}
	if managed && !matchingNamespace {
		return nil, nil, fmt.Errorf("existing Verver history uses another main pattern; restore it or migrate history explicitly")
	}
	return stable, consumed, nil
}

func Intent(commits []repository.Commit) version.Bump {
	bump := version.Patch
	for _, c := range commits {
		if strings.Contains(c.Message, "[version:major]") {
			return version.Major
		}
		if strings.Contains(c.Message, "[version:minor]") {
			bump = version.Minor
		}
	}
	return bump
}

// Ancestor includes equality. Missing history is an error, never a false result.
func Ancestor(ctx context.Context, h History, ancestor, head plumbing.Hash) (bool, error) {
	seen := map[plumbing.Hash]bool{}
	stack := []plumbing.Hash{head}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		hash := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[hash] {
			continue
		}
		seen[hash] = true
		commit, err := h.Commit(hash)
		if err != nil {
			return false, err
		}
		if hash == ancestor {
			return true, nil
		}
		stack = append(stack, commit.Parents...)
	}
	return false, nil
}

func Calculate(ctx context.Context, h History, state State, req Request) (Result, error) {
	c, err := req.Config.Normalize()
	if err != nil {
		return Result{}, err
	}
	if err := ValidateRef(req.Ref); err != nil {
		return Result{}, err
	}
	if _, err := ParseHash(req.Commit.String()); err != nil {
		return Result{}, err
	}
	p, _ := version.NewPattern(c.MainPattern, c.FeaturePattern)
	stable, consumed, err := Inspect(state.Tags, c, req.Adopt)
	if err != nil {
		return Result{}, err
	}
	// Identity wins over a moved/deleted branch or a newer baseline.
	if result, ok, err := existing(state.Tags, c, req.Ref, req.Commit); err != nil || ok {
		return result, err
	}
	if state.Main.IsZero() || state.Branch.IsZero() {
		return Result{}, fmt.Errorf("main or source branch is missing")
	}
	ok, err := Ancestor(ctx, h, req.Commit, state.Branch)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("tested commit is no longer reachable from the source branch")
	}
	base := version.Core{}
	if stable != nil {
		base = stable.Version.Core
		ok, err = Ancestor(ctx, h, stable.Commit, state.Main)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, fmt.Errorf("latest stable tag is not on main; history adoption or repair is required")
		}
	}
	var excluded []plumbing.Hash
	main := req.Ref == "refs/heads/"+c.MainBranch
	if main {
		if stable != nil {
			ok, err := Ancestor(ctx, h, stable.Commit, req.Commit)
			if err != nil {
				return Result{}, err
			}
			if !ok || stable.Commit == req.Commit {
				return Result{}, fmt.Errorf("stale main run: latest stable release already covers or overtakes this commit")
			}
			excluded = append(excluded, stable.Commit)
		}
	} else {
		excluded = append(excluded, state.Main)
	}
	commits, err := h.Range(ctx, req.Commit, excluded, consumed)
	if err != nil {
		return Result{}, err
	}
	bump := Intent(commits)
	consumedIDs := map[string]bool{}
	if main {
		for _, source := range req.Sources {
			if source.Pull < 0 {
				return Result{}, fmt.Errorf("source PR number must not be negative")
			}
			hash, err := ParseHash(source.Head)
			if err != nil {
				return Result{}, err
			}
			// Non-rewritten merges already preserve intent in the landed history.
			sourceRange, err := h.Range(ctx, hash, append(excluded, req.Commit), consumed)
			if err != nil {
				return Result{}, err
			}
			for _, commit := range sourceRange {
				consumedIDs[commit.Hash.String()] = true
			}
			if Intent(sourceRange) > bump {
				return Result{}, fmt.Errorf("merged source %s lost its version marker; preserve the marker in landed history before releasing", source.Head)
			}
		}
	} else if len(req.Sources) != 0 {
		return Result{}, fmt.Errorf("merged-source provenance is only valid for main releases")
	}
	core, err := base.Next(bump)
	if err != nil {
		return Result{}, err
	}
	candidate := version.Version{Core: core}
	kind := "stable"
	if !main {
		kind = "rc"
		for _, tag := range state.Tags {
			v, err := p.Parse(tag.Name)
			if err == nil && v.Core == core && v.RC > candidate.RC {
				candidate.RC = v.RC
			}
		}
		candidate, err = candidate.NextRC()
		if err != nil {
			return Result{}, err
		}
	}
	tag, err := p.Format(candidate)
	if err != nil {
		return Result{}, err
	}
	a := Assignment{Config: c, Ref: req.Ref, Commit: req.Commit.String(), Tag: tag, Sources: req.Sources}
	for id := range consumedIDs {
		a.Consumed = append(a.Consumed, id)
	}
	sort.Strings(a.Consumed)
	return Result{Tag: tag, Commit: a.Commit, Kind: kind, Status: "planned", Assignment: a}, nil
}
