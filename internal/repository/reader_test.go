package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/serhii-tokranov/verver/internal/version"
)

// Fixtures use real on-disk Git objects, without subprocesses or network access.
type fixture struct {
	path string
	repo *git.Repository
	tree plumbing.Hash
}

func newFixture(t *testing.T, bare bool) fixture {
	t.Helper()
	path := t.TempDir()
	repo, err := git.PlainInit(path, bare)
	if err != nil {
		t.Fatal(err)
	}
	obj := repo.Storer.NewEncodedObject()
	if err := (&object.Tree{}).Encode(obj); err != nil {
		t.Fatal(err)
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{path, repo, hash}
}

func (f fixture) commit(t *testing.T, message string, parents ...plumbing.Hash) plumbing.Hash {
	t.Helper()
	signature := object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0).UTC()}
	c := &object.Commit{Author: signature, Committer: signature, Message: message, TreeHash: f.tree, ParentHashes: parents}
	obj := f.repo.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		t.Fatal(err)
	}
	hash, err := f.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func (f fixture) ref(t *testing.T, name string, hash plumbing.Hash) {
	t.Helper()
	if err := f.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(name), hash)); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) annotated(t *testing.T, target plumbing.Hash, kind plumbing.ObjectType, message string) plumbing.Hash {
	t.Helper()
	tag := &object.Tag{Name: "annotation", Target: target, TargetType: kind, Message: message, Tagger: object.Signature{Name: "Test", Email: "test@example.com", When: time.Unix(1, 0).UTC()}}
	obj := f.repo.Storer.NewEncodedObject()
	if err := tag.Encode(obj); err != nil {
		t.Fatal(err)
	}
	hash, err := f.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func openFixture(t *testing.T, f fixture) *Reader {
	t.Helper()
	r, err := Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTags(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(map[bool]string{false: "worktree", true: "bare"}[bare], func(t *testing.T) {
			f := newFixture(t, bare)
			head := f.commit(t, "initial")
			inner := f.annotated(t, head, plumbing.CommitObject, "inner\n")
			outer := f.annotated(t, inner, plumbing.TagObject, "outer\n")
			f.ref(t, "refs/tags/v1.2.3-rc.10", head)
			f.ref(t, "refs/tags/v1.2.3", outer)
			f.ref(t, "refs/tags/v1.2.3-rc.2", inner)
			f.ref(t, "refs/tags/v1.10.0", head)
			f.ref(t, "refs/tags/unrelated", plumbing.NewHash(strings.Repeat("a", 40)))
			f.ref(t, "refs/tags/v01.2.3", head)
			r := openFixture(t, f)
			p, _ := version.NewPattern("", "")
			tags, err := r.Tags(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tag := range tags {
				names = append(names, tag.Name)
				if tag.Commit != head {
					t.Fatalf("tag target: %+v", tag)
				}
			}
			want := []string{"v1.2.3-rc.2", "v1.2.3-rc.10", "v1.2.3", "v1.10.0"}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("tags: %v", names)
			}
			if tags[0].Annotation != "inner\n" || tags[1].Annotation != "" || tags[2].Annotation != "outer\n" {
				t.Fatalf("annotations: %+v", tags)
			}
			hash, err := r.Resolve("refs/tags/v1.2.3")
			if err != nil || hash != head {
				t.Fatal(hash, err)
			}
			custom, _ := version.NewPattern("api/vMAJOR.MINOR.PATCH", "-beta.RC")
			tags, err = r.Tags(context.Background(), custom)
			if err != nil || len(tags) != 0 {
				t.Fatal(tags, err)
			}
			f.ref(t, "refs/tags/api/v2.0.0-beta.1", head)
			tags, err = r.Tags(context.Background(), custom)
			if err != nil || len(tags) != 1 || tags[0].Version.RC != 1 {
				t.Fatal(tags, err)
			}
		})
	}
}

func TestCommitRange(t *testing.T) {
	f := newFixture(t, false)
	root := f.commit(t, "root")
	a := f.commit(t, "feature a\n\n[version:minor]\n", root)
	b := f.commit(t, "feature b", root)
	merge := f.commit(t, "merge", a, b)
	tip := f.commit(t, "tip", merge)
	r := openFixture(t, f)
	for _, tt := range []struct {
		exclude []plumbing.Hash
		want    []plumbing.Hash
	}{
		{nil, []plumbing.Hash{tip, merge, a, root, b}},
		{[]plumbing.Hash{root}, []plumbing.Hash{tip, merge, a, b}},
		{[]plumbing.Hash{a}, []plumbing.Hash{tip, merge, b}},
		{[]plumbing.Hash{a, b}, []plumbing.Hash{tip, merge}},
		{[]plumbing.Hash{tip}, nil},
	} {
		commits, err := r.Commits(context.Background(), tip, tt.exclude...)
		if err != nil {
			t.Fatal(err)
		}
		var got []plumbing.Hash
		for _, c := range commits {
			got = append(got, c.Hash)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("got %v, want %v", got, tt.want)
		}
	}
	c, err := r.Commit(a)
	if err != nil || c.Message != "feature a\n\n[version:minor]\n" || !reflect.DeepEqual(c.Parents, []plumbing.Hash{root}) {
		t.Fatal(c, err)
	}
}

func TestResolve(t *testing.T) {
	f := newFixture(t, false)
	r := openFixture(t, f)
	if _, err := r.Resolve("HEAD"); err == nil {
		t.Fatal("unborn HEAD accepted")
	}
	head := f.commit(t, "main")
	f.ref(t, "refs/heads/main", head)
	if err := f.repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"HEAD", "refs/heads/main"} {
		got, err := r.Resolve(name)
		if err != nil || got != head {
			t.Fatal(got, err)
		}
	}
	f.ref(t, "HEAD", head)
	got, err := r.Resolve("HEAD")
	if err != nil || got != head {
		t.Fatal("detached HEAD", got, err)
	}
	for _, name := range []string{"main", "refs/heads/missing", "refs/heads/../main", ""} {
		if _, err := r.Resolve(name); err == nil {
			t.Fatalf("resolved %q", name)
		}
	}
}

