package repository

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"verver/internal/version"
)

// Remote fetches into private refs/verver refs and pushes one immutable tag.
// It never changes local branches, the worktree, or local refs/tags.
type Remote struct {
	Reader *Reader
	remote *git.Remote
	auth   transport.AuthMethod
}

type Snapshot struct {
	Tags  []Tag
	Heads map[string]plumbing.Hash
}

func (r *Reader) RemoteURL(name string) (string, error) {
	cfg, err := r.repo.Config()
	if err != nil {
		return "", err
	}
	remote, ok := cfg.Remotes[name]
	if !ok || len(remote.URLs) != 1 {
		return "", fmt.Errorf("remote %q must have exactly one URL", name)
	}
	return remote.URLs[0], nil
}

// Connect only permits HTTPS; go-git's subprocess-backed file/SSH transports
// cannot be selected through production inputs. Credentials never enter URLs.
func (r *Reader) Connect(rawURL, token string) (*Remote, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("remote must be an HTTPS Git URL without embedded credentials, query, or fragment")
	}
	remote := git.NewRemote(r.repo.Storer, &config.RemoteConfig{Name: "verver", URLs: []string{rawURL}})
	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: token}
	}
	return &Remote{Reader: r, remote: remote, auth: auth}, nil
}

func (r *Remote) Refresh(ctx context.Context, p version.Pattern) (Snapshot, error) {
	// The caller must hold a repository-wide writer lock throughout refresh and
	// push. This inventory, not stale local refs, defines the candidate history.
	refs, err := r.remote.ListContext(ctx, &git.ListOptions{Auth: r.auth})
	if err != nil {
		return Snapshot{}, fmt.Errorf("list remote refs: %w", err)
	}
	heads := make(map[string]plumbing.Hash)
	var tags []*plumbing.Reference
	var specs []config.RefSpec
	for _, ref := range refs {
		if ref.Type() != plumbing.HashReference {
			continue
		}
		name := ref.Name().String()
		if strings.HasPrefix(name, "refs/heads/") {
			heads[name] = ref.Hash()
		}
		if strings.HasPrefix(name, "refs/tags/") && !strings.HasSuffix(name, "^{}") {
			tags = append(tags, ref)
		}
	}
	if len(heads) > 0 {
		specs = append(specs, "+refs/heads/*:refs/verver/heads/*")
	}
	if len(tags) > 0 {
		specs = append(specs, "+refs/tags/*:refs/verver/tags/*")
	}
	if len(specs) > 0 {
		err = r.remote.FetchContext(ctx, &git.FetchOptions{RemoteName: "verver", Auth: r.auth, RefSpecs: specs, Tags: git.NoTags, Force: true})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return Snapshot{}, fmt.Errorf("fetch remote history: %w", err)
		}
	}
	inventory, err := r.Reader.Inventory(ctx, tags, p)
	return Snapshot{Tags: inventory, Heads: heads}, err
}

// EnsureSource retrieves a preserved PR head or an explicitly supplied SHA.
// Its identity is checked by the caller after fetching; a ref may have moved.
func (r *Remote) EnsureSource(ctx context.Context, hash plumbing.Hash, pull int) error {
	if _, err := r.Reader.Commit(hash); err == nil {
		return nil
	}
	src := hash.String()
	if pull > 0 {
		src = fmt.Sprintf("refs/pull/%d/head", pull)
	}
	dst := "refs/verver/sources/" + hash.String()
	err := r.remote.FetchContext(ctx, &git.FetchOptions{RemoteName: "verver", Auth: r.auth, Tags: git.NoTags, RefSpecs: []config.RefSpec{config.RefSpec("+" + src + ":" + dst)}, Force: true})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch source history: %w", err)
	}
	_, err = r.Reader.Commit(hash)
	return err
}

// CreateTag sends a single new annotated tag. No force, branch writes, hook
// execution, or local tag refs are involved. Existing tags are never replaced.
func (r *Remote) CreateTag(ctx context.Context, name string, commit plumbing.Hash, message string) error {
	ref := plumbing.NewTagReferenceName(name)
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("invalid tag name: %w", err)
	}
	if _, err := r.Reader.Commit(commit); err != nil {
		return err
	}
	refs, err := r.remote.ListContext(ctx, &git.ListOptions{Auth: r.auth})
	if err != nil {
		return err
	}
	for _, existing := range refs {
		if existing.Name() == ref {
			return fmt.Errorf("remote tag %s already exists", name)
		}
	}
	tag := &object.Tag{Name: name, Target: commit, TargetType: plumbing.CommitObject, Message: message, Tagger: object.Signature{Name: "Verver", Email: "verver@users.noreply.github.com", When: time.Now().UTC()}}
	obj := r.Reader.repo.Storer.NewEncodedObject()
	if err := tag.Encode(obj); err != nil {
		return err
	}
	hash, err := r.Reader.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return err
	}
	// go-git's push matcher needs a source ref even for an object ID. Keep
	// that implementation detail outside the user's branches and tag namespace.
	outbox := plumbing.ReferenceName("refs/verver/outbox/" + hash.String())
	if err := r.Reader.repo.Storer.SetReference(plumbing.NewHashReference(outbox, hash)); err != nil {
		return err
	}
	defer r.Reader.repo.Storer.RemoveReference(outbox)
	// The server also compares the advertised old OID when applying the push.
	return r.remote.PushContext(ctx, &git.PushOptions{RemoteName: "verver", Auth: r.auth, RefSpecs: []config.RefSpec{config.RefSpec(outbox.String() + ":" + ref.String())}})
}

// Inventory includes matching tags and Verver-managed tags with other labels,
// so a configuration change cannot quietly discard managed history.
func (r *Reader) Inventory(ctx context.Context, refs []*plumbing.Reference, p version.Pattern) ([]Tag, error) {
	if _, err := p.Format(version.Version{}); err != nil {
		return nil, err
	}
	var tags []Tag
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(ref.Name().String(), "refs/tags/")
		v, parseErr := p.Parse(name)
		if parseErr != nil {
			obj, err := r.repo.TagObject(ref.Hash())
			if err != nil || !strings.HasPrefix(obj.Message, "verver/") {
				continue
			}
		}
		hash, message, err := r.peel(ctx, ref.Hash())
		if err != nil {
			return nil, fmt.Errorf("tag %s: %w", name, err)
		}
		tags = append(tags, Tag{Name: name, Version: v, Commit: hash, Annotation: message})
	}
	return tags, nil
}

func (r *Reader) LocalInventory(ctx context.Context, p version.Pattern) ([]Tag, error) {
	iter, err := r.repo.Tags()
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var refs []*plumbing.Reference
	if err := iter.ForEach(func(ref *plumbing.Reference) error { refs = append(refs, ref); return nil }); err != nil {
		return nil, err
	}
	return r.Inventory(ctx, refs, p)
}
