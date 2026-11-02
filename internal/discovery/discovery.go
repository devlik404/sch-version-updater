package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sch-version-updater/internal/model"
	"sch-version-updater/internal/repository"
)

const discoveryBranch = "release/development"

type Cloner interface {
	CloneBranch(context.Context, string, string, string) error
}

type RemoteRepository struct{ Name, Slug, HTTPSURL, ProjectKey, ProjectName string }

type BitbucketClient struct {
	endpoint, token string
	client          *http.Client
}

func NewBitbucketClient(projectURL, tokenPath string) (*BitbucketClient, error) {
	u, err := url.Parse(projectURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("BITBUCKET_PROJECT_URL must be an absolute HTTP(S) URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || !strings.EqualFold(parts[len(parts)-2], "projects") || parts[len(parts)-1] == "" {
		return nil, errors.New("BITBUCKET_PROJECT_URL must end with /projects/{project-key}")
	}
	key := parts[len(parts)-1]
	prefix := "/" + strings.Join(parts[:len(parts)-2], "/")
	if prefix == "/" {
		prefix = ""
	}
	u.Path = prefix + "/rest/api/1.0/projects/" + url.PathEscape(key) + "/repos"
	u.RawQuery = ""
	u.Fragment = ""
	info, err := os.Stat(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("stat Bitbucket API token file: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Bitbucket API token permissions must not allow group/other access: %s", info.Mode().Perm())
	}
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("read Bitbucket API token file: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return nil, errors.New("Bitbucket API token file is empty")
	}
	return &BitbucketClient{endpoint: u.String(), token: token, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *BitbucketClient) ListRepositories(ctx context.Context) ([]RemoteRepository, error) {
	start := 0
	var result []RemoteRepository
	for {
		u, _ := url.Parse(c.endpoint)
		q := u.Query()
		q.Set("limit", "100")
		q.Set("start", fmt.Sprint(start))
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list Bitbucket repositories: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Bitbucket repository list returned HTTP %d", resp.StatusCode)
		}
		var page struct {
			Values []struct {
				Name, Slug string
				Project    struct{ Key, Name string }
				Links      struct{ Clone []struct{ Name, Href string } }
			}
			IsLastPage    bool `json:"isLastPage"`
			NextPageStart int  `json:"nextPageStart"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decode Bitbucket repositories: %w", err)
		}
		for _, item := range page.Values {
			https := ""
			for _, link := range item.Links.Clone {
				if strings.EqualFold(link.Name, "http") && strings.HasPrefix(link.Href, "https://") {
					https = link.Href
					break
				}
			}
			if https != "" {
				result = append(result, RemoteRepository{Name: item.Name, Slug: item.Slug, HTTPSURL: https, ProjectKey: item.Project.Key, ProjectName: item.Project.Name})
			}
		}
		if page.IsLastPage {
			break
		}
		if page.NextPageStart <= start {
			return nil, errors.New("invalid Bitbucket pagination")
		}
		start = page.NextPageStart
	}
	return result, nil
}

type Service struct {
	api                 *BitbucketClient
	jobs                repository.DiscoveryJobs
	git                 Cloner
	root                string
	interval, timeLimit time.Duration
	logger              *slog.Logger
	now                 func() time.Time
}

func New(api *BitbucketClient, jobs repository.DiscoveryJobs, git Cloner, root string, interval, timeLimit time.Duration, logger *slog.Logger) *Service {
	return &Service{api: api, jobs: jobs, git: git, root: root, interval: interval, timeLimit: timeLimit, logger: logger, now: time.Now}
}
func (s *Service) Run(ctx context.Context) {
	delay, latest, err := s.initialDelay(ctx)
	if err != nil {
		s.logger.Error("schedule repository discovery", "error", err)
		delay = s.interval
	}
	if delay > 0 {
		nextRun := s.now().Add(delay)
		attrs := []any{"next_run_at", nextRun, "delay", delay}
		if latest != nil {
			attrs = append(attrs, "latest_created_at", *latest)
		}
		s.logger.Info("repository discovery scheduled", attrs...)
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
	s.runOnce(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Service) initialDelay(ctx context.Context) (time.Duration, *time.Time, error) {
	latest, err := s.jobs.LatestCreatedAt(ctx)
	if err != nil || latest == nil {
		return 0, latest, err
	}
	delay := latest.Add(s.interval).Sub(s.now())
	if delay < 0 {
		delay = 0
	}
	return delay, latest, nil
}
func (s *Service) runOnce(ctx context.Context) {
	repos, err := s.api.ListRepositories(ctx)
	if err != nil {
		s.logger.Error("repository discovery failed", "error", err)
		return
	}
	for _, remote := range repos {
		if ctx.Err() != nil {
			return
		}
		s.discover(ctx, remote)
	}
}
func (s *Service) discover(parent context.Context, remote RemoteRepository) {
	ctx, cancel := context.WithTimeout(parent, s.timeLimit)
	defer cancel()
	log := s.logger.With("repo", remote.HTTPSURL, "branch", discoveryBranch)
	workspace, err := os.MkdirTemp(s.root, "repository-discovery-")
	if err != nil {
		log.Error("create discovery workspace", "error", err)
		return
	}
	defer func() {
		if err := os.RemoveAll(workspace); err != nil {
			log.Error("discovery workspace cleanup failed", "path", workspace, "error", err)
		}
	}()
	repoDir := filepath.Join(workspace, "repository")
	if err := s.git.CloneBranch(ctx, remote.HTTPSURL, discoveryBranch, repoDir); err != nil {
		log.Warn("repository skipped", "error", err)
		return
	}
	target, err := Detect(repoDir)
	if err != nil {
		log.Info("repository has no supported version target", "reason", err)
		return
	}
	job := model.Job{ProductID: remote.ProjectKey, ProductName: remote.ProjectName, JobName: remote.Name, JobType: "VERSION_UPDATE", RepoURL: remote.HTTPSURL, BranchName: discoveryBranch, FilePath: target.FilePath, TargetType: target.Type, ImageName: target.ImageName, CurrentVersion: target.Version}
	created, err := s.jobs.UpsertDiscovered(ctx, job)
	if err != nil {
		log.Error("save discovered repository", "error", err)
		return
	}
	log.Info("repository discovery complete", "created", created, "target_type", target.Type, "current_version", target.Version)
}

type Target struct{ Type, FilePath, ImageName, Version string }

func Detect(root string) (Target, error) {
	type candidate struct {
		depth int
		path  string
	}
	found := map[string][]candidate{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules" || d.Name() == "target") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		name := d.Name()
		if name == "go.mod" || name == "pom.xml" || name == ".nvmrc" || strings.EqualFold(name, "Dockerfile") {
			rel, _ := filepath.Rel(root, path)
			found[name] = append(found[name], candidate{strings.Count(filepath.ToSlash(rel), "/"), rel})
		}
		return nil
	})
	if err != nil {
		return Target{}, err
	}
	for _, name := range []string{"go.mod", "pom.xml", ".nvmrc", "Dockerfile"} {
		items := found[name]
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].depth == items[j].depth {
				return items[i].path < items[j].path
			}
			return items[i].depth < items[j].depth
		})
		return readTarget(root, items[0].path, name)
	}
	return Target{}, errors.New("no go.mod, pom.xml, .nvmrc, or Dockerfile found")
}

var goDirective = regexp.MustCompile(`(?m)^go\s+([^\s]+)`)
var javaProperty = regexp.MustCompile(`(?s)<(?:java\.version|maven\.compiler\.release)>\s*([^<\s]+)`)
var dockerFrom = regexp.MustCompile(`(?mi)^\s*FROM(?:\s+--platform=\S+)?\s+(\S+)`)

func readTarget(root, rel, name string) (Target, error) {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return Target{}, err
	}
	path := filepath.ToSlash(rel)
	switch name {
	case "go.mod":
		m := goDirective.FindSubmatch(b)
		if len(m) < 2 {
			return Target{}, errors.New("go directive not found")
		}
		return Target{Type: "GO", FilePath: path, Version: string(m[1])}, nil
	case "pom.xml":
		m := javaProperty.FindSubmatch(b)
		if len(m) < 2 {
			return Target{}, errors.New("Java version property not found")
		}
		return Target{Type: "JAVA", FilePath: path, Version: string(m[1])}, nil
	case ".nvmrc":
		scanner := bufio.NewScanner(strings.NewReader(string(b)))
		if !scanner.Scan() || strings.TrimSpace(scanner.Text()) == "" {
			return Target{}, errors.New("empty .nvmrc")
		}
		return Target{Type: "NODEJS", FilePath: path, Version: strings.TrimSpace(scanner.Text())}, nil
	default:
		m := dockerFrom.FindSubmatch(b)
		if len(m) < 2 {
			return Target{}, errors.New("Docker FROM image not found")
		}
		image, version := splitImage(string(m[1]))
		if version == "" {
			return Target{}, errors.New("Docker image has no tag")
		}
		return Target{Type: "DOCKER_IMAGE", FilePath: path, ImageName: image, Version: version}, nil
	}
}
func splitImage(ref string) (string, string) {
	if strings.Contains(ref, "@") {
		return "", ""
	}
	slash, colon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":")
	if colon <= slash {
		return ref, ""
	}
	return ref[:colon], ref[colon+1:]
}
