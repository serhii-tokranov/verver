package repository

import (
	"context"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"

	"github.com/serhii-tokranov/verver/internal/version"
)

func TestRemoteLifecycle(t *testing.T) {
	// In-process Git protocol server: real pack transfer and ref updates, no
	// shell, subprocess, network service, GitHub account, or credentials.
	upstream := newFixture(t, true)
	root := upstream.commit(t, "root")
	head := upstream.commit(t, "feature", root)
	upstream.ref(t, "refs/heads/main", root)
	upstream.ref(t, "refs/heads/feature", head)
	upstream.ref(t, "refs/pull/7/head", head)
	url := "verver-test://server/repository"
	old := client.Protocols["verver-test"]
	client.InstallProtocol("verver-test", server.NewClient(server.MapLoader{url: upstream.repo.Storer}))
	t.Cleanup(func() { client.InstallProtocol("verver-test", old) })
	local := newFixture(t, true)
	reader := openFixture(t, local)
	remote := &Remote{Reader: reader, remote: git.NewRemote(reader.repo.Storer, &config.RemoteConfig{Name: "verver", URLs: []string{url}})}
	p, _ := version.NewPattern("", "")
	ctx := context.Background()
	snapshot, err := remote.Refresh(ctx, p)
	if err != nil || snapshot.Heads["refs/heads/feature"] != head || len(snapshot.Tags) != 0 {
		t.Fatal(snapshot, err)
	}
	if _, err := reader.Commit(root); err != nil {
		t.Fatal(err)
	}
	if err := remote.CreateTag(ctx, "v0.0.1-rc.1", head, "assignment\n"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = remote.Refresh(ctx, p)
	if err != nil || len(snapshot.Tags) != 1 || snapshot.Tags[0].Commit != head || snapshot.Tags[0].Annotation != "assignment\n" {
		t.Fatal(snapshot, err)
	}
	tag, err := upstream.repo.Reference("refs/tags/v0.0.1-rc.1", true)
	if err != nil {
		t.Fatal(err)
	}
	original := tag.Hash()
	if err := remote.CreateTag(ctx, "v0.0.1-rc.1", root, "overwrite"); err == nil {
		t.Fatal("overwrote tag")
	}
	tag, err = upstream.repo.Reference("refs/tags/v0.0.1-rc.1", true)
	if err != nil || tag.Hash() != original {
		t.Fatal(tag, err)
	}
	if _, err := local.repo.Reference("refs/tags/v0.0.1-rc.1", true); err != plumbing.ErrReferenceNotFound {
		t.Fatalf("local tag was created: %v", err)
	}
	// Stale local tags and private fetched refs never enter the remote inventory.
	local.ref(t, "refs/tags/v99.0.0", head)
	if err := upstream.repo.Storer.RemoveReference("refs/tags/v0.0.1-rc.1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = remote.Refresh(ctx, p)
	if err != nil || len(snapshot.Tags) != 0 {
		t.Fatal(snapshot, err)
	}
	// A PR head not reachable from advertised branch heads can still be loaded.
	source := upstream.commit(t, "squashed source", root)
	upstream.ref(t, "refs/pull/8/head", source)
	if err := remote.EnsureSource(ctx, source, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Commit(source); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteTransportRestrictions(t *testing.T) {
	r := openFixture(t, newFixture(t, false))
	for _, url := range []string{"ssh://git@github.com/a/b", "git@github.com:a/b.git", "file:///tmp/repo", "/tmp/repo", "http://example.com/repo", "https://user:secret@example.com/repo", "https://example.com/repo?token=secret"} {
		if _, err := r.Connect(url, "secret"); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
	if _, err := r.Connect("https://github.com/example/repository.git", ""); err != nil {
		t.Fatal(err)
	}
}
