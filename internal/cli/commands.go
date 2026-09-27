package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	gh "github.com/serhii-tokranov/verver/internal/github"
	"github.com/serhii-tokranov/verver/internal/release"
	"github.com/serhii-tokranov/verver/internal/repository"
	"github.com/serhii-tokranov/verver/internal/version"
)

type options struct {
	config                                                                                              release.Config
	path, ref, sha, mainRef, remote, remoteURL, github, apiURL, format, output, provenance, mergeMethod string
	adopt, serialized                                                                                   bool
	pr                                                                                                  int
	timeout                                                                                             time.Duration
}

type inputError struct{ error }

func invalid(format string, args ...any) error { return inputError{fmt.Errorf(format, args...)} }

func parseOptions(command string, args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.path, "path", ".", "repository root")
	fs.StringVar(&o.ref, "ref", "", "full tested branch ref (refs/heads/...) ")
	fs.StringVar(&o.sha, "sha", "", "full tested commit SHA (defaults to local HEAD)")
	fs.StringVar(&o.config.MainBranch, "main-branch", "main", "stable release branch")
	fs.StringVar(&o.config.MainPattern, "main-pattern", version.DefaultMainPattern, "stable tag pattern")
	fs.StringVar(&o.config.FeaturePattern, "feature-pattern", version.DefaultFeaturePattern, "candidate suffix pattern")
	fs.StringVar(&o.mainRef, "main-ref", "", "main ref for offline next (defaults to refs/heads/<main-branch>)")
	fs.StringVar(&o.remote, "remote", "origin", "remote name")
	fs.StringVar(&o.remoteURL, "remote-url", "", "override HTTPS remote URL")
	fs.StringVar(&o.github, "github", "", "GitHub owner/repository (inferred from github.com remote)")
	fs.StringVar(&o.apiURL, "github-api-url", os.Getenv("GITHUB_API_URL"), "GitHub API URL")
	fs.StringVar(&o.format, "format", "tag", "output format: tag or json")
	fs.StringVar(&o.output, "github-output", "", "append outputs to this GitHub Actions output file")
	fs.StringVar(&o.provenance, "provenance", "", "verified merged-source JSON file (non-GitHub integrations)")
	fs.StringVar(&o.mergeMethod, "merge-method", "squash", "PR check strategy: squash, merge, rebase")
	fs.BoolVar(&o.adopt, "adopt-existing", false, "explicitly adopt existing unmanaged version tags")
	fs.BoolVar(&o.serialized, "serialized", false, "confirm this finalization is protected by a shared CI lock")
	fs.IntVar(&o.pr, "pr", 0, "pull request number for check-pr")
	fs.DurationVar(&o.timeout, "timeout", 5*time.Minute, "maximum command duration")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, err
		}
		return o, invalid("%v", err)
	}
	if fs.NArg() != 0 {
		return o, invalid("unexpected positional arguments")
	}
	if command != "check-pr" {
		if err := release.ValidateRef(o.ref); err != nil {
			return o, invalid("%v", err)
		}
	}
	if command == "release" && !o.serialized {
		return o, invalid("release requires --serialized and a shared CI lock covering every writer")
	}
	if command == "check-pr" && (o.pr <= 0 || o.github == "") {
		return o, invalid("check-pr requires --pr and --github owner/repository")
	}
	if o.format != "tag" && o.format != "json" {
		return o, invalid("format must be tag or json")
	}
	if o.timeout <= 0 {
		return o, invalid("timeout must be positive")
	}
	if o.mergeMethod != "squash" && o.mergeMethod != "merge" && o.mergeMethod != "rebase" {
		return o, invalid("unsupported merge method")
	}
	if o.sha != "" {
		if _, err := release.ParseHash(o.sha); err != nil {
			return o, invalid("%v", err)
		}
	}
	var err error
	o.config, err = o.config.Normalize()
	if err != nil {
		return o, invalid("%v", err)
	}
	return o, nil
}

