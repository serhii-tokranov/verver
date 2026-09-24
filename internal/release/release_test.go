package release

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/serhii-tokranov/verver/internal/repository"
	"github.com/serhii-tokranov/verver/internal/version"
)

type graph map[plumbing.Hash]repository.Commit

func (g graph) add(message string, parents ...plumbing.Hash) plumbing.Hash {
	hash := plumbing.Hash(sha1.Sum([]byte(fmt.Sprint(message, parents))))
	g[hash] = repository.Commit{Hash: hash, Message: message, Parents: parents}
	return hash
}
func (g graph) Commit(h plumbing.Hash) (repository.Commit, error) {
	c, ok := g[h]
	if !ok {
		return c, fmt.Errorf("missing %s", h)
	}
	return c, nil
}
func (g graph) Commits(ctx context.Context, h plumbing.Hash, exclude ...plumbing.Hash) ([]repository.Commit, error) {
	return g.Range(ctx, h, exclude, nil)
}
func (g graph) Range(ctx context.Context, h plumbing.Hash, exclude, consumed []plumbing.Hash) ([]repository.Commit, error) {
	seen := map[plumbing.Hash]bool{}
	var out []repository.Commit
	var walk func(plumbing.Hash, bool) error
	walk = func(hash plumbing.Hash, emit bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[hash] {
			return nil
		}
		c, err := g.Commit(hash)
		if err != nil {
			return err
		}
		seen[hash] = true
		if emit {
			out = append(out, c)
		}
		for _, p := range c.Parents {
			if err := walk(p, emit); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range exclude {
		if err := walk(e, false); err != nil {
			return nil, err
		}
	}
	for _, id := range consumed {
		seen[id] = true
	}
	err := walk(h, true)
	return out, err
}
func testConfig() Config { c, _ := (Config{}).Normalize(); return c }
func save(t *testing.T, state *State, result Result) {
	t.Helper()
	annotation, err := result.Assignment.Annotation()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := version.NewPattern(result.Assignment.Config.MainPattern, result.Assignment.Config.FeaturePattern)
	v, err := p.Parse(result.Tag)
	if err != nil {
		t.Fatal(err)
	}
	state.Tags = append(state.Tags, repository.Tag{Name: result.Tag, Version: v, Commit: plumbing.NewHash(result.Commit), Annotation: annotation})
}
func assign(t *testing.T, g graph, state *State, ref string, head plumbing.Hash, want string, sources ...Source) Result {
	t.Helper()
	state.Branch = head
	r, err := Calculate(context.Background(), g, *state, Request{Config: testConfig(), Ref: "refs/heads/" + ref, Commit: head, Sources: sources})
	if err != nil || r.Tag != want {
		t.Fatalf("%s: got %+v, %v; want %s", ref, r, err, want)
	}
	save(t, state, r)
	return r
}

func TestReleaseSequence(t *testing.T) {
	g := graph{}
	root := g.add("initial")
	s := State{Main: root}
	assign(t, g, &s, "main", root, "v0.0.1")
	a := g.add("feature A", root)
	b := g.add("feature B", root)
	assign(t, g, &s, "a", a, "v0.0.2-rc.1")
	assign(t, g, &s, "b", b, "v0.0.2-rc.2")
	a2 := g.add("request [version:minor]", a)
	assign(t, g, &s, "a", a2, "v0.1.0-rc.1")
	s.Main = g.add("merge B", root, b)
	assign(t, g, &s, "main", s.Main, "v0.0.2")
	a3 := g.add("more work", a2)
	assign(t, g, &s, "a", a3, "v0.1.0-rc.2")
	// B independently releases the same minor target; A keeps pending intent.
	s.Main = g.add("another [version:minor]", s.Main)
	assign(t, g, &s, "main", s.Main, "v0.1.0")
	a4 := g.add("still pending", a3)
	assign(t, g, &s, "a", a4, "v0.2.0-rc.1")
	// Retrying A's old push keeps the earlier target, even after branch deletion.
	retry, err := Calculate(context.Background(), g, State{Tags: s.Tags}, Request{Config: testConfig(), Ref: "refs/heads/a", Commit: a2})
	if err != nil || retry.Tag != "v0.1.0-rc.1" || retry.Status != "reused" {
		t.Fatal(retry, err)
	}
	// Squash preserves intent and records original history as consumed.
	s.Main = g.add("squash A [version:minor]", s.Main)
	assign(t, g, &s, "main", s.Main, "v0.2.0", Source{Head: a4.String(), Pull: 1})
	a5 := g.add("reuse branch without repeating old intent", a4)
	assign(t, g, &s, "a", a5, "v0.2.1-rc.1")
	major := g.add("body\n[version:major]\n[version:minor]", a5)
	assign(t, g, &s, "a", major, "v1.0.0-rc.1")
}

func TestPolicies(t *testing.T) {
	g := graph{}
	root := g.add("root")
	s := State{Main: root}
	assign(t, g, &s, "main", root, "v0.0.1")
	marked := g.add("[version:minor]", root)
	badSquash := g.add("squash missing marker", root)
	req := Request{Config: testConfig(), Ref: "refs/heads/main", Commit: badSquash, Sources: []Source{{Head: marked.String()}}}
	s.Main = badSquash
	s.Branch = badSquash
	if _, err := Calculate(context.Background(), g, s, req); err == nil || !strings.Contains(err.Error(), "lost") {
		t.Fatal(err)
	}
	// A main release overtaking an older unassigned push makes that push stale.
	later := g.add("later", badSquash)
	s.Main = later
	assign(t, g, &s, "main", later, "v0.0.2")
	req.Sources = nil
	s.Branch = later
	if _, err := Calculate(context.Background(), g, s, req); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatal(err)
	}
	req.Ref = "refs/heads/feature"
	req.Commit = marked
	if _, err := Calculate(context.Background(), g, s, req); err == nil || !strings.Contains(err.Error(), "reachable") {
		t.Fatal(err)
	}
	req.Ref = "refs/tags/nope"
	if _, err := Calculate(context.Background(), g, s, req); err == nil {
		t.Fatal("accepted tag event")
	}
}

func TestAdoptionAndMetadata(t *testing.T) {
	g := graph{}
	root := g.add("root")
	head := g.add("next", root)
	s := State{Main: head, Branch: head, Tags: []repository.Tag{{Name: "v2.3.4", Commit: root}}}
	req := Request{Config: testConfig(), Ref: "refs/heads/main", Commit: head}
	if _, err := Calculate(context.Background(), g, s, req); err == nil {
		t.Fatal("silent adoption")
	}
	req.Adopt = true
	r, err := Calculate(context.Background(), g, s, req)
	if err != nil || r.Tag != "v2.3.5" {
		t.Fatal(r, err)
	}
	save(t, &s, r)
	changed := testConfig()
	changed.FeaturePattern = "-beta.RC"
	if _, _, err := Inspect(s.Tags, changed, true); err == nil {
		t.Fatal("silent pattern migration")
	}
	changed = testConfig()
	changed.MainPattern = "release-MAJOR.MINOR.PATCH"
	if _, _, err := Inspect(s.Tags, changed, true); err == nil {
		t.Fatal("silent prefix migration")
	}
	tag := s.Tags[1]
	tag.Annotation += "garbage"
	if _, err := Decode(tag); err == nil {
		t.Fatal("trailing metadata accepted")
	}
	tag = s.Tags[1]
	tag.Commit = root
	if _, err := Decode(tag); err == nil {
		t.Fatal("wrong target accepted")
	}
}

type fakeBackend struct {
	g         graph
	snapshot  repository.Snapshot
	pushes    int
	lost      bool
	fail      bool
	collision bool
}

func (b *fakeBackend) Refresh(context.Context, version.Pattern) (repository.Snapshot, error) {
	return b.snapshot, nil
}
func (b *fakeBackend) EnsureSource(_ context.Context, h plumbing.Hash, _ int) error {
	_, err := b.g.Commit(h)
	return err
}
func (b *fakeBackend) CreateTag(_ context.Context, name string, h plumbing.Hash, message string) error {
	b.pushes++
	if b.fail {
		return errors.New("permission denied")
	}
	p, _ := version.NewPattern("", "")
	v, _ := p.Parse(name)
	if b.collision && b.pushes == 1 {
		a := Assignment{Config: testConfig(), Ref: "refs/heads/other", Commit: h.String(), Tag: name}
		message, _ = a.Annotation()
		b.snapshot.Tags = append(b.snapshot.Tags, repository.Tag{Name: name, Commit: h, Version: v, Annotation: message})
		return errors.New("collision")
	}
	b.snapshot.Tags = append(b.snapshot.Tags, repository.Tag{Name: name, Commit: h, Version: v, Annotation: message})
	if b.lost {
		return errors.New("connection lost after acceptance")
	}
	return nil
}
func TestFinalize(t *testing.T) {
	for _, mode := range []string{"normal", "lost", "denied", "collision", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			g := graph{}
			root := g.add("root")
			head := g.add("feature", root)
			b := &fakeBackend{g: g, snapshot: repository.Snapshot{Heads: map[string]plumbing.Hash{"refs/heads/main": root, "refs/heads/a": head}}, lost: mode == "lost", fail: mode == "denied", collision: mode == "collision"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			req := Request{Config: testConfig(), Ref: "refs/heads/a", Commit: head}
			r, err := Finalize(ctx, g, b, req, nil)
			switch mode {
			case "cancel":
				if !errors.Is(err, context.Canceled) || b.pushes != 0 {
					t.Fatal(r, err, b.pushes)
				}
			case "denied":
				if err == nil || len(b.snapshot.Tags) != 0 || b.pushes != 3 {
					t.Fatal(r, err, b.pushes)
				}
			default:
				want := "v0.0.1-rc.1"
				if mode == "collision" {
					want = "v0.0.1-rc.2"
				}
				if err != nil || r.Tag != want {
					t.Fatal(r, err)
				}
				before := b.pushes
				r, err = Finalize(ctx, g, b, req, nil)
				if err != nil || r.Status != "reused" || b.pushes != before {
					t.Fatal(r, err, b.pushes)
				}
			}
		})
	}
}

