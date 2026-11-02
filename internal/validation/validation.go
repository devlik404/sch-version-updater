package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"sch-version-updater/internal/model"
)

const (
	govulncheckVersion       = "v1.6.0"
	maxGoRemediationRounds   = 5
	maxGoVersionConflictRuns = 10
)

var goVersionConflict = regexp.MustCompile(`requires ([^@ ]+)@(v[^ ,]+), not ([^@ ]+)@(v[^ ,]+)`)

type Service interface {
	ValidateAndFix(context.Context, string, model.Job) ([]string, error)
}

type Runner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CI=true", "GIT_TERMINAL_PROMPT=0")
	return cmd.CombinedOutput()
}

type Validator struct{ runner Runner }

func New() *Validator                   { return &Validator{runner: execRunner{}} }
func NewWithRunner(r Runner) *Validator { return &Validator{runner: r} }

func (v *Validator) ValidateAndFix(ctx context.Context, root string, job model.Job) ([]string, error) {
	switch strings.ToUpper(job.TargetType) {
	case "GO":
		return v.validateGo(ctx, root, job)
	case "NODEJS":
		return v.validateNode(ctx, root, job)
	case "JAVA":
		return v.validateJava(ctx, root, job)
	case "DOCKER_IMAGE":
		// Docker image jobs only replace the explicitly requested tag. The
		// updater validates the resulting Dockerfile syntax; no image pull,
		// vulnerability scan, or compatibility test is performed here.
		return nil, nil
	default:
		return nil, fmt.Errorf("dependency validation unsupported for target_type %q", job.TargetType)
	}
}

func (v *Validator) validateGo(ctx context.Context, root string, job model.Job) ([]string, error) {
	dir, manifest, err := findProject(root, job.FilePath, "go.mod")
	if err != nil {
		return nil, errors.New("GO validation requires go.mod in the target directory or one of its parents")
	}
	if err := v.mustRun(ctx, dir, "go", "test", "-p=1", "./..."); err != nil {
		return nil, fmt.Errorf("Go compatibility test failed: %w", err)
	}
	remediated := false
	for round := 0; ; round++ {
		fixes, scanErr := v.scanGo(ctx, dir)
		if scanErr == nil {
			if !remediated {
				return nil, nil
			}
			return []string{manifest, filepath.ToSlash(filepath.Join(filepath.Dir(manifest), "go.sum"))}, nil
		}
		if len(fixes) == 0 {
			return nil, scanErr
		}
		if round == maxGoRemediationRounds {
			return nil, fmt.Errorf("Go vulnerabilities remain after %d remediation rounds: %w", maxGoRemediationRounds, scanErr)
		}
		if err := v.updateGoModules(ctx, dir, fixes); err != nil {
			return nil, fmt.Errorf("update vulnerable Go modules: %w", err)
		}
		if err := v.mustRun(ctx, dir, "go", "mod", "tidy"); err != nil {
			return nil, fmt.Errorf("tidy remediated Go modules: %w", err)
		}
		if err := v.mustRun(ctx, dir, "go", "test", "-p=1", "./..."); err != nil {
			return nil, fmt.Errorf("Go test after remediation round %d failed: %w", round+1, err)
		}
		remediated = true
	}
}

func (v *Validator) updateGoModules(ctx context.Context, dir string, fixes map[string]string) error {
	selected := make(map[string]string, len(fixes))
	for module, version := range fixes {
		selected[module] = version
	}
	for attempt := 0; attempt < maxGoVersionConflictRuns; attempt++ {
		modules := make([]string, 0, len(selected))
		for module, version := range selected {
			modules = append(modules, module+"@"+version)
		}
		sort.Strings(modules)
		out, err := v.runner.Run(ctx, dir, "go", append([]string{"get"}, modules...)...)
		if err == nil {
			return nil
		}
		adjusted := false
		for _, match := range goVersionConflict.FindAllStringSubmatch(string(out), -1) {
			module, required, rejectedModule := match[1], match[2], match[3]
			if module != rejectedModule {
				continue
			}
			current, requested := selected[module]
			if requested && semver.Compare(required, current) <= 0 {
				continue
			}
			// The rejected version can come from the repository's existing
			// go.mod rather than from a govulncheck fix. Include the required
			// transitive module explicitly so the next go get can resolve the
			// vulnerable module upgrade.
			selected[module] = required
			adjusted = true
		}
		if !adjusted {
			return fmt.Errorf("go: %w: %s", err, compact(out))
		}
	}
	return fmt.Errorf("could not resolve Go module version conflicts after %d attempts", maxGoVersionConflictRuns)
}

type goMessage struct {
	Finding *struct {
		OSV          string `json:"osv"`
		FixedVersion string `json:"fixed_version"`
		Trace        []struct {
			Module   string `json:"module"`
			Function string `json:"function"`
		} `json:"trace"`
	} `json:"finding"`
}

