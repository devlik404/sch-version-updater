package updater

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sch-version-updater/internal/model"
)

var ErrNoChange = errors.New("target version already applied")

type Service interface {
	Update(repoRoot string, job model.Job) error
}
type FileUpdater struct{}

func New() *FileUpdater { return &FileUpdater{} }

func (u *FileUpdater) Update(repoRoot string, job model.Job) error {
	path, err := safeTarget(repoRoot, job.FilePath)
	if err != nil {
		return err
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read target file %q: %w", job.FilePath, err)
	}
	var updated []byte
	switch strings.ToUpper(job.TargetType) {
	case "GO":
		updated, err = updateGo(original, job.TargetVersion)
	case "JAVA":
		updated, err = updateJava(original, job.TargetVersion)
	case "NODEJS":
		updated, err = updateNode(original, job.TargetVersion)
	case "DOCKER_IMAGE":
		updated, err = updateDocker(original, job.ImageName, job.TargetVersion)
	default:
		err = fmt.Errorf("unsupported target_type %q", job.TargetType)
	}
	if err != nil {
		return err
	}
	if bytes.Equal(original, updated) {
		return ErrNoChange
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write target file: %w", err)
	}
	return validate(job.TargetType, updated)
}

func safeTarget(root, configured string) (string, error) {
	if configured == "" || filepath.IsAbs(configured) {
		return "", fmt.Errorf("file_path must be a non-empty relative path")
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	cleanRoot, err = filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	target := filepath.Join(cleanRoot, filepath.Clean(configured))
	rel, err := filepath.Rel(cleanRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file_path escapes repository: %q", configured)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve target file %q: %w", configured, err)
	}
	rel, err = filepath.Rel(cleanRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("target symlink escapes repository")
	}
	return resolved, nil
}

var goVersion = regexp.MustCompile(`(?m)^(go[ \t]+)([0-9]+\.[0-9]+(?:\.[0-9]+)?)([ \t]*(?:\r)?$)`)

func updateGo(in []byte, version string) ([]byte, error) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?$`).MatchString(version) {
		return nil, fmt.Errorf("invalid Go version %q", version)
	}
	if !goVersion.Match(in) {
		return nil, errors.New("go directive not found")
	}
	return goVersion.ReplaceAll(in, []byte("${1}"+version+"${3}")), nil
}

func updateJava(in []byte, version string) ([]byte, error) {
	if strings.TrimSpace(version) == "" || strings.ContainsAny(version, "<>&") {
		return nil, fmt.Errorf("invalid Java version %q", version)
	}
	keys := []string{"java.version", "maven.compiler.release"}
	out := append([]byte(nil), in...)
	found := false
	for _, key := range keys {
		re := regexp.MustCompile(`(?s)(<` + regexp.QuoteMeta(key) + `>\s*)([^<]*?)(\s*</` + regexp.QuoteMeta(key) + `>)`)
		if re.Match(out) {
			found = true
			out = re.ReplaceAll(out, []byte("${1}"+version+"${3}"))
		}
	}
	if !found {
		return nil, errors.New("supported Java property not found (java.version or maven.compiler.release)")
	}
	return out, nil
}

func updateNode(in []byte, version string) ([]byte, error) {
	v := strings.TrimSpace(version)
	if !regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+){0,2}(?:[-+][0-9A-Za-z.-]+)?$`).MatchString(v) {
		return nil, fmt.Errorf("invalid Node.js version %q", version)
	}
	lineEnding := ""
	if bytes.HasSuffix(in, []byte("\r\n")) {
		lineEnding = "\r\n"
	} else if bytes.HasSuffix(in, []byte("\n")) {
		lineEnding = "\n"
	}
	return []byte(v + lineEnding), nil
}

var fromLine = regexp.MustCompile(`(?mi)^(\s*FROM(?:\s+--platform=\S+)?\s+)(\S+)(.*)$`)

func updateDocker(in []byte, imageName, version string) ([]byte, error) {
	if imageName == "" {
		return nil, errors.New("image_name is required for DOCKER_IMAGE")
	}
	if strings.ContainsAny(version, " \t\r\n@") || version == "" {
		return nil, fmt.Errorf("invalid Docker tag %q", version)
	}
	for _, parts := range fromLine.FindAllSubmatch(in, -1) {
		ref := string(parts[2])
		if dockerImageName(ref) == imageName && strings.Contains(ref, "@") {
			return nil, fmt.Errorf("Docker image %q uses a digest; digest updates are not supported", imageName)
		}
	}
	found := false
	out := fromLine.ReplaceAllFunc(in, func(line []byte) []byte {
		parts := fromLine.FindSubmatch(line)
		ref := string(parts[2])
		name := dockerImageName(ref)
		if name != imageName {
			return line
		}
		found = true
		return append(append(append([]byte{}, parts[1]...), []byte(name+":"+version)...), parts[3]...)
	})
	if !found {
		return nil, fmt.Errorf("Docker image %q not found", imageName)
	}
	return out, nil
}
func dockerImageName(ref string) string {
	ref = strings.SplitN(ref, "@", 2)[0]
	lastSlash, lastColon := strings.LastIndex(ref, "/"), strings.LastIndex(ref, ":")
	if lastColon > lastSlash {
		return ref[:lastColon]
	}
	return ref
}
func validate(targetType string, content []byte) error {
	switch strings.ToUpper(targetType) {
	case "GO":
		if !goVersion.Match(content) {
			return errors.New("updated go.mod is invalid")
		}
	case "JAVA":
		decoder := xml.NewDecoder(bytes.NewReader(content))
		for {
			if _, err := decoder.Token(); err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("updated XML is invalid: %w", err)
			}
		}
	case "NODEJS":
		if strings.ContainsAny(string(content), " \t") {
			return errors.New("updated Node version file is invalid")
		}
	case "DOCKER_IMAGE":
		if !fromLine.Match(content) {
			return errors.New("updated Dockerfile has no FROM instruction")
		}
	}
	return nil
}
