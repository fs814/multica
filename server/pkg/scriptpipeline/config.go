// Package scriptpipeline describes deterministic clone/build/run execution.
package scriptpipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const Capability = "workflow_script_pipeline_v1"

var Stages = []string{"clone", "build", "run"}
var InputKeys = []string{"script_directory", "script_platform", "script_steps", "clone_script", "build_script", "run_script", "script_timeout_seconds"}

type Config struct {
	Directory      string            `json:"directory"`
	Platform       string            `json:"platform"`
	Steps          []string          `json:"steps"`
	Scripts        map[string]string `json:"scripts,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type Step struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func Resolve(defaults *Config, raw []byte) (*Config, error) {
	c := Config{Platform: "auto", Steps: append([]string(nil), Stages...), Scripts: map[string]string{}, TimeoutSeconds: 3600}
	if defaults != nil {
		c = *defaults
		c.Steps = append([]string(nil), defaults.Steps...)
		c.Scripts = map[string]string{}
		for k, v := range defaults.Scripts {
			c.Scripts[k] = v
		}
	}
	values := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("script pipeline input must contain string values")
		}
	}
	if v, ok := values["script_directory"]; ok {
		c.Directory = v
	}
	if v, ok := values["script_platform"]; ok {
		c.Platform = v
	}
	if v, ok := values["script_steps"]; ok {
		if err := json.Unmarshal([]byte(v), &c.Steps); err != nil {
			return nil, fmt.Errorf("script_steps must be a JSON array")
		}
	}
	for _, stage := range Stages {
		if v, ok := values[stage+"_script"]; ok {
			c.Scripts[stage] = v
		}
	}
	if v, ok := values["script_timeout_seconds"]; ok {
		if err := json.Unmarshal([]byte(v), &c.TimeoutSeconds); err != nil {
			return nil, fmt.Errorf("script timeout must be an integer")
		}
	}
	if c.Platform == "" {
		c.Platform = "auto"
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 3600
	}
	c.Directory = strings.TrimSpace(c.Directory)
	return &c, c.Validate()
}

func (c *Config) Validate() error {
	if c == nil || c.Directory == "" || len(c.Directory) > 4096 || strings.ContainsAny(c.Directory, "\x00\r\n") {
		return fmt.Errorf("script directory is required and must be a valid path")
	}
	if c.Platform != "auto" && c.Platform != "windows" && c.Platform != "darwin" && c.Platform != "linux" {
		return fmt.Errorf("script platform must be auto, windows, darwin or linux")
	}
	if len(c.Steps) == 0 || len(c.Steps) > 3 {
		return fmt.Errorf("select at least one of clone, build or run")
	}
	selected := map[string]bool{}
	for _, s := range c.Steps {
		if (s != "clone" && s != "build" && s != "run") || selected[s] {
			return fmt.Errorf("invalid or duplicate script step %q", s)
		}
		selected[s] = true
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 86400 {
		return fmt.Errorf("script timeout must be between 1 and 86400 seconds")
	}
	for k, v := range c.Scripts {
		if k != "clone" && k != "build" && k != "run" {
			return fmt.Errorf("unknown script step %q", k)
		}
		if len(v) > 4096 || strings.ContainsAny(v, "\x00\r\n") {
			return fmt.Errorf("invalid %s script path", k)
		}
	}
	return nil
}

func (c *Config) TargetPlatform() string {
	if c.Platform != "" && c.Platform != "auto" {
		return c.Platform
	}
	for _, part := range strings.Split(strings.ReplaceAll(c.Directory, "\\", "/"), "/") {
		switch strings.ToLower(part) {
		case "winbuild":
			return "windows"
		case "macbuild":
			return "darwin"
		case "linuxbuild":
			return "linux"
		}
	}
	return "auto"
}

// Plan checks every selected script before running any of them. No shell text
// is constructed from input paths. Ambiguous discoveries require explicit paths.
func (c *Config) Plan(goos string) (string, []Step, error) {
	if err := c.Validate(); err != nil {
		return "", nil, err
	}
	if target := c.TargetPlatform(); target != "auto" && target != goos {
		return "", nil, fmt.Errorf("pipeline requires %s; selected runtime runs %s", target, goos)
	}
	if !filepath.IsAbs(c.Directory) {
		return "", nil, fmt.Errorf("use an absolute script directory on the execution machine")
	}
	dir, err := filepath.EvalSymlinks(c.Directory)
	if err != nil {
		return "", nil, fmt.Errorf("script directory unavailable: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("script directory is not a directory")
	}
	ext := ".sh"
	if goos == "windows" {
		ext = ".ps1"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	selected := map[string]bool{}
	for _, s := range c.Steps {
		selected[s] = true
	}
	var plan []Step
	for _, stage := range Stages {
		if !selected[stage] {
			continue
		}
		name := strings.TrimSpace(c.Scripts[stage])
		if name == "" {
			stem := filepath.Base(dir)
			preferred := map[string]string{"clone": "build_" + stem + "_clone" + ext, "build": "build_" + stem + ext, "run": "run_" + stem + ext}[stage]
			var candidates []string
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				n := e.Name()
				low := strings.ToLower(n)
				if !strings.HasSuffix(low, ext) {
					continue
				}
				base := strings.TrimSuffix(low, ext)
				matches := false
				switch stage {
				case "clone":
					matches = base == "clone" || strings.HasPrefix(base, "clone_") || strings.HasSuffix(base, "_clone")
				case "build":
					matches = (base == "build" || strings.HasPrefix(base, "build_")) && !strings.HasSuffix(base, "_clone")
				case "run":
					matches = base == "run" || strings.HasPrefix(base, "run_")
				}
				if matches {
					candidates = append(candidates, n)
				}
			}
			for _, candidate := range candidates {
				if candidate == preferred {
					name = candidate
					break
				}
			}
			if name == "" {
				if len(candidates) != 1 {
					return "", nil, fmt.Errorf("%s: found %d matching scripts; specify its script path", stage, len(candidates))
				}
				name = candidates[0]
			}
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		if !strings.EqualFold(filepath.Ext(name), ext) {
			return "", nil, fmt.Errorf("%s script must end with %s on %s", stage, ext, goos)
		}
		resolved, err := filepath.EvalSymlinks(name)
		if err != nil {
			return "", nil, fmt.Errorf("%s script unavailable: %w", stage, err)
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("%s script is not a regular file", stage)
		}
		plan = append(plan, Step{Name: stage, Path: resolved})
	}
	return dir, plan, nil
}
