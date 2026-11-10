package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

type Condition struct {
	OS        []string          `json:"os,omitempty"`
	Arch      []string          `json:"arch,omitempty"`
	Hostnames []string          `json:"hostnames,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Exists    []string          `json:"exists,omitempty"`
	All       []Condition       `json:"all,omitempty"`
	Any       []Condition       `json:"any,omitempty"`
	Not       *Condition        `json:"not,omitempty"`
}

type Skill struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Owner        string     `json:"owner"`
	Source       string     `json:"source,omitempty"`
	Locations    []string   `json:"locations"`
	Groups       []string   `json:"groups,omitempty"`
	When         *Condition `json:"when,omitempty"`
	Snapshot     string     `json:"snapshot"`
	ProviderPath string     `json:"provider_path,omitempty"`
	Topics       []string   `json:"topics,omitempty"`
	Scope        string     `json:"scope,omitempty"`
}

type Ignore struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type MCPRule struct {
	Name        string         `json:"name"`
	Client      string         `json:"client"`
	Fingerprint string         `json:"fingerprint"`
	Groups      []string       `json:"groups,omitempty"`
	Definition  map[string]any `json:"definition,omitempty"`
	Template    map[string]any `json:"template,omitempty"`
}

type Config struct {
	Version int                  `json:"version"`
	Scope   string               `json:"scope,omitempty"`
	Groups  map[string]Condition `json:"groups"`
	Roots   []string             `json:"roots,omitempty"`
	Skills  []Skill              `json:"skills"`
	Ignore  []Ignore             `json:"ignore"`
	MCP     []MCPRule            `json:"mcp"`
}

type Machine struct {
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Groups   []string `json:"groups"`
	Home     string   `json:"-"`
}

type App struct {
	Repo         string
	Home         string
	Config       Config
	Machine      Machine
	configDigest string
	Only         []string
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func Open(repo, home string) (*App, error) {
	var err error
	repo, err = filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	if home == "" {
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(repo, "dotagents.json"))
	if err != nil {
		return nil, fmt.Errorf("open catalog: %w (use --repo to select your dotagents repository)", err)
	}
	var cfg Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("catalog contains trailing data")
	}
	if err = cfg.Validate(); err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	a := &App{Repo: repo, Home: home, Config: cfg, Machine: Machine{Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Home: home, Groups: []string{}}}
	if cfg.Scope == "project" {
		machineHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		a.Machine.Home = machineHome
	}
	digest := sha256.Sum256(b)
	a.configDigest = hex.EncodeToString(digest[:])
	for name, condition := range cfg.Groups {
		if condition.Match(a.Machine) {
			a.Machine.Groups = append(a.Machine.Groups, name)
		}
	}
	sort.Strings(a.Machine.Groups)
	return a, nil
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported catalog version %d", c.Version)
	}
	if c.Scope != "" && c.Scope != "home" && c.Scope != "project" {
		return fmt.Errorf("invalid catalog scope %q", c.Scope)
	}
	for name, condition := range c.Groups {
		if !identifier.MatchString(name) {
			return fmt.Errorf("invalid group %q", name)
		}
		if err := condition.Validate(0); err != nil {
			return fmt.Errorf("group %s: %w", name, err)
		}
	}
	seen := map[string]bool{}
	locations := map[string]string{}
	for _, s := range c.Skills {
		if s.Scope != "" && s.Scope != "home" && s.Scope != "project" {
			return fmt.Errorf("invalid skill scope %q", s.Scope)
		}
		for _, topic := range s.Topics {
			if !identifier.MatchString(topic) {
				return fmt.Errorf("invalid topic %q", topic)
			}
		}
		if !identifier.MatchString(s.ID) || !identifier.MatchString(s.Name) {
			return fmt.Errorf("invalid skill ID/name %q/%q", s.ID, s.Name)
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate skill ID %s", s.ID)
		}
		seen[s.ID] = true
		switch s.Owner {
		case "manual", "skillshare", "td", "jev", "native":
		default:
			if !strings.HasPrefix(s.Owner, "tool:") || !identifier.MatchString(strings.TrimPrefix(s.Owner, "tool:")) {
				return fmt.Errorf("skill %s: unsupported owner %q (use tool:NAME for another tool)", s.ID, s.Owner)
			}
		}
		if len(s.Locations) == 0 {
			return fmt.Errorf("skill %s has no locations", s.ID)
		}
		for _, path := range s.Locations {
			if err := validateLocation(path); err != nil {
				return fmt.Errorf("skill %s: %w", s.ID, err)
			}
			if old := locations[path]; old != "" {
				return fmt.Errorf("location %s claimed by both %s and %s", path, old, s.ID)
			}
			for other, owner := range locations {
				if strings.HasPrefix(path, other+string(filepath.Separator)) || strings.HasPrefix(other, path+string(filepath.Separator)) {
					return fmt.Errorf("overlapping skill locations for %s and %s: %s and %s", s.ID, owner, path, other)
				}
			}
			locations[path] = s.ID
		}
		if err := c.validateGroups(s.Groups); err != nil {
			return err
		}
		if s.When != nil {
			if err := s.When.Validate(0); err != nil {
				return err
			}
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s.Snapshot) {
			return fmt.Errorf("skill %s has invalid snapshot", s.ID)
		}
	}
	for _, ignore := range c.Ignore {
		if err := validateLocation(ignore.Path); err != nil {
			return err
		}
		if strings.TrimSpace(ignore.Reason) == "" {
			return errors.New("ignored paths need a reason")
		}
	}
	seenMCP := map[string]bool{}
	for _, m := range c.MCP {
		key := m.Client + ":" + m.Name
		if seenMCP[key] {
			return fmt.Errorf("duplicate MCP %s", key)
		}
		seenMCP[key] = true
		if m.Name == "" || m.Client == "" || (m.Fingerprint == "" && m.Template == nil) {
			return errors.New("MCP rules need name, client and fingerprint")
		}
		if m.Template != nil {
			if err := validatePortableMCP(m.Template); err != nil {
				return fmt.Errorf("MCP %s: %w", key, err)
			}
		}
		if err := c.validateGroups(m.Groups); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateGroups(groups []string) error {
	for _, name := range groups {
		if _, ok := c.Groups[name]; !ok {
			return fmt.Errorf("unknown group %q", name)
		}
	}
	return nil
}

func validateLocation(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || path == ".." {
		return fmt.Errorf("location must be a clean path relative to home: %q", path)
	}
	if !strings.Contains(filepath.ToSlash(path), "/skills/") {
		return fmt.Errorf("skill location must be inside a skills directory: %q", path)
	}
	return nil
}

func (c Condition) Validate(depth int) error {
	if depth > 16 {
		return errors.New("condition nesting exceeds 16")
	}
	for _, glob := range c.Hostnames {
		if _, err := filepath.Match(glob, "test"); err != nil {
			return fmt.Errorf("hostname pattern: %w", err)
		}
	}
	for _, sub := range append(append([]Condition{}, c.All...), c.Any...) {
		if err := sub.Validate(depth + 1); err != nil {
			return err
		}
	}
	if c.Not != nil {
		return c.Not.Validate(depth + 1)
	}
	return nil
}

func contains(values []string, v string) bool {
	for _, s := range values {
		if s == v {
			return true
		}
	}
	return false
}

func (c Condition) Match(m Machine) bool {
	if len(c.OS) > 0 && !contains(c.OS, m.OS) {
		return false
	}
	if len(c.Arch) > 0 && !contains(c.Arch, m.Arch) {
		return false
	}
	if len(c.Hostnames) > 0 {
		match := false
		for _, g := range c.Hostnames {
			ok, _ := filepath.Match(g, m.Hostname)
			match = match || ok
		}
		if !match {
			return false
		}
	}
	for key, expected := range c.Env {
		value, ok := os.LookupEnv(key)
		if !ok || value != expected {
			return false
		}
	}
	for _, path := range c.Exists {
		if _, err := os.Stat(expandHome(m.Home, path)); err != nil {
			return false
		}
	}
	for _, sub := range c.All {
		if !sub.Match(m) {
			return false
		}
	}
	if len(c.Any) > 0 {
		match := false
		for _, sub := range c.Any {
			match = match || sub.Match(m)
		}
		if !match {
			return false
		}
	}
	return c.Not == nil || !c.Not.Match(m)
}

func expandHome(home, path string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return filepath.Join(home, path)
	}
	return path
}

func (a *App) wanted(groups []string, when *Condition) bool {
	if when != nil && !when.Match(a.Machine) {
		return false
	}
	if len(groups) == 0 {
		return true
	}
	for _, group := range groups {
		if contains(a.Machine.Groups, group) {
			return true
		}
	}
	return false
}

func (a *App) skillWanted(s Skill) bool {
	if s.Scope == "project" && a.Config.Scope != "project" {
		return false
	}
	return a.wanted(s.Groups, s.When)
}

// Reload retains the machine being inspected while rereading its desired state.
// A project's target directory is not the machine's home directory.
func (a *App) Reload() (*App, error) {
	fresh, err := Open(a.Repo, a.Home)
	if err != nil {
		return nil, err
	}
	fresh.Machine = a.Machine
	fresh.Machine.Groups = []string{}
	for name, condition := range fresh.Config.Groups {
		if condition.Match(fresh.Machine) {
			fresh.Machine.Groups = append(fresh.Machine.Groups, name)
		}
	}
	sort.Strings(fresh.Machine.Groups)
	fresh.Only = append([]string{}, a.Only...)
	return fresh, nil
}

func (a *App) Save() error {
	if err := a.Config.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a.Config, "", "  ")
	if err != nil {
		return err
	}
	state := filepath.Join(a.Repo, ".dotagents")
	if err = os.MkdirAll(state, 0700); err != nil {
		return err
	}
	lock := filepath.Join(state, "catalog.lock")
	if err = os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("catalog is locked by another writer: %w", err)
	}
	defer os.Remove(lock)
	path := filepath.Join(a.Repo, "dotagents.json")
	if a.configDigest != "" {
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(current)
		if hex.EncodeToString(digest[:]) != a.configDigest {
			return errors.New("catalog changed since it was loaded; rerun the command")
		}
	}
	b = append(b, '\n')
	if err = atomicWrite(path, b, 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(b)
	a.configDigest = hex.EncodeToString(digest[:])
	return nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dotagents-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
