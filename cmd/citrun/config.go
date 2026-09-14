// cmd/citrun/config.go
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration so YAML accepts "35m" style values.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

type Budgets struct {
	Networks    int                       `yaml:"networks"`
	Subnetworks int                       `yaml:"subnetworks"`
	Regions     map[string]map[string]int `yaml:"regions"`
}

type Shape struct {
	Arch        string `yaml:"arch"`
	CPUs        int    `yaml:"cpus"`
	Family      string `yaml:"family"`
	ZoneSet     string `yaml:"zone_set"`
	Metal       bool   `yaml:"metal"`
	MaxParallel int    `yaml:"max_parallel"`
}

type Suite struct {
	VMs      int      `yaml:"vms"`
	Networks int      `yaml:"networks"`
	Subnets  int      `yaml:"subnets"`
	Timeout  Duration `yaml:"timeout"`
	Est      Duration `yaml:"est"`
}

type MatrixConfig struct {
	Name     string   `yaml:"name"`
	Shapes   []string `yaml:"shapes"`
	Images   []string `yaml:"images"`
	Suites   []string `yaml:"suites"`
	EachZone bool     `yaml:"each_zone"`
}

type SuiteOnlyOnRule struct {
	Suites []string `yaml:"suites"`
	Shapes []string `yaml:"shapes"`
}
type KeepOnlyRule struct {
	Shapes []string `yaml:"shapes"`
	Suites []string `yaml:"suites"`
}
type DropRule struct {
	Shapes []string `yaml:"shapes"`
	Suites []string `yaml:"suites"`
	Images []string `yaml:"images"`
}
type ZoneSetRule struct {
	Shapes []string `yaml:"shapes"`
	Suites []string `yaml:"suites"`
	Set    string   `yaml:"set"`
}
type TimeoutRule struct {
	Shapes []string `yaml:"shapes"`
	Value  Duration `yaml:"value"`
}

type Rule struct {
	SuiteOnlyOn *SuiteOnlyOnRule `yaml:"suite_only_on"`
	KeepOnly    *KeepOnlyRule    `yaml:"keep_only"`
	Drop        *DropRule        `yaml:"drop"`
	ZoneSet     *ZoneSetRule     `yaml:"zone_set"`
	Timeout     *TimeoutRule     `yaml:"timeout"`
}

type QuarantineEntry struct {
	Image  string `yaml:"image"`
	Shape  string `yaml:"shape"`
	Suite  string `yaml:"suite"`
	Reason string `yaml:"reason"`
}

type Family struct {
	Name       string `yaml:"name"`
	Shape      string `yaml:"shape"`
	CPUs       int    `yaml:"cpus"`
	Family     string `yaml:"family"`
	Arch       string `yaml:"arch"`
	ZoneSet    string `yaml:"zone_set"`
	PinnedZone string `yaml:"pinned_zone"`
}

type SVConfig struct {
	Images    []string `yaml:"images"`
	ArmImages []string `yaml:"arm_images"`
	Families  []Family `yaml:"families"`
}