func TestIncompleteHistory(t *testing.T) {
	f := newFixture(t, false)
	missing := plumbing.NewHash(strings.Repeat("b", 40))
	broken := f.commit(t, "missing parent", missing)
	r := openFixture(t, f)
	for _, tt := range []struct {
		head    plumbing.Hash
		exclude []plumbing.Hash
	}{{missing, nil}, {broken, nil}, {broken, []plumbing.Hash{missing}}} {
		if _, err := r.Commits(context.Background(), tt.head, tt.exclude...); err == nil {
			t.Fatal("missing object accepted")
		}
	}
	for _, target := range []plumbing.Hash{missing, f.tree} {
		f.ref(t, "refs/tags/v1.0.0", target)
		p, _ := version.NewPattern("", "")
		if _, err := r.Tags(context.Background(), p); err == nil {
			t.Fatal("invalid tag target accepted")
		}
	}
}

func TestUnsupportedRepositories(t *testing.T) {
	t.Run("shallow", func(t *testing.T) {
		f := newFixture(t, false)
		if err := f.repo.Storer.SetShallow([]plumbing.Hash{f.commit(t, "root")}); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(f.path); err == nil || !strings.Contains(err.Error(), "shallow") {
			t.Fatal(err)
		}
	})
	t.Run("gitfile", func(t *testing.T) {
		path := t.TempDir()
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: ../somewhere\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "layout") {
			t.Fatal(err)
		}
	})
	for _, config := range []string{"\n[extensions]\npartialClone = origin\n", "\n[remote \"origin\"]\npromisor = true\n", "\n[remote \"origin\"]\npartialCloneFilter = blob:none\n"} {
		t.Run("partial", func(t *testing.T) {
			f := newFixture(t, false)
			path := filepath.Join(f.path, ".git", "config")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(data, config...), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(f.path); err == nil || !strings.Contains(err.Error(), "partial") {
				t.Fatal(err)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		if _, err := Open(t.TempDir()); err == nil {
			t.Fatal("non-repository accepted")
		}
	})
}

func TestCancellation(t *testing.T) {
	f := newFixture(t, false)
	root := f.commit(t, "root")
	r := openFixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, _ := version.NewPattern("", "")
	if _, err := r.Tags(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := r.Commits(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Also exercise cancellation between visits rather than only at entry.
	ctx, cancel = context.WithCancel(context.Background())
	child := f.commit(t, "child", root)
	visits := 0
	err := r.walk(ctx, []plumbing.Hash{child}, map[plumbing.Hash]bool{}, func(Commit) { visits++; cancel() })
	if !errors.Is(err, context.Canceled) || visits != 1 {
		t.Fatal(visits, err)
	}
}

func TestUnsupportedFormats(t *testing.T) {
	for _, extension := range []string{"objectformat = sha256", "refstorage = reftable"} {
		f := newFixture(t, false)
		path := filepath.Join(f.path, ".git", "config")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte("\n[extensions]\n"+extension+"\n")...)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(f.path); err == nil {
			t.Fatalf("accepted %s", extension)
		}
	}
}

func TestReadOnlyPackedRefs(t *testing.T) {
	f := newFixture(t, false)
	root := f.commit(t, "root")
	f.ref(t, "refs/heads/main", root)
	f.ref(t, "refs/tags/v0.1.0", root)
	packer, ok := f.repo.Storer.(interface{ PackRefs() error })
	if !ok {
		t.Fatal("fixture cannot pack refs")
	}
	if err := packer.PackRefs(); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string][32]byte {
		result := make(map[string][32]byte)
		err := filepath.WalkDir(f.path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	r := openFixture(t, f)
	if hash, err := r.Resolve("refs/heads/main"); err != nil || hash != root {
		t.Fatal(hash, err)
	}
	p, _ := version.NewPattern("", "")
	if tags, err := r.Tags(context.Background(), p); err != nil || len(tags) != 1 {
		t.Fatal(tags, err)
	}
	if commits, err := r.Commits(context.Background(), root); err != nil || len(commits) != 1 {
		t.Fatal(commits, err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("reader changed repository files")
	}
}

func TestRangeWithConsumedIDs(t *testing.T) {
	f := newFixture(t, false)
	root := f.commit(t, "root")
	marked := f.commit(t, "old [version:major]", root)
	head := f.commit(t, "new work", marked)
	r := openFixture(t, f)
	absent := plumbing.NewHash(strings.Repeat("c", 40))
	commits, err := r.Range(context.Background(), head, []plumbing.Hash{root}, []plumbing.Hash{marked, absent})
	if err != nil || len(commits) != 1 || commits[0].Hash != head {
		t.Fatal(commits, err)
	}
}
