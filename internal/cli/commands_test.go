package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"

	"verver/internal/release"
)

func cliRepo(t *testing.T) (string, *git.Repository, plumbing.Hash) {
	t.Helper()
	path := t.TempDir()
	repo, err := git.PlainInit(path, true)
	if err != nil {
		t.Fatal(err)
	}
	tree := repo.Storer.NewEncodedObject()
	if err := (&object.Tree{}).Encode(tree); err != nil {
		t.Fatal(err)
	}
	treeHash, err := repo.Storer.SetEncodedObject(tree)
	if err != nil {
		t.Fatal(err)
	}
	sig := object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0)}
	obj := repo.Storer.NewEncodedObject()
	if err := (&object.Commit{Author: sig, Committer: sig, Message: "first [version:minor]", TreeHash: treeHash}).Encode(obj); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []plumbing.ReferenceName{"refs/heads/main", plumbing.HEAD} {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(name, hash)); err != nil {
			t.Fatal(err)
		}
	}
	return path, repo, hash
}

func TestCLIReleaseAndRetry(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("VERVER_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	_, upstream, hash := cliRepo(t)
	path, local, _ := cliRepo(t)
	url := "https://git.example.test/owner/repo"
	old := client.Protocols["https"]
	client.InstallProtocol("https", server.NewClient(server.MapLoader{url: upstream.Storer}))
	t.Cleanup(func() { client.InstallProtocol("https", old) })
	if _, err := local.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "outputs")
	if err := os.WriteFile(output, nil, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"release", "--path", path, "--ref", "refs/heads/main", "--sha", hash.String(), "--serialized", "--format", "json", "--github-output", output}
	for _, want := range []string{"created", "reused"} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr, "test"); code != 0 {
			t.Fatal(code, stderr.String())
		}
		var result release.Result
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Tag != "v0.1.0" || result.Status != want {
			t.Fatal(result)
		}
	}
	tags, err := upstream.Tags()
	if err != nil {
		t.Fatal(err)
	}
	defer tags.Close()
	count := 0
	if err := tags.ForEach(func(*plumbing.Reference) error { count++; return nil }); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("created %d tags", count)
	}
	data, err := os.ReadFile(output)
	if err != nil || !strings.Contains(string(data), "status=reused") {
		t.Fatal(string(data), err)
	}
}

func TestCLIValidationAndOfflinePreview(t *testing.T) {
	path, _, _ := cliRepo(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"next", "--path", path, "--ref", "refs/heads/main"}, &stdout, &stderr, "test"); code != 0 || stdout.String() != "v0.1.0\n" {
		t.Fatal(code, stdout.String(), stderr.String())
	}
	for _, args := range [][]string{
		{"release", "--ref", "refs/heads/main"},
		{"next", "--ref", "main"},
		{"next", "--ref", "refs/heads/main", "--sha", "abc"},
		{"next", "--ref", "refs/heads/main", "--format", "bad"},
		{"check-pr"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := Run(args, &stdout, &stderr, "test"); code != 2 || stdout.Len() != 0 {
			t.Fatal(args, code, stdout.String(), stderr.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout.Reset()
	stderr.Reset()
	if code := RunContext(ctx, []string{"next", "--path", path, "--ref", "refs/heads/main"}, &stdout, &stderr, "test"); code != 1 {
		t.Fatal(code, stderr.String())
	}
}

func TestGitHubWriteEventGate(t *testing.T) {
	_, _, hash := cliRepo(t)
	o := options{ref: "refs/heads/main", github: "owner/repo"}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_EVENT_NAME", "pull_request")
	if err := validatePushEvent(o, hash); err == nil {
		t.Fatal("PR write accepted")
	}
	t.Setenv("GITHUB_EVENT_NAME", "push")
	t.Setenv("GITHUB_REF", o.ref)
	t.Setenv("GITHUB_SHA", hash.String())
	t.Setenv("GITHUB_REPOSITORY", o.github)
	path := filepath.Join(t.TempDir(), "event.json")
	t.Setenv("GITHUB_EVENT_PATH", path)
	event := map[string]any{"ref": o.ref, "after": hash.String(), "deleted": false, "repository": map[string]string{"full_name": o.github}}
	data, _ := json.Marshal(event)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePushEvent(o, hash); err != nil {
		t.Fatal(err)
	}
	event["deleted"] = true
	data, _ = json.Marshal(event)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePushEvent(o, hash); err == nil {
		t.Fatal("deleted branch accepted")
	}
}
