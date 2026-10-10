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
	AdminPath   string `yaml:"admin_path" json:"admin_path"`
	Homepage    string `yaml:"homepage" json:"homepage"`
	Notes       string `yaml:"notes" json:"notes,omitempty"` // shown after install
	Updatable   bool   `json:"updatable"`

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
		if a.ID == "" || len(a.Install) == 0 {
			panic("catalog " + n + ": id and install are required")
		}
		a.Updatable = len(a.Update) > 0
		apps[a.ID] = a
	}
}

func List() []*App {
	out := make([]*App, 0, len(apps))
	for _, a := range apps {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func Get(id string) (*App, bool) {
	a, ok := apps[id]
	return a, ok
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
