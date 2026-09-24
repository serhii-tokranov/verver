package release

import (
	"context"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"

	"verver/internal/repository"
	"verver/internal/version"
)

// Backend must operate on the same repository as History. Callers serialize
// this entire operation across all writers, including other workflows.
type Backend interface {
	Refresh(context.Context, version.Pattern) (repository.Snapshot, error)
	EnsureSource(context.Context, plumbing.Hash, int) error
	CreateTag(context.Context, string, plumbing.Hash, string) error
}

// Provenance returns verified original source tips introduced by landed commits.
// A nil provider is appropriate only for integrations preserving Git ancestry.
type Provenance func(context.Context, []repository.Commit) ([]Source, error)

func existing(tags []repository.Tag, c Config, ref string, hash plumbing.Hash) (Result, bool, error) {
	for _, tag := range tags {
		a, err := Decode(tag)
		if err != nil {
			return Result{}, false, err
		}
		if a != nil && a.Config == c && a.Ref == ref && a.Commit == hash.String() {
			kind := "rc"
			if ref == "refs/heads/"+c.MainBranch {
				kind = "stable"
			}
			return Result{Tag: tag.Name, Commit: a.Commit, Kind: kind, Status: "reused", Assignment: *a}, true, nil
		}
	}
	return Result{}, false, nil
}

// Finalize reconciles unknown write outcomes before trying another assignment.
// It never reports creation until the remote has accepted the tag.
func Finalize(ctx context.Context, h History, b Backend, req Request, provenance Provenance) (Result, error) {
	c, err := req.Config.Normalize()
	if err != nil {
		return Result{}, err
	}
	req.Config = c
	p, _ := version.NewPattern(c.MainPattern, c.FeaturePattern)
	var writeErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		snapshot, err := b.Refresh(ctx, p)
		if err != nil {
			return Result{}, err
		}
		state := State{Tags: snapshot.Tags, Main: snapshot.Heads["refs/heads/"+c.MainBranch], Branch: snapshot.Heads[req.Ref]}
		stable, consumed, err := Inspect(state.Tags, c, req.Adopt)
		if err != nil {
			return Result{}, err
		}
		if result, ok, err := existing(state.Tags, c, req.Ref, req.Commit); err != nil || ok {
			return result, err
		}
		if attempt == 3 {
			break
		} // Last refresh reconciles a possibly accepted write.
		current := req
		if req.Ref == "refs/heads/"+c.MainBranch && provenance != nil {
			var excluded []plumbing.Hash
			if stable != nil {
				excluded = append(excluded, stable.Commit)
			}
			commits, err := h.Range(ctx, req.Commit, excluded, consumed)
			if err != nil {
				return Result{}, err
			}
			sources, err := provenance(ctx, commits)
			if err != nil {
				return Result{}, err
			}
			current.Sources = append(append([]Source(nil), req.Sources...), sources...)
		}
		if err := ensure(ctx, b, current.Sources); err != nil {
			return Result{}, err
		}
		result, err := Calculate(ctx, h, state, current)
		if err != nil {
			return Result{}, err
		}
		annotation, err := result.Assignment.Annotation()
		if err != nil {
			return Result{}, err
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		writeErr = b.CreateTag(ctx, result.Tag, req.Commit, annotation)
		if writeErr == nil {
			result.Status = "created"
			return result, nil
		}
	}
	return Result{}, fmt.Errorf("tag was not confirmed after three attempts; rerun to reconcile: %w", writeErr)
}

func ensure(ctx context.Context, b Backend, sources []Source) error {
	seen := map[string]bool{}
	for _, source := range sources {
		if seen[source.Head] {
			continue
		}
		seen[source.Head] = true
		hash, err := ParseHash(source.Head)
		if err != nil {
			return err
		}
		if err := b.EnsureSource(ctx, hash, source.Pull); err != nil {
			return fmt.Errorf("load merged source %s: %w", source.Head, err)
		}
	}
	return nil
}
