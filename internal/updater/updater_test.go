package updater

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sch-version-updater/internal/model"
)

func TestUpdates(t *testing.T) {
	tests := []struct{ name, typ, file, input, image, target, want string }{
		{"go.mod", "GO", "go.mod", "module example.com/x\n\ngo 1.21\n\nrequire example.com/a v1.2.3\n", "", "1.23.4", "go 1.23.4"},
		{"java property", "JAVA", "pom.xml", "<project><properties><java.version>17</java.version><other>keep</other></properties></project>", "", "21", "<java.version>21</java.version>"},
		{"java release", "JAVA", "pom.xml", "<project><properties><maven.compiler.release>17</maven.compiler.release></properties></project>", "", "21", "<maven.compiler.release>21</maven.compiler.release>"},
		{"node newline", "NODEJS", ".nvmrc", "18.1.0\n", "", "20.12.2", "20.12.2\n"},
		{"docker all matches", "DOCKER_IMAGE", "Dockerfile", "FROM registry.local/team/go:1.21 AS build\nFROM registry.local/team/go:1.21\nRUN echo 1.21\n", "registry.local/team/go", "1.23", "FROM registry.local/team/go:1.23 AS build\nFROM registry.local/team/go:1.23\nRUN echo 1.21\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, tt.file)
			if err := os.WriteFile(path, []byte(tt.input), 0640); err != nil {
				t.Fatal(err)
			}
			err := New().Update(dir, model.Job{FilePath: tt.file, TargetType: tt.typ, ImageName: tt.image, TargetVersion: tt.target})
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), tt.want) {
				t.Fatalf("got %q, want content %q", got, tt.want)
			}
		})
	}
}

func TestNoChange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".nvmrc"), []byte("20\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err := New().Update(dir, model.Job{FilePath: ".nvmrc", TargetType: "NODEJS", TargetVersion: "20"})
	if !errors.Is(err, ErrNoChange) {
		t.Fatalf("got %v", err)
	}
}
func TestFileNotFound(t *testing.T) {
	err := New().Update(t.TempDir(), model.Job{FilePath: "missing", TargetType: "GO", TargetVersion: "1.23"})
	if err == nil || !strings.Contains(err.Error(), "resolve target file") {
		t.Fatalf("got %v", err)
	}
}
func TestDockerImageNotFound(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3\n"), 0644)
	err := New().Update(dir, model.Job{FilePath: "Dockerfile", TargetType: "DOCKER_IMAGE", ImageName: "ubuntu", TargetVersion: "24.04"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got %v", err)
	}
}
func TestDockerDigestRejected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine@sha256:abc\n"), 0644)
	err := New().Update(dir, model.Job{FilePath: "Dockerfile", TargetType: "DOCKER_IMAGE", ImageName: "alpine", TargetVersion: "3.22"})
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("got %v", err)
	}
}
func TestPathTraversalRejected(t *testing.T) {
	err := New().Update(t.TempDir(), model.Job{FilePath: "../outside", TargetType: "NODEJS", TargetVersion: "20"})
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("got %v", err)
	}
}