func runCommand(ctx context.Context, command string, args []string, stdout, stderr io.Writer) int {
	o, err := parseOptions(command, args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "verver: invalid arguments:", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	result, err := execute(ctx, command, o)
	if err == nil && command == "check-pr" {
		_, err = fmt.Fprintln(stdout, "version intent check passed")
	} else if err == nil {
		if o.output != "" {
			err = writeOutputs(o.output, result)
		}
		if err == nil {
			if o.format == "json" {
				err = json.NewEncoder(stdout).Encode(result)
			} else {
				_, err = fmt.Fprintln(stdout, result.Tag)
			}
		}
		if err != nil {
			err = fmt.Errorf("assignment %s has status %s, but output failed: %w", result.Tag, result.Status, err)
		}
	}
	if err != nil {
		message := err.Error()
		for _, key := range []string{"VERVER_TOKEN", "GITHUB_TOKEN"} {
			if token := os.Getenv(key); token != "" {
				message = strings.ReplaceAll(message, token, "[redacted]")
			}
		}
		fmt.Fprintln(stderr, "verver:", message)
		var ie inputError
		if errors.As(err, &ie) {
			return 2
		}
		return 1
	}
	return 0
}

func execute(ctx context.Context, command string, o options) (release.Result, error) {
	empty := release.Result{}
	reader, err := repository.Open(o.path)
	if err != nil {
		return empty, err
	}
	pattern, _ := version.NewPattern(o.config.MainPattern, o.config.FeaturePattern)
	if command == "check-pr" {
		return empty, checkPR(ctx, reader, o, pattern)
	}
	var hash plumbing.Hash
	if o.sha == "" {
		hash, err = reader.Resolve("HEAD")
	} else {
		hash, err = release.ParseHash(o.sha)
	}
	if err != nil {
		return empty, err
	}
	req := release.Request{Config: o.config, Ref: o.ref, Commit: hash, Adopt: o.adopt}
	if o.provenance != "" {
		data, err := os.ReadFile(o.provenance)
		if err != nil {
			return empty, err
		}
		if len(data) > 1024*1024 {
			return empty, invalid("provenance exceeds 1 MiB")
		}
		if err := json.Unmarshal(data, &req.Sources); err != nil {
			return empty, invalid("invalid provenance: %v", err)
		}
	}
	if command == "next" {
		// Offline preview: no freshness or new GitHub merge-provenance guarantee.
		tags, err := reader.LocalInventory(ctx, pattern)
		if err != nil {
			return empty, err
		}
		if o.mainRef == "" {
			o.mainRef = "refs/heads/" + o.config.MainBranch
		}
		main, err := reader.Resolve(o.mainRef)
		if err != nil {
			return empty, err
		}
		// A detached checkout can preview its explicit tested ref; this is not a
		// remote reachability assertion, which is required again during release.
		return release.Calculate(ctx, reader, release.State{Tags: tags, Main: main, Branch: hash}, req)
	}
	if err := validatePushEvent(o, hash); err != nil {
		return empty, err
	}
	if os.Getenv("GITHUB_ACTIONS") == "true" && o.github == "" {
		o.github = os.Getenv("GITHUB_REPOSITORY")
	}
	remote, client, err := connect(reader, o)
	if err != nil {
		return empty, err
	}
	var provenance release.Provenance
	if client != nil {
		provenance = func(ctx context.Context, commits []repository.Commit) ([]release.Source, error) {
			return client.Sources(ctx, commits, o.config.MainBranch)
		}
	}
	return release.Finalize(ctx, reader, remote, req, provenance)
}

func connect(reader *repository.Reader, o options) (*repository.Remote, *gh.Client, error) {
	rawURL := o.remoteURL
	if rawURL == "" {
		var err error
		rawURL, err = reader.RemoteURL(o.remote)
		if err != nil {
			return nil, nil, err
		}
	}
	token := os.Getenv("VERVER_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	remote, err := reader.Connect(rawURL, token)
	if err != nil {
		return nil, nil, err
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	name := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if o.github == "" && strings.EqualFold(u.Host, "github.com") {
		o.github = name
	}
	var client *gh.Client
	if o.github != "" {
		if !strings.EqualFold(name, o.github) {
			return nil, nil, invalid("GitHub repository does not match the Git remote")
		}
		api := o.apiURL
		if api == "" {
			api = "https://api.github.com"
		}
		apiURL, err := url.Parse(api)
		if err != nil {
			return nil, nil, err
		}
		expected := u.Host
		if strings.EqualFold(u.Host, "github.com") {
			expected = "api.github.com"
		}
		if !strings.EqualFold(apiURL.Host, expected) {
			return nil, nil, invalid("GitHub API host does not match the Git remote")
		}
		client, err = gh.New(api, o.github, token)
		if err != nil {
			return nil, nil, err
		}
	}
	return remote, client, nil
}

func validatePushEvent(o options, hash plumbing.Hash) error {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return nil
	}
	if os.Getenv("GITHUB_EVENT_NAME") != "push" {
		return invalid("GitHub tag creation is only allowed for branch push events")
	}
	if os.Getenv("GITHUB_REF") != o.ref || os.Getenv("GITHUB_SHA") != hash.String() {
		return invalid("requested ref/SHA does not match the tested push event")
	}
	data, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return fmt.Errorf("read push event: %w", err)
	}
	var event struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	if event.Deleted || event.Ref != o.ref || event.After != hash.String() || !strings.EqualFold(event.Repository.FullName, os.Getenv("GITHUB_REPOSITORY")) {
		return invalid("push event does not match the requested assignment")
	}
	if o.github != "" && !strings.EqualFold(o.github, event.Repository.FullName) {
		return invalid("GitHub repository does not match push event")
	}
	return nil
}

func checkPR(ctx context.Context, reader *repository.Reader, o options, p version.Pattern) error {
	remote, client, err := connect(reader, o)
	if err != nil {
		return err
	}
	if client == nil {
		return invalid("GitHub client is required")
	}
	pull, err := client.Pull(ctx, o.pr)
	if err != nil {
		return err
	}
	if pull.Merged {
		return fmt.Errorf("PR is already merged")
	}
	if pull.Base.Ref != o.config.MainBranch {
		return fmt.Errorf("PR does not target configured main branch")
	}
	hash, err := release.ParseHash(pull.Head.SHA)
	if err != nil {
		return err
	}
	if o.sha != "" && o.sha != pull.Head.SHA {
		return fmt.Errorf("PR head changed; rerun checks on its current head")
	}
	if err := remote.EnsureSource(ctx, hash, o.pr); err != nil {
		return err
	}
	snapshot, err := remote.Refresh(ctx, p)
	if err != nil {
		return err
	}
	_, consumed, err := release.Inspect(snapshot.Tags, o.config, o.adopt)
	if err != nil {
		return err
	}
	excluded := []plumbing.Hash{snapshot.Heads["refs/heads/"+o.config.MainBranch]}
	commits, err := reader.Range(ctx, hash, excluded, consumed)
	if err != nil {
		return err
	}
	pending, err := release.ResolveIntent(commits)
	if err != nil {
		return err
	}
	if o.mergeMethod == "squash" {
		landed, err := release.ResolveIntent([]repository.Commit{{Message: pull.Title + "\n" + pull.Body}})
		if err != nil {
			return err
		}
		if !landed.Preserves(pending) {
			return fmt.Errorf("squash PR title or body must preserve %s; also keep it in the final squash message", pending.Marker())
		}
	}
	return nil
}

func writeOutputs(path string, r release.Result) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(f, "tag=%s\nsha=%s\nkind=%s\nstatus=%s\n", r.Tag, r.Commit, r.Kind, r.Status)
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}