type Config struct {
	Project         string              `yaml:"project"`
	ImagePrefix     string              `yaml:"image_prefix"`
	DockerImage     string              `yaml:"docker_image"`
	ContainerCap    int                 `yaml:"container_cap"`
	SafetyFactor    float64             `yaml:"safety_factor"`
	Budgets         Budgets             `yaml:"budgets"`
	ZoneSets        map[string][]string `yaml:"zone_sets"`
	Shapes          map[string]Shape    `yaml:"shapes"`
	Suites          map[string]Suite    `yaml:"suites"`
	Matrix          []MatrixConfig      `yaml:"matrix"`
	Rules           []Rule              `yaml:"rules"`
	Quarantine      []QuarantineEntry   `yaml:"quarantine"`
	ShapeValidation SVConfig            `yaml:"shapevalidation"`
}

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	// -1 marks "absent"; yaml.v3 only overwrites fields present in the
	// document, so an explicit `safety_factor: 0` survives decode and is
	// rejected by Validate(), while an omitted field is defaulted below.
	cfg.SafetyFactor = -1
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.DockerImage == "" {
		cfg.DockerImage = "cloud-image-tests"
	}
	if cfg.ContainerCap == 0 {
		cfg.ContainerCap = 80
	}
	if cfg.SafetyFactor == -1 {
		cfg.SafetyFactor = 0.8
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func zoneRegion(zone string) string {
	i := strings.LastIndex(zone, "-")
	if i < 0 {
		return zone
	}
	return zone[:i]
}

func (c *Config) Validate() error {
	if c.Project == "" {
		return fmt.Errorf("project is required")
	}
	if c.SafetyFactor <= 0 || c.SafetyFactor > 1 {
		return fmt.Errorf("safety_factor must be in (0,1], got %v", c.SafetyFactor)
	}
	for name, zones := range c.ZoneSets {
		for _, z := range zones {
			if _, ok := c.Budgets.Regions[zoneRegion(z)]; !ok {
				return fmt.Errorf("zone_set %s: zone %s has no budgets for region %s", name, z, zoneRegion(z))
			}
		}
	}
	for name, s := range c.Shapes {
		if _, ok := c.ZoneSets[s.ZoneSet]; !ok {
			return fmt.Errorf("shape %s: unknown zone_set %q", name, s.ZoneSet)
		}
		if s.CPUs <= 0 {
			return fmt.Errorf("shape %s: cpus must be > 0", name)
		}
	}
	for name, s := range c.Suites {
		if s.Timeout <= 0 {
			return fmt.Errorf("suite %s: timeout must be set", name)
		}
	}
	for _, m := range c.Matrix {
		for _, sh := range m.Shapes {
			shape, ok := c.Shapes[sh]
			if !ok {
				return fmt.Errorf("matrix %s: unknown shape %q", m.Name, sh)
			}
			if m.EachZone && len(c.ZoneSets[shape.ZoneSet]) == 0 {
				return fmt.Errorf("matrix %s: each_zone shape %q has no zones in zone_set %q", m.Name, sh, shape.ZoneSet)
			}
		}
		for _, su := range m.Suites {
			if _, ok := c.Suites[su]; !ok {
				return fmt.Errorf("matrix %s: unknown suite %q", m.Name, su)
			}
		}
	}
	for i, r := range c.Rules {
		n := 0
		if r.SuiteOnlyOn != nil {
			n++
			if len(r.SuiteOnlyOn.Suites) == 0 {
				return fmt.Errorf("rule %d: suite_only_on requires non-empty suites", i)
			}
			if len(r.SuiteOnlyOn.Shapes) == 0 {
				return fmt.Errorf("rule %d: suite_only_on requires non-empty shapes", i)
			}
		}
		if r.KeepOnly != nil {
			n++
			if len(r.KeepOnly.Shapes) == 0 {
				return fmt.Errorf("rule %d: keep_only requires non-empty shapes", i)
			}
			if len(r.KeepOnly.Suites) == 0 {
				return fmt.Errorf("rule %d: keep_only requires non-empty suites", i)
			}
		}
		if r.Drop != nil {
			n++
			if len(r.Drop.Shapes) == 0 && len(r.Drop.Suites) == 0 && len(r.Drop.Images) == 0 {
				return fmt.Errorf("rule %d: drop requires non-empty shapes/suites/images", i)
			}
		}
		if r.ZoneSet != nil {
			n++
			if len(r.ZoneSet.Shapes) == 0 {
				return fmt.Errorf("rule %d: zone_set requires non-empty shapes", i)
			}
			if _, ok := c.ZoneSets[r.ZoneSet.Set]; !ok {
				return fmt.Errorf("rule %d: unknown zone_set %q", i, r.ZoneSet.Set)
			}
		}
		if r.Timeout != nil {
			n++
			if len(r.Timeout.Shapes) == 0 {
				return fmt.Errorf("rule %d: timeout requires non-empty shapes", i)
			}
		}
		if n != 1 {
			return fmt.Errorf("rule %d: exactly one action required, got %d", i, n)
		}
	}
	for i, q := range c.Quarantine {
		if q.Image == "" && q.Shape == "" && q.Suite == "" {
			return fmt.Errorf("quarantine %d: needs image, shape, or suite", i)
		}
	}
	for _, f := range c.ShapeValidation.Families {
		if f.PinnedZone == "" && f.ZoneSet == "" {
			return fmt.Errorf("shapevalidation family %s: needs pinned_zone or zone_set", f.Name)
		}
		if f.PinnedZone != "" {
			if _, ok := c.Budgets.Regions[zoneRegion(f.PinnedZone)]; !ok {
				return fmt.Errorf("shapevalidation family %s: pinned zone %s has no budgets", f.Name, f.PinnedZone)
			}
		} else if _, ok := c.ZoneSets[f.ZoneSet]; !ok {
			return fmt.Errorf("shapevalidation family %s: unknown zone_set %q", f.Name, f.ZoneSet)
		}
	}
	return nil
}
