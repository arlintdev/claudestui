package config

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProfileInstance defines one instance in a profile.
type ProfileInstance struct {
	Name      string `yaml:"name"`
	Dir       string `yaml:"dir"`
	Task      string `yaml:"task"`
	Dangerous bool   `yaml:"dangerous"`
}

// Profile defines a set of instances to launch together.
type Profile struct {
	Name      string            `yaml:"name"`
	Instances []ProfileInstance `yaml:"instances"`
}

// LoadProfile reads a profile from a YAML file.
func LoadProfile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListProfiles returns names of all profiles in the profile directory.
func ListProfiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			names = append(names, strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml"))
		}
	}
	return names, nil
}

// ProfilePath returns the full path for a profile name.
func ProfilePath(dir, name string) string {
	// Try .yaml first, then .yml
	p := filepath.Join(dir, name+".yaml")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return filepath.Join(dir, name+".yml")
}
