// Package providers inspects installed tool bundles without invoking their CLIs.
// A provider is deliberately narrower than a general command runner: some CLIs
// rewrite global skills before dispatching even apparently local commands.
package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jdevera/dotagents/internal/skills"
)

type Options struct {
	Home string
	// Path selects an installed executable or package directory. It is never run.
	Path string
}

type Result struct {
	Tool       string      `json:"tool"`
	Version    string      `json:"version"`
	Method     string      `json:"method"`
	SourcePath string      `json:"source_path"`
	Files      skills.Tree `json:"files"`
}

// Probe compares against the installed tool, not an uninstalled remote release.
// Unknown bundle layouts are errors rather than an assertion of no changes.
func Probe(ctx context.Context, name string, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if options.Home == "" {
		var err error
		options.Home, err = os.UserHomeDir()
		if err != nil {
			return Result{}, err
		}
	}
	switch name {
	case "td":
		return probeTD(ctx, options)
	case "jev":
		return probeJev(ctx, options)
	default:
		return Result{}, fmt.Errorf("no read-only provider for %q; generated contents could not be compared", name)
	}
}

func readFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("not a regular file below 4 MiB: %s", path)
	}
	return os.ReadFile(path)
}

func ancestors(path string) []string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		path = filepath.Dir(path)
	}
	var roots []string
	for i := 0; i < 7; i++ {
		roots = append(roots, path)
		next := filepath.Dir(path)
		if next == path {
			break
		}
		path = next
	}
	return roots
}

func executable(name string, options Options) string {
	if options.Path != "" {
		return options.Path
	}
	// A synthetic home must not accidentally probe the operator's real tools.
	actualHome, _ := os.UserHomeDir()
	if filepath.Clean(actualHome) == filepath.Clean(options.Home) {
		path, _ := exec.LookPath(name)
		return path
	}
	return ""
}

func probeTD(ctx context.Context, options Options) (Result, error) {
	var roots []string
	if path := executable("td", options); path != "" {
		roots = ancestors(path)
	}
	if options.Path == "" {
		roots = append(roots,
			filepath.Join(options.Home, ".local/share/mise/installs/npm-doist-todoist-cli/latest/lib/node_modules/@doist/todoist-cli"),
			filepath.Join(options.Home, ".local/lib/node_modules/@doist/todoist-cli"))
	}
	for _, root := range roots {
		data, err := readFile(filepath.Join(root, "package.json"))
		if err != nil {
			continue
		}
		var metadata struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			License string `json:"license"`
		}
		if json.Unmarshal(data, &metadata) != nil || metadata.Name != "@doist/todoist-cli" {
			continue
		}
		if metadata.Version == "" || metadata.License == "" || strings.ContainsAny(metadata.License, "\r\n") {
			return Result{}, errors.New("td bundle has unsupported package metadata")
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		generator, err := readFile(filepath.Join(root, "dist/lib/skills/create-installer.js"))
		if err != nil {
			return Result{}, fmt.Errorf("read td generator: %w", err)
		}
		// Checking only the renderer could miss a changed installer that no
		// longer uses it or postprocesses its output. Pin the reviewed complete
		// installer; content.js remains variable and is parsed without execution.
		if digest(generator) != "d287080a8e757bac8ac6683439722c84ef55326f0c8ea112c2da6a56d42284fa" {
			return Result{}, errors.New("td installer differs from the reviewed 5.12.0 implementation; generated contents cannot be verified")
		}
		content, err := readFile(filepath.Join(root, "dist/lib/skills/content.js"))
		if err != nil {
			return Result{}, fmt.Errorf("read td skill constants: %w", err)
		}
		generated, err := renderTD(string(generator), string(content), metadata.License)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Tool: "td", Version: metadata.Version,
			Method:     "statically evaluated supported td generator; no tool process executed",
			SourcePath: root,
			Files:      skills.Tree{"SKILL.md": skills.File{Data: generated, Mode: 0644}},
		}, nil
	}
	return Result{}, errors.New("td package could not be located; set provider.path to its installed package directory")
}

