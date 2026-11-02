package gitservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sch-version-updater/internal/model"
)

type call struct {
	args []string
	env  []string
}
type fakeRunner struct {
	calls       []call
	failCommand string
}

func (f *fakeRunner) Run(_ context.Context, _ string, env, args []string) ([]byte, error) {
	f.calls = append(f.calls, call{args: args, env: env})
	if len(args) > 0 && args[0] == f.failCommand {
		return []byte("simulated"), errors.New("failed")
	}
	if len(args) > 0 && args[0] == "status" {
		return []byte(" M go.mod\x00"), nil
	}
	return nil, nil
}
func credentials(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	token := filepath.Join(d, "token")
	if err := os.WriteFile(token, []byte("fake-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return token
}
func TestPrepareUsesHTTPSBearerTokenAndSeparateArgs(t *testing.T) {
	token := credentials(t)
	r := &fakeRunner{}
	g := NewWithRunner(token, "bot", "bot@example", 1, r)
	j := model.Job{RepoURL: "https://bitbucket.org/team/repo.git", BranchName: "main"}
	if err := g.Prepare(context.Background(), j, "/tmp/repo", "chore/update"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("calls=%d", len(r.calls))
	}
	joined := strings.Join(r.calls[0].env, " ")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.extraHeader") || !strings.Contains(joined, "Authorization: Bearer fake-token") || !strings.Contains(joined, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("env=%q", joined)
	}
}
func TestPrepareAcceptsBitbucketServerHTTPSURL(t *testing.T) {
	token := credentials(t)
	r := &fakeRunner{}
	g := NewWithRunner(token, "bot", "bot@example", 1, r)
	j := model.Job{RepoURL: "https://bitbucket.internal.example:8443/scm/team/repo.git", BranchName: "main"}
	if err := g.Prepare(context.Background(), j, "/tmp/repo", "chore/update"); err != nil {
		t.Fatal(err)
	}
	if got := r.calls[0].args[7]; got != j.RepoURL {
		t.Fatalf("clone URL changed: got %q", got)
	}
}
func TestRejectsSSHRepository(t *testing.T) {
	token := credentials(t)
	g := NewWithRunner(token, "bot", "bot@example", 1, &fakeRunner{})
	err := g.Prepare(context.Background(), model.Job{RepoURL: "git@bitbucket.org:team/repo.git"}, "/tmp/repo", "branch")
	if err == nil {
		t.Fatal("expected error")
	}
}
func TestRejectsUnsafeHTTPSRepositories(t *testing.T) {
	tests := []string{
		"http://bitbucket.internal/team/repo.git",
		"https://user:password@bitbucket.internal/team/repo.git",
		"https://bitbucket.internal/",
		"https://bitbucket.internal/repo.git\n--upload-pack=evil",
		"https://bitbucket.internal/repo.git?token=secret",
	}
	for _, repoURL := range tests {
		t.Run(repoURL, func(t *testing.T) {
			if isHTTPSURL(repoURL) {
				t.Fatalf("accepted unsafe URL %q", repoURL)
			}
		})
	}
}
func TestPushFailureReturned(t *testing.T) {
	token := credentials(t)
	r := &fakeRunner{failCommand: "push"}
	g := NewWithRunner(token, "bot", "bot@example", 2, r)
	err := g.CommitAndPush(context.Background(), t.TempDir(), model.Job{FilePath: "go.mod", TargetType: "GO", TargetVersion: "1.23"}, "branch", []string{"go.mod", "go.sum"})
	if err == nil || !strings.Contains(err.Error(), "git push") {
		t.Fatalf("got %v", err)
	}
}
func TestChangedFilesIncludesUntrackedAndRename(t *testing.T) {
	token := credentials(t)
	g := NewWithRunner(token, "bot", "bot@example", 1, RunnerFunc(func(_ context.Context, _ string, _ []string, args []string) ([]byte, error) {
		return []byte(" M go.mod\x00?? go.sum\x00R  new.txt\x00old.txt\x00"), nil
	}))
	files, err := g.ChangedFiles(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(files, ","); got != "go.mod,go.sum,new.txt,old.txt" {
		t.Fatalf("files=%q", got)
	}
}

type RunnerFunc func(context.Context, string, []string, []string) ([]byte, error)

func (f RunnerFunc) Run(ctx context.Context, dir string, env, args []string) ([]byte, error) {
	return f(ctx, dir, env, args)
}
