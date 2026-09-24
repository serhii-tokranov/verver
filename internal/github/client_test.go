package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"verver/internal/repository"
)

func TestSources(t *testing.T) {
	landing := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	pages := 0
	details := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing auth")
		}
		switch {
		case strings.Contains(r.URL.Path, "/commits/"):
			pages++
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("Link", `<https://ignored.example/evil>; rel="next"`)
				fmt.Fprint(w, `[{"number":1}]`)
			} else {
				fmt.Fprint(w, `[{"number":1},{"number":2},{"number":3}]`)
			}
		case strings.HasSuffix(r.URL.Path, "/pulls/1"):
			details++
			fmt.Fprintf(w, `{"number":1,"merged":true,"merge_commit_sha":%q,"head":{"sha":%q},"base":{"ref":"main","repo":{"full_name":"owner/repo"}}}`, landing, head)
		case strings.HasSuffix(r.URL.Path, "/pulls/2"):
			details++
			fmt.Fprintf(w, `{"number":2,"merged":false,"head":{"sha":%q},"base":{"ref":"main","repo":{"full_name":"owner/repo"}}}`, head)
		case strings.HasSuffix(r.URL.Path, "/pulls/3"):
			details++
			fmt.Fprintf(w, `{"number":3,"merged":true,"merge_commit_sha":%q,"head":{"sha":%q},"base":{"ref":"other","repo":{"full_name":"owner/repo"}}}`, landing, head)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	})
	client, err := New("https://api.example.test", "owner/repo", "secret")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = handlerTransport{handler}
	sources, err := client.Sources(context.Background(), []repository.Commit{{Hash: plumbing.NewHash(landing)}}, "main")
	if err != nil || len(sources) != 1 || sources[0].Head != head || sources[0].Pull != 1 || pages != 2 || details != 3 {
		t.Fatal(sources, err, pages, details)
	}
}

func TestAPIErrorAndCancellation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, "secret server diagnostics")
	})
	client, _ := New("https://api.example.test", "owner/repo", "secret")
	client.http.Transport = handlerTransport{handler}
	if _, err := client.Pull(context.Background(), 1); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Pull(ctx, 1); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestInputValidation(t *testing.T) {
	for _, repo := range []string{"owner", "owner/repo/extra", "../repo", "owner/repo?token=x", "owner/"} {
		if _, err := New("", repo, ""); err == nil {
			t.Fatal(repo)
		}
	}
	for _, base := range []string{"http://api.example", "https://secret@api.example", "https://api.example?token=x"} {
		if _, err := New(base, "owner/repo", ""); err == nil {
			t.Fatal(base)
		}
	}
}

// Exercise HTTP request/response handling without binding a port or networking.
type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, r)
	return recorder.Result(), nil
}
