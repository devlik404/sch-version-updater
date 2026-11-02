package gitservice

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sch-version-updater/internal/model"
)

type Service interface {
	CloneBranch(context.Context, string, string, string) error
	Prepare(context.Context, model.Job, string, string) error
	ChangedFiles(context.Context, string) ([]string, error)
	CommitAndPush(context.Context, string, model.Job, string, []string) error
}
type Runner interface {
	Run(context.Context, string, []string, []string) ([]byte, error)
}
type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir string, env, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

type Git struct {
	tokenPath, userName, userEmail string
	retries                        int
	runner                         Runner
}

func New(tokenPath, userName, userEmail string, retries int) *Git {
	return &Git{tokenPath: tokenPath, userName: userName, userEmail: userEmail, retries: retries, runner: execRunner{}}
}
func NewWithRunner(tokenPath, userName, userEmail string, retries int, runner Runner) *Git {
	g := New(tokenPath, userName, userEmail, retries)
	g.runner = runner
	return g
}

func (g *Git) authEnv() ([]string, error) {
	if !filepath.IsAbs(g.tokenPath) {
		return nil, errors.New("Bitbucket API token path must be absolute")
	}
	info, err := os.Stat(g.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("Bitbucket API token: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Bitbucket API token permissions must not allow group/other access: %s", info.Mode().Perm())
	}
	tokenBytes, err := os.ReadFile(g.tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read Bitbucket API token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("Bitbucket API token file is empty or invalid")
	}
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Bearer " + token,
		"GIT_TERMINAL_PROMPT=0",
	}, nil
}
func (g *Git) run(ctx context.Context, dir string, env []string, args ...string) error {
	out, err := g.runner.Run(ctx, dir, env, args)
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
func (g *Git) Prepare(ctx context.Context, job model.Job, dir, branch string) error {
	if err := g.CloneBranch(ctx, job.RepoURL, job.BranchName, dir); err != nil {
		return err
	}
	return g.run(ctx, dir, nil, "checkout", "-b", branch)
}
func (g *Git) CloneBranch(ctx context.Context, repoURL, branch, dir string) error {
	if !isHTTPSURL(repoURL) {
		return errors.New("repo_url must be an HTTPS URL without embedded credentials")
	}
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, "\r\n\t ") {
		return errors.New("invalid branch name")
	}
	env, err := g.authEnv()
	if err != nil {
		return err
	}
	return g.run(ctx, "", env, "clone", "--depth", "1", "--branch", branch, "--single-branch", "--", repoURL, dir)
}
func isHTTPSURL(v string) bool {
	if v == "" || strings.ContainsAny(v, "\r\n\t ") {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Hostname() != "" &&
		u.Path != "" && u.Path != "/" && u.RawQuery == "" && u.Fragment == ""
}

func (g *Git) ChangedFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := g.runner.Run(ctx, dir, nil, []string{"status", "--porcelain=v1", "-z", "--untracked-files=all"})
	if err != nil {
		return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	items := strings.Split(string(out), "\x00")
	files := make([]string, 0, len(items))
	for i := 0; i < len(items); i++ {
		record := items[i]
		if record == "" {
			continue
		}
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("parse git status entry %q", record)
		}
		files = append(files, record[3:])
		if strings.ContainsAny(record[:2], "RC") && i+1 < len(items) {
			i++
			if items[i] != "" {
				files = append(files, items[i])
			}
		}
	}
	return files, nil
}
func (g *Git) CommitAndPush(ctx context.Context, dir string, job model.Job, branch string, changedFiles []string) error {
	authEnv, err := g.authEnv()
	if err != nil {
		return err
	}
	addArgs := []string{"add", "--"}
	for _, file := range changedFiles {
		addArgs = append(addArgs, filepath.ToSlash(filepath.Clean(file)))
	}
	if err := g.run(ctx, dir, nil, addArgs...); err != nil {
		return err
	}
	commitEnv := []string{"GIT_AUTHOR_NAME=" + g.userName, "GIT_AUTHOR_EMAIL=" + g.userEmail, "GIT_COMMITTER_NAME=" + g.userName, "GIT_COMMITTER_EMAIL=" + g.userEmail}
	if err := g.run(ctx, dir, commitEnv, "commit", "-m", fmt.Sprintf("chore: update %s to %s", strings.ToLower(job.TargetType), job.TargetVersion)); err != nil {
		return err
	}
	var pushErr error
	for attempt := 1; attempt <= g.retries; attempt++ {
		pushErr = g.run(ctx, dir, authEnv, "push", "--set-upstream", "origin", branch)
		if pushErr == nil {
			return nil
		}
		if attempt < g.retries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
			}
		}
	}
	return pushErr
}
