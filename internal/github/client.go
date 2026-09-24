// Package github retrieves merge provenance. It does not create tags or run code.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"verver/internal/release"
	"verver/internal/repository"
)

type Client struct {
	base  string
	repo  string
	token string
	http  *http.Client
}

type Pull struct {
	Number      int    `json:"number"`
	Merged      bool   `json:"merged"`
	MergeCommit string `json:"merge_commit_sha"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Head        struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

func ValidRepository(repo string) bool {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

func New(base, repo, token string) (*Client, error) {
	if base == "" {
		base = "https://api.github.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("GitHub API URL must be HTTPS without credentials, query, or fragment")
	}
	if !ValidRepository(repo) {
		return nil, fmt.Errorf("GitHub repository must be owner/name")
	}
	return &Client{base: strings.TrimRight(base, "/"), repo: repo, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("GitHub API redirects are not followed; configure the canonical repository URL")
	}}}, nil
}

func (c *Client) get(ctx context.Context, path string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/repos/"+c.repo+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "verver")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("GitHub request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GitHub API %s returned HTTP %d", path, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
	if err != nil {
		return false, err
	}
	if len(data) > 16*1024*1024 {
		return false, fmt.Errorf("GitHub response exceeded 16 MiB")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("invalid GitHub response: %w", err)
	}
	return strings.Contains(response.Header.Get("Link"), `rel="next"`), nil
}

func (c *Client) Pull(ctx context.Context, number int) (Pull, error) {
	var p Pull
	if number <= 0 {
		return p, fmt.Errorf("PR number must be positive")
	}
	_, err := c.get(ctx, fmt.Sprintf("/pulls/%d", number), &p)
	if err != nil {
		return p, err
	}
	if p.Number != number || !strings.EqualFold(p.Base.Repo.FullName, c.repo) {
		return p, fmt.Errorf("PR response does not match the requested repository")
	}
	if _, err := release.ParseHash(p.Head.SHA); err != nil {
		return p, fmt.Errorf("PR head: %w", err)
	}
	return p, nil
}

// Sources inspects every landed commit and follows pagination. Only merged PRs
// whose landing commit is in the actual range and whose base is main qualify.
func (c *Client) Sources(ctx context.Context, commits []repository.Commit, main string) ([]release.Source, error) {
	landed := map[string]bool{}
	for _, commit := range commits {
		landed[commit.Hash.String()] = true
	}
	seen := map[int]bool{}
	var sources []release.Source
	for _, commit := range commits {
		for page := 1; ; page++ {
			if page > 1000 {
				return nil, fmt.Errorf("GitHub PR pagination exceeded 1000 pages")
			}
			var pulls []struct {
				Number int `json:"number"`
			}
			more, err := c.get(ctx, fmt.Sprintf("/commits/%s/pulls?per_page=100&page=%d", commit.Hash, page), &pulls)
			if err != nil {
				return nil, err
			}
			for _, summary := range pulls {
				if seen[summary.Number] {
					continue
				}
				seen[summary.Number] = true
				p, err := c.Pull(ctx, summary.Number)
				if err != nil {
					return nil, err
				}
				if !p.Merged || p.Base.Ref != main || !landed[p.MergeCommit] {
					continue
				}
				sources = append(sources, release.Source{Head: p.Head.SHA, Pull: p.Number})
			}
			if !more {
				break
			}
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Pull < sources[j].Pull })
	return sources, nil
}