func probeJev(ctx context.Context, options Options) (Result, error) {
	var candidates []string
	if path := executable("jev", options); path != "" {
		for _, root := range ancestors(path) {
			candidates = append(candidates, root)
			for _, pattern := range []string{"lib/python*/site-packages/jev_cli", "lib64/python*/site-packages/jev_cli"} {
				matches, _ := filepath.Glob(filepath.Join(root, pattern))
				candidates = append(candidates, matches...)
			}
		}
	}
	if options.Path == "" {
		for _, base := range []string{".local/share/uv/tools/jev-cli", ".local/share/pipx/venvs/jev-cli", ".local/pipx/venvs/jev-cli"} {
			matches, _ := filepath.Glob(filepath.Join(options.Home, base, "lib/python*/site-packages/jev_cli"))
			candidates = append(candidates, matches...)
		}
	}
	seen := map[string]bool{}
	for _, root := range candidates {
		if seen[root] {
			continue
		}
		seen[root] = true
		bundle := filepath.Join(root, "bundled_skills/jev-cli")
		if _, err := os.Stat(filepath.Join(bundle, "SKILL.md")); err != nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		version, err := jevVersion(root)
		if err != nil {
			return Result{}, err
		}
		source, err := readFile(filepath.Join(root, "__init__.py"))
		if err != nil {
			return Result{}, fmt.Errorf("read jev installer: %w", err)
		}
		if err := verifyJevInstaller(string(source)); err != nil {
			return Result{}, err
		}
		files, err := skills.ReadTree(bundle)
		if err != nil {
			return Result{}, fmt.Errorf("read jev bundle: %w", err)
		}
		files[".jev-cli-managed"] = skills.File{Data: []byte("managed by jev install-skills\n"), Mode: 0644}
		return Result{Tool: "jev", Version: version, Method: "bundled skill files plus installer ownership marker; no tool process executed", SourcePath: root, Files: files}, nil
	}
	return Result{}, errors.New("jev bundle could not be located; set provider.path to its installed jev_cli package directory")
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func verifyJevInstaller(source string) error {
	for name, assignment := range map[string]string{
		"INSTALL_MARKER": `INSTALL_MARKER = ".jev-cli-managed"`,
		"BUNDLED_SKILLS": `BUNDLED_SKILLS = Path(__file__).with_name("bundled_skills")`,
	} {
		matches := regexp.MustCompile(`(?m)^`+name+`\s*=.*$`).FindAllString(source, -1)
		if len(matches) != 1 || matches[0] != assignment {
			return errors.New("jev installer paths or marker changed; bundled output cannot be verified")
		}
	}
	const prefix = "def install_skills("
	if strings.Count(source, prefix) != 1 {
		return errors.New("jev installer function is missing or ambiguous")
	}
	start := strings.Index(source, prefix)
	end := strings.Index(source[start:], "\ndef ")
	body := source[start:]
	if end >= 0 {
		body = body[:end]
	}
	body = strings.TrimSpace(body) + "\n"
	// Complete reviewed 0.7.0 function, including copy and publication. Added
	// postprocessing must never masquerade as an unchanged bundled skill.
	if digest([]byte(body)) != "147823e06b658610380842d1b5d24600e242feaf7876a0216ad626663e4d7025" {
		return errors.New("jev install_skills differs from the reviewed 0.7.0 implementation; generated contents cannot be verified")
	}
	return nil
}

func jevVersion(root string) (string, error) {
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(root), "jev_cli-*.dist-info/METADATA"))
	sort.Strings(paths)
	if len(paths) != 1 {
		return "", errors.New("jev version metadata is missing or ambiguous")
	}
	data, err := readFile(paths[0])
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Version: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Version: ")), nil
		}
	}
	return "", errors.New("jev metadata has no version")
}
