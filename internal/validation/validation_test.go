package validation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sch-version-updater/internal/model"
)

type response struct {
	out string
	err error
}
type fakeRunner struct {
	responses []response
	calls     []string
}

func (f *fakeRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if len(f.responses) == 0 {
		return nil, nil
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	return []byte(r.out), r.err
}

func TestGoUpdatesOnlyReachableFixedModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	vuln := `{"finding":{"osv":"GO-TEST-1","fixed_version":"v1.2.3","trace":[{"module":"example.com/dependency","function":"Vulnerable"}]}}` + "\n"
	r := &fakeRunner{responses: []response{{}, {vuln, errors.New("exit 3")}, {}, {}, {}, {}}}
	allowed, err := NewWithRunner(r).ValidateAndFix(context.Background(), dir, model.Job{TargetType: "GO", FilePath: "go.mod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 2 || allowed[0] != "go.mod" || allowed[1] != "go.sum" {
		t.Fatalf("allowed=%v", allowed)
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "go get example.com/dependency@v1.2.3") {
		t.Fatalf("calls=%v", r.calls)
	}
	if !strings.Contains(strings.Join(r.calls, "\n"), "go test -p=1 ./...") {
		t.Fatalf("Go test must limit package build parallelism: calls=%v", r.calls)
	}
}

func TestGoRemediatesNewFindingFromSecondScan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	first := `{"finding":{"osv":"GO-FIRST","fixed_version":"v1.2.3","trace":[{"module":"example.com/first","function":"Vulnerable"}]}}` + "\n"
	second := `{"finding":{"osv":"GO-SECOND","fixed_version":"v2.3.4","trace":[{"module":"example.com/second","function":"Vulnerable"}]}}` + "\n"
	r := &fakeRunner{responses: []response{
		{},
		{first, errors.New("exit 3")}, {}, {}, {},
		{second, errors.New("exit 3")}, {}, {}, {},
		{},
	}}
	allowed, err := NewWithRunner(r).ValidateAndFix(context.Background(), dir, model.Job{TargetType: "GO", FilePath: "go.mod"})
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(r.calls, "\n")
	if len(allowed) != 2 || !strings.Contains(calls, "go get example.com/first@v1.2.3") || !strings.Contains(calls, "go get example.com/second@v2.3.4") {
		t.Fatalf("allowed=%v calls=%v", allowed, r.calls)
	}
}

func TestGoRaisesFixedVersionRequiredByAnotherUpgrade(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	vulns := `{"finding":{"osv":"GO-EXCEL","fixed_version":"v2.11.1-0.20260915055537-22f76f9acb94","trace":[{"module":"github.com/xuri/excelize/v2","function":"OpenFile"}]}}` + "\n" +
		`{"finding":{"osv":"GO-TEXT","fixed_version":"v0.38.0","trace":[{"module":"golang.org/x/text","function":"Parse"}]}}` + "\n"
	conflict := "go: github.com/xuri/excelize/v2@v2.11.1-0.20260915055537-22f76f9acb94 requires golang.org/x/text@v0.41.0, not golang.org/x/text@v0.39.0"
	if match := goVersionConflict.FindStringSubmatch(conflict); len(match) != 5 {
		t.Fatalf("version conflict did not match: %q", conflict)
	}
	r := &fakeRunner{responses: []response{
		{},
		{vulns, errors.New("exit 3")},
		{conflict, errors.New("exit 1")}, {},
		{}, {}, {},
	}}
	if _, err := NewWithRunner(r).ValidateAndFix(context.Background(), dir, model.Job{TargetType: "GO", FilePath: "go.mod"}); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(r.calls, "\n")
	if !strings.Contains(calls, "golang.org/x/text@v0.41.0") {
		t.Fatalf("higher required version was not selected: calls=%v", r.calls)
	}
}

func TestGoKeepsHighestFixForSameModule(t *testing.T) {
	vulns := `{"finding":{"osv":"GO-HIGH","fixed_version":"v0.41.0","trace":[{"module":"golang.org/x/text","function":"High"}]}}` + "\n" +
		`{"finding":{"osv":"GO-LOW","fixed_version":"v0.39.0","trace":[{"module":"golang.org/x/text","function":"Low"}]}}` + "\n"
	r := &fakeRunner{responses: []response{{vulns, errors.New("exit 3")}}}
	fixes, err := NewWithRunner(r).scanGo(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected vulnerability error")
	}
	if got := fixes["golang.org/x/text"]; got != "v0.41.0" {
		t.Fatalf("fixed version=%q, want v0.41.0", got)
	}
}

func TestCompactPreservesCommandErrorAtEnd(t *testing.T) {
	input := "download output\n" + strings.Repeat("x", 5000) + "\nFINAL TEST ERROR"
	got := compact([]byte(input))
	if !strings.HasPrefix(got, "download output") {
		t.Fatalf("compact output lost prefix: %q", got)
	}
	if !strings.Contains(got, "output truncated") || !strings.HasSuffix(got, "FINAL TEST ERROR") {
		t.Fatalf("compact output lost final error: %q", got)
	}
}

func TestGoDoesNotAutoFixStandardLibrary(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.25\n"), 0644)
	vuln := `{"finding":{"osv":"GO-STDLIB","fixed_version":"1.25.9","trace":[{"module":"stdlib","function":"Bad"}]}}` + "\n"
	r := &fakeRunner{responses: []response{{}, {vuln, errors.New("exit 3")}}}
	_, err := NewWithRunner(r).ValidateAndFix(context.Background(), dir, model.Job{TargetType: "GO", FilePath: "go.mod"})
	if err == nil || !strings.Contains(err.Error(), "runtime/manual") {
		t.Fatalf("got %v", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("unexpected remediation calls: %v", r.calls)
	}
}

func TestNodeRunsNonBreakingAuditFix(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{}}`), 0644)
	os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(`{}`), 0644)
	vulnerable := `{"metadata":{"vulnerabilities":{"high":1,"total":1}}}`
	clean := `{"metadata":{"vulnerabilities":{"high":0,"total":0}}}`
	r := &fakeRunner{responses: []response{{vulnerable, errors.New("exit 1")}, {}, {clean, nil}, {}, {}}}
	allowed, err := NewWithRunner(r).ValidateAndFix(context.Background(), dir, model.Job{TargetType: "NODEJS", FilePath: ".nvmrc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 2 || !strings.Contains(strings.Join(r.calls, "\n"), "npm audit fix --package-lock-only --ignore-scripts") {
		t.Fatalf("allowed=%v calls=%v", allowed, r.calls)
	}
}

func TestDockerSkipsDependencyValidation(t *testing.T) {
	r := &fakeRunner{}
	allowed, err := NewWithRunner(r).ValidateAndFix(context.Background(), t.TempDir(), model.Job{TargetType: "DOCKER_IMAGE", ImageName: "registry/app", TargetVersion: "2"})
	if err != nil || len(allowed) != 0 || len(r.calls) != 0 {
		t.Fatalf("allowed=%v calls=%v err=%v", allowed, r.calls, err)
	}
}

func TestFindProjectWalksToParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config", "java"), 0755); err != nil {
		t.Fatal(err)
	}
	projectDir, manifest, err := findProject(dir, "config/java/runtime.xml", "pom.xml")
	if err != nil {
		t.Fatal(err)
	}
	if projectDir != dir || manifest != "pom.xml" {
		t.Fatalf("dir=%q manifest=%q", projectDir, manifest)
	}
}