func BenchmarkCalculate(b *testing.B) {
	for _, size := range []int{100, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			g := graph{}
			head := g.add("root")
			for i := 0; i < size; i++ {
				head = g.add(fmt.Sprintf("change %d", i), head)
			}
			branch := g.add("feature", head)
			state := State{Main: head, Branch: branch}
			req := Request{Config: testConfig(), Ref: "refs/heads/feature", Commit: branch}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Calculate(context.Background(), g, state, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestConsumedHistorySurvivesSourcePruning(t *testing.T) {
	g := graph{}
	root := g.add("root")
	state := State{Main: root}
	assign(t, g, &state, "main", root, "v0.0.1")
	marked := g.add("feature [version:minor]", root)
	source := g.add("more work", marked)
	state.Main = g.add("squash [version:minor]", root)
	result := assign(t, g, &state, "main", state.Main, "v0.1.0", Source{Head: source.String(), Pull: 7})
	if len(result.Assignment.Consumed) != 2 {
		t.Fatal(result.Assignment)
	}
	// No RC tag retained the original PR tip; the source object disappears.
	delete(g, source)
	fresh := g.add("fresh work", state.Main)
	assign(t, g, &state, "fresh", fresh, "v0.1.1-rc.1")
	// A branch forked from an earlier original commit must not repeat its marker.
	fork := g.add("work from old feature", marked)
	assign(t, g, &state, "fork", fork, "v0.1.1-rc.2")
}

func TestRebaseConsumesOriginalIDs(t *testing.T) {
	g := graph{}
	root := g.add("root")
	state := State{Main: root}
	assign(t, g, &state, "main", root, "v0.0.1")
	original := g.add("feature [version:minor]", root)
	intervening := g.add("main changed", root)
	state.Main = intervening
	assign(t, g, &state, "main", intervening, "v0.0.2")
	state.Main = g.add("feature [version:minor]", intervening)
	assign(t, g, &state, "main", state.Main, "v0.1.0", Source{Head: original.String(), Pull: 8})
	continued := g.add("continued after rebase merge", original)
	assign(t, g, &state, "feature", continued, "v0.1.1-rc.1")
}
