// Package catalog holds the one-click app templates (apps/*.yaml).
//
// A template describes how to install an app into a PHP site with typed
// steps that the agent executes (fetch with a pinned checksum, wp-cli, php
// CLI, write file, remove). Templates never contain shell commands.
package catalog

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed apps/*.yaml
var files embed.FS

type App struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Category    string `yaml:"category" json:"category"`
	Description string `yaml:"description" json:"description"`
	Version     string `yaml:"version" json:"version"` // pinned version, or "latest" when a tool resolves it
	PHPMin      string `yaml:"php_min" json:"php_min"`
	PHPMax      string `yaml:"php_max" json:"php_max,omitempty"`
	Database    bool   `yaml:"database" json:"database"`
	Admin       bool   `yaml:"admin" json:"admin"` // asks for an admin user/email and generates a password
	// Login says what the admin signs in with: "" (user name), "email" or
	// "password" (only a generated password, no user name or email asked).
	Login     string `yaml:"login" json:"login,omitempty"`
	AdminPath string `yaml:"admin_path" json:"admin_path"`
	Homepage  string `yaml:"homepage" json:"homepage"`
	Notes     string `yaml:"notes" json:"notes,omitempty"` // shown after install
	Updatable bool   `json:"updatable"`
	// Wizard apps are pinned by "Refresh catalog" and finished in the app's
	// own web installer at AdminPath.
	Wizard bool `json:"wizard,omitempty"`

	// Kind is "php" (default: steps run in a PHP site) or "compose" (a Docker
	// compose app behind an nginx proxy site).
	Kind        string `yaml:"kind" json:"kind"`
	Compose     string `yaml:"compose" json:"-"`
	NeedsDocker bool   `yaml:"needs_docker" json:"needs_docker"`
	MinRAMMB    int    `yaml:"min_ram_mb" json:"min_ram_mb,omitempty"`
	// Protect turns on password protection for the site by default
	// (database tools reach every database on the server).
	Protect bool `yaml:"protect" json:"protect"`

	Install []map[string]any `yaml:"install" json:"-"`
	Update  []map[string]any `yaml:"update" json:"-"`
	Ver     []map[string]any `yaml:"version_check" json:"-"`
}

var apps map[string]*App

func init() {
	apps = map[string]*App{}
	names, _ := fs.Glob(files, "apps/*.yaml")
	for _, n := range names {
		b, _ := files.ReadFile(n)
		a := &App{}
		if err := yaml.Unmarshal(b, a); err != nil {
			panic(fmt.Sprintf("catalog %s: %v", n, err))
		}
		if a.Kind == "" {
			a.Kind = "php"
		}
		if a.ID == "" || (a.Kind == "php" && len(a.Install) == 0) || (a.Kind == "compose" && a.Compose == "") {
			panic("catalog " + n + ": id and install (php) or compose (compose) are required")
		}
		if err := checkComposeQuoting(a.Compose); err != nil {
			panic("catalog " + n + ": " + err.Error())
		}
		switch a.Login {
		case "", "email", "password":
		default:
			panic("catalog " + n + ": login must be empty, email or password")
		}
		a.Updatable = len(a.Update) > 0
		apps[a.ID] = a
	}
	loadSources()
}

var (
	freeTextVar   = regexp.MustCompile(`\$\{(title|admin_user|admin_email|admin_password)\}`)
	quotedFreeVar = regexp.MustCompile(`"[^"\n]*\$\{(title|admin_user|admin_email|admin_password)\}[^"\n]*"`)
)

// checkComposeQuoting makes sure form-supplied values only land inside
// double-quoted YAML strings, where the panel escapes them.
func checkComposeQuoting(compose string) error {
	quoted := 0
	for _, m := range quotedFreeVar.FindAllString(compose, -1) {
		quoted += len(freeTextVar.FindAllString(m, -1))
	}
	if n := len(freeTextVar.FindAllString(compose, -1)); n != quoted {
		return fmt.Errorf("${title}/${admin_*} must be inside double quotes in compose")
	}
	return nil
}

// List returns the built-in apps plus every refreshed app that has a pinned
// release.
func List() []*App {
	out := make([]*App, 0, len(apps)+len(sources))
	for _, a := range apps {
		out = append(out, a)
	}
	pinnedMu.RLock()
	for _, s := range sources {
		if p := pinnedMap[s.ID]; p != nil && p.SHA256 != "" {
			out = append(out, sourceApp(s, p))
		}
	}
	pinnedMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func Get(id string) (*App, bool) {
	if a, ok := apps[id]; ok {
		return a, true
	}
	pinnedMu.RLock()
	defer pinnedMu.RUnlock()
	for _, s := range sources {
		if p := pinnedMap[s.ID]; s.ID == id && p != nil && p.SHA256 != "" {
			return sourceApp(s, p), true
		}
	}
	return nil, false
}

// StepsJSON renders a step list as the JSON the agent expects.
func StepsJSON(steps []map[string]any) string {
	if len(steps) == 0 {
		return ""
	}
	b, err := json.Marshal(steps)
	if err != nil {
		panic(err) // templates are validated at start-up
	}
	return string(b)
}
