// Package repository reads local Git history without fetching or modifying it.
package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"verver/internal/version"
)

// Reader requires a full, ordinary SHA-1 repository or a bare repository.
// The caller must supply fresh refs and all tags before release calculation.
// Missing remote tags cannot be detected by inspecting a local repository.
type Reader struct{ repo *git.Repository }

type Commit struct {
	Hash    plumbing.Hash
	Parents []plumbing.Hash
	Message string
}

type Tag struct {
	Name    string
	Version version.Version
	Commit  plumbing.Hash
	// Annotation is the outer tag message, empty for lightweight tags.
	Annotation string
}

// Open uses the exact repository root; it does not search parent directories.
// Linked worktrees and partial/shallow clones are deliberately unsupported.
func Open(path string) (*Reader, error) {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err == nil && !info.IsDir() {
		return nil, fmt.Errorf("unsupported repository layout: .git must be a directory")
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect repository: %w", err)
	}
	r, err := git.PlainOpen(path)
	if err != nil {
		return nil, fmt.Errorf("open repository: %w", err)
	}
	shallow, err := r.Storer.Shallow()
	if err != nil {
		return nil, fmt.Errorf("read shallow boundary: %w", err)
	}
	if len(shallow) != 0 {
		return nil, fmt.Errorf("shallow repository: fetch complete history before versioning")
	}
	cfg, err := r.Config()
	if err != nil {
		return nil, fmt.Errorf("read repository config: %w", err)
	}
	if format := cfg.Raw.Section("extensions").Option("objectformat"); format != "" && format != "sha1" {
		return nil, fmt.Errorf("unsupported object format: %s", format)
	}
	if storage := cfg.Raw.Section("extensions").Option("refstorage"); storage != "" && storage != "files" {
		return nil, fmt.Errorf("unsupported ref storage: %s", storage)
	}
	if cfg.Raw.Section("extensions").Option("partialclone") != "" {
		return nil, fmt.Errorf("partial clones are unsupported")
	}
	for _, remote := range cfg.Raw.Section("remote").Subsections {
		if remote.Option("promisor") != "" || remote.Option("partialclonefilter") != "" {
			return nil, fmt.Errorf("partial clones are unsupported")
		}
	}
	return &Reader{repo: r}, nil
}

// Resolve accepts HEAD or an explicit full ref, avoiding branch/tag ambiguity.
// A detached HEAD is valid; an unborn HEAD is an error, not a zero baseline.
func (r *Reader) Resolve(name string) (plumbing.Hash, error) {
	refName := plumbing.ReferenceName(name)
	if name != "HEAD" && (!strings.HasPrefix(name, "refs/") || refName.Validate() != nil) {
		return plumbing.ZeroHash, fmt.Errorf("expected HEAD or a full ref name")
	}
	ref, err := r.repo.Reference(refName, true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve %s: %w", name, err)
	}
	hash, _, err := r.peel(context.Background(), ref.Hash())
	return hash, err
}

func (r *Reader) Commit(hash plumbing.Hash) (Commit, error) {
	c, err := r.repo.CommitObject(hash)
	if err != nil {
		return Commit{}, fmt.Errorf("read commit %s: %w", hash, err)
	}
	return Commit{Hash: c.Hash, Parents: append([]plumbing.Hash(nil), c.ParentHashes...), Message: c.Message}, nil
}

// Tags returns matching tags in ascending numeric order. Unrelated names are
// ignored, but broken objects behind matching tags are errors.
func (r *Reader) Tags(ctx context.Context, pattern version.Pattern) ([]Tag, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := pattern.Format(version.Version{}); err != nil {
		return nil, err
	}
	refs, err := r.repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer refs.Close()
	var tags []Tag
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimPrefix(ref.Name().String(), "refs/tags/")
		v, err := pattern.Parse(name)
		if err != nil {
			return nil
		}
		hash, annotation, err := r.peel(ctx, ref.Hash())
		if err != nil {
			return fmt.Errorf("tag %s: %w", name, err)
		}
		tags = append(tags, Tag{Name: name, Version: v, Commit: hash, Annotation: annotation})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Version.Compare(tags[j].Version) < 0 })
	return tags, nil
}

func (r *Reader) peel(ctx context.Context, hash plumbing.Hash) (plumbing.Hash, string, error) {
	annotation := ""
	for depth := 0; depth < 32; depth++ {
		if err := ctx.Err(); err != nil {
			return plumbing.ZeroHash, "", err
		}
		obj, err := r.repo.Object(plumbing.AnyObject, hash)
		if err != nil {
			return plumbing.ZeroHash, "", fmt.Errorf("read object %s: %w", hash, err)
		}
		switch obj := obj.(type) {
		case *object.Commit:
			return obj.Hash, annotation, nil
		case *object.Tag:
			if depth == 0 {
				annotation = obj.Message
			}
			hash = obj.Target
		default:
			return plumbing.ZeroHash, "", fmt.Errorf("object %s does not point to a commit", hash)
		}
	}
	return plumbing.ZeroHash, "", fmt.Errorf("tag nesting exceeds 32 objects")
}

// Commits returns commits reachable from head but not from any excluded tip.
// It follows every merge parent and visits shared ancestors once. Ordering is
// deterministic traversal order, not commit date or release order.
func (r *Reader) Commits(ctx context.Context, head plumbing.Hash, exclude ...plumbing.Hash) ([]Commit, error) {
	return r.Range(ctx, head, exclude, nil)
}

// Range excludes both reachable tip history and exact previously consumed IDs.
// Consumed IDs need not still have objects: rewritten source history may have
// been garbage-collected remotely after its consumption was recorded in a tag.
func (r *Reader) Range(ctx context.Context, head plumbing.Hash, tips, consumed []plumbing.Hash) ([]Commit, error) {
	excluded := make(map[plumbing.Hash]bool)
	if err := r.walk(ctx, tips, excluded, nil); err != nil {
		return nil, err
	}
	for _, hash := range consumed {
		excluded[hash] = true
	}
	var commits []Commit
	err := r.walk(ctx, []plumbing.Hash{head}, excluded, func(c Commit) { commits = append(commits, c) })
	if err != nil {
		return nil, err
	}
	return commits, nil
}

func (r *Reader) walk(ctx context.Context, roots []plumbing.Hash, seen map[plumbing.Hash]bool, visit func(Commit)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stack := append([]plumbing.Hash(nil), roots...)
	for len(stack) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		hash := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[hash] {
			continue
		}
		c, err := r.Commit(hash)
		if err != nil {
			return err
		}
		seen[hash] = true
		if visit != nil {
			visit(c)
		}
		for i := len(c.Parents) - 1; i >= 0; i-- {
			stack = append(stack, c.Parents[i])
		}
	}
	return nil
}