func (v *Validator) scanGo(ctx context.Context, root string) (map[string]string, error) {
	command := "go"
	args := []string{"run", "golang.org/x/vuln/cmd/govulncheck@" + govulncheckVersion, "-json", "./..."}
	if _, ok := v.runner.(execRunner); ok {
		if _, err := exec.LookPath("govulncheck"); err == nil {
			command = "govulncheck"
			args = []string{"-json", "./..."}
		}
	}
	out, runErr := v.runner.Run(ctx, root, command, args...)
	decoder := json.NewDecoder(bytes.NewReader(out))
	fixes := map[string]string{}
	var vulnerable, unfixable []string
	for {
		var message goMessage
		if err := decoder.Decode(&message); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse govulncheck output: %w", err)
		}
		f := message.Finding
		if f == nil || len(f.Trace) == 0 || f.Trace[0].Function == "" {
			continue // Only symbol-level findings are reachable.
		}
		module := f.Trace[0].Module
		vulnerable = append(vulnerable, f.OSV)
		if module == "stdlib" || f.FixedVersion == "" {
			unfixable = append(unfixable, f.OSV)
			continue
		}
		if current := fixes[module]; current == "" || semver.Compare(f.FixedVersion, current) > 0 {
			fixes[module] = f.FixedVersion
		}
	}
	if len(vulnerable) == 0 {
		if runErr != nil {
			return nil, fmt.Errorf("govulncheck failed: %w: %s", runErr, compact(out))
		}
		return nil, nil
	}
	if len(unfixable) > 0 {
		sort.Strings(unfixable)
		return nil, fmt.Errorf("reachable vulnerabilities require runtime/manual remediation: %s", strings.Join(unique(unfixable), ", "))
	}
	return fixes, fmt.Errorf("reachable Go vulnerabilities found: %s", strings.Join(unique(vulnerable), ", "))
}

func (v *Validator) validateNode(ctx context.Context, root string, job model.Job) ([]string, error) {
	dir, manifest, err := findProject(root, job.FilePath, "package.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err != nil {
		return nil, errors.New("NODEJS validation requires package-lock.json")
	}
	if clean, err := v.npmAudit(ctx, dir); err != nil {
		return nil, err
	} else if !clean {
		if err := v.mustRun(ctx, dir, "npm", "audit", "fix", "--package-lock-only", "--ignore-scripts"); err != nil {
			return nil, fmt.Errorf("npm audit fix failed: %w", err)
		}
		if clean, err = v.npmAudit(ctx, dir); err != nil {
			return nil, err
		} else if !clean {
			return nil, errors.New("reachable Node.js vulnerabilities remain after non-breaking npm audit fix")
		}
	}
	if err := v.mustRun(ctx, dir, "npm", "ci", "--ignore-scripts"); err != nil {
		return nil, fmt.Errorf("npm compatibility install failed: %w", err)
	}
	if err := v.mustRun(ctx, dir, "npm", "test", "--if-present"); err != nil {
		return nil, fmt.Errorf("npm test failed: %w", err)
	}
	base := filepath.Dir(manifest)
	return []string{filepath.ToSlash(filepath.Join(base, "package.json")), filepath.ToSlash(filepath.Join(base, "package-lock.json"))}, nil
}

func (v *Validator) npmAudit(ctx context.Context, root string) (bool, error) {
	out, runErr := v.runner.Run(ctx, root, "npm", "audit", "--json", "--package-lock-only")
	var report struct {
		Metadata struct {
			Vulnerabilities map[string]int `json:"vulnerabilities"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return false, fmt.Errorf("parse npm audit output: %w: %s", err, compact(out))
	}
	total := 0
	for _, n := range report.Metadata.Vulnerabilities {
		total += n
	}
	if total == 0 && runErr != nil {
		return false, fmt.Errorf("npm audit failed: %w: %s", runErr, compact(out))
	}
	return total == 0, nil
}

func (v *Validator) validateJava(ctx context.Context, root string, job model.Job) ([]string, error) {
	dir, manifest, err := findProject(root, job.FilePath, "pom.xml")
	if err != nil {
		return nil, errors.New("JAVA validation currently requires pom.xml in the target directory or one of its parents")
	}
	if err := v.mustRun(ctx, dir, "mvn", "-B", "-ntp", "test"); err != nil {
		return nil, fmt.Errorf("Maven compatibility test failed: %w", err)
	}
	if err := v.mustRun(ctx, dir, "mvn", "-B", "-ntp", "org.owasp:dependency-check-maven:12.1.8:check", "-DfailBuildOnCVSS=0"); err != nil {
		return nil, fmt.Errorf("Maven vulnerability scan failed; OWASP does not provide a deterministic fixed Maven version, so no speculative upgrade was made: %w", err)
	}
	return []string{manifest}, nil
}

func (v *Validator) mustRun(ctx context.Context, dir, name string, args ...string) error {
	out, err := v.runner.Run(ctx, dir, name, args...)
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, compact(out))
	}
	return nil
}

func compact(b []byte) string {
	s := strings.TrimSpace(string(b))
	const (
		limit      = 4000
		prefixSize = 1000
	)
	runes := []rune(s)
	if len(runes) > limit {
		return string(runes[:prefixSize]) + "\n… output truncated …\n" + string(runes[len(runes)-(limit-prefixSize):])
	}
	return s
}
func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func findProject(root, target, manifest string) (string, string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	dir := filepath.Dir(filepath.Join(absRoot, filepath.Clean(target)))
	if filepath.Base(target) == manifest {
		dir = filepath.Dir(filepath.Join(absRoot, filepath.Clean(target)))
	}
	for {
		candidate := filepath.Join(dir, manifest)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			rel, relErr := filepath.Rel(absRoot, candidate)
			if relErr != nil {
				return "", "", relErr
			}
			return dir, filepath.ToSlash(rel), nil
		}
		if dir == absRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir || !within(absRoot, parent) {
			break
		}
		dir = parent
	}
	return "", "", os.ErrNotExist
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
