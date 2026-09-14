// cmd/citrun/matrix.go
package main

import (
	"fmt"
	"path"
	"strings"
)

type Job struct {
	ID, Config, Image, BaseImage, Shape, Suite, Family, Arch string
	CPUCost                                                  int
	BudgetKey, Series                                        string
	MaxParallel, Networks, Subnets                           int
	Zones                                                    []string
	Timeout, Est                                             Duration
	Quarantined                                              string
}

type JobFilter struct {
	Configs, Images, Shapes, Suites []string
}

func matchAny(globs []string, s string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, s); err == nil && ok {
			return true
		}
		if g == s {
			return true
		}
	}
	return false
}

func shapeSeries(shape string) string {
	if i := strings.Index(shape, "-"); i > 0 {
		return shape[:i]
	}
	return shape
}

func (f JobFilter) admits(j Job) bool {
	if len(f.Configs) > 0 && !matchAny(f.Configs, j.Config) {
		return false
	}
	if len(f.Images) > 0 && !matchAny(f.Images, j.BaseImage) {
		return false
	}
	if len(f.Shapes) > 0 && !matchAny(f.Shapes, j.Shape) {
		return false
	}
	if len(f.Suites) > 0 && !matchAny(f.Suites, j.Suite) {
		return false
	}
	return true
}

func fullImage(prefix, img string) (full, base string) {
	if strings.Contains(img, "/") {
		parts := strings.Split(img, "/")
		return img, parts[len(parts)-1]
	}
	return prefix + "/" + img, img
}

// keepJob applies suite_only_on / keep_only / drop rules; returns false if dropped.
func keepJob(cfg *Config, j *Job) bool {
	for _, r := range cfg.Rules {
		switch {
		case r.SuiteOnlyOn != nil:
			if matchAny(r.SuiteOnlyOn.Suites, j.Suite) && !matchAny(r.SuiteOnlyOn.Shapes, j.Shape) {
				return false
			}
		case r.KeepOnly != nil:
			if matchAny(r.KeepOnly.Shapes, j.Shape) && !matchAny(r.KeepOnly.Suites, j.Suite) {
				return false
			}
		case r.Drop != nil:
			d := r.Drop
			m := true
			if len(d.Shapes) > 0 && !matchAny(d.Shapes, j.Shape) {
				m = false
			}
			if len(d.Suites) > 0 && !matchAny(d.Suites, j.Suite) {
				m = false
			}
			if len(d.Images) > 0 && !matchAny(d.Images, j.BaseImage) {
				m = false
			}
			if m {
				return false
			}
		case r.ZoneSet != nil:
			z := r.ZoneSet
			if matchAny(z.Shapes, j.Shape) && (len(z.Suites) == 0 || matchAny(z.Suites, j.Suite)) {
				j.Zones = append([]string(nil), cfg.ZoneSets[z.Set]...)
			}
		case r.Timeout != nil:
			if matchAny(r.Timeout.Shapes, j.Shape) {
				j.Timeout = r.Timeout.Value
			}
		}
	}
	return true
}

func applyQuarantine(cfg *Config, j *Job) {
	for _, q := range cfg.Quarantine {
		if q.Image != "" && !matchAny([]string{q.Image}, j.BaseImage) {
			continue
		}
		if q.Shape != "" && !matchAny([]string{q.Shape}, j.Shape) {
			continue
		}
		if q.Suite != "" && !matchAny([]string{q.Suite}, j.Suite) {
			continue
		}
		j.Quarantined = q.Reason
	}
}

func ExpandJobs(cfg *Config, f JobFilter) ([]Job, error) {
	var jobs []Job
	seen := map[string]bool{}
	add := func(j Job) error {
		if seen[j.ID] {
			return fmt.Errorf("duplicate job id %s", j.ID)
		}
		seen[j.ID] = true
		if f.admits(j) {
			jobs = append(jobs, j)
		}
		return nil
	}

	for _, m := range cfg.Matrix {
		for _, shapeName := range m.Shapes {
			sh := cfg.Shapes[shapeName]
			for _, img := range m.Images {
				full, base := fullImage(cfg.ImagePrefix, img)
				for _, suiteName := range m.Suites {
					su := cfg.Suites[suiteName]
					vms := su.VMs
					if vms < 1 {
						vms = 1
					}
					j := Job{
						ID: base + "_" + shapeName + "_" + suiteName, Config: m.Name,
						Image: full, BaseImage: base, Shape: shapeName, Suite: suiteName,
						Arch: sh.Arch, CPUCost: sh.CPUs * vms, BudgetKey: sh.Family,
						Series: shapeSeries(shapeName), MaxParallel: sh.MaxParallel,
						Networks: su.Networks, Subnets: su.Subnets,
						Zones:   append([]string(nil), cfg.ZoneSets[sh.ZoneSet]...),
						Timeout: su.Timeout, Est: su.Est,
					}
					if !keepJob(cfg, &j) {
						continue
					}
					if m.EachZone {
						for _, zone := range j.Zones {
							zonedJob := j
							zonedJob.ID += "_" + zone
							zonedJob.Zones = []string{zone}
							applyQuarantine(cfg, &zonedJob)
							if err := add(zonedJob); err != nil {
								return nil, err
							}
						}
					} else {
						applyQuarantine(cfg, &j)
						if err := add(j); err != nil {
							return nil, err
						}
					}
				}
			}
		}
	}

	sv := cfg.ShapeValidation
	svSuite, hasSV := cfg.Suites["shapevalidation"]
	if len(sv.Families) > 0 && !hasSV {
		return nil, fmt.Errorf("shapevalidation families configured but no shapevalidation suite entry")
	}
	for _, fam := range sv.Families {
		images := sv.Images
		if fam.Arch == "arm64" {
			images = sv.ArmImages
		}
		for _, img := range images {
			full, base := fullImage(cfg.ImagePrefix, img)
			zones := []string{fam.PinnedZone}
			if fam.PinnedZone == "" {
				zones = append([]string(nil), cfg.ZoneSets[fam.ZoneSet]...)
			}
			j := Job{
				ID: base + "_" + fam.Shape + "_shapevalidation", Config: "shapevalidation",
				Image: full, BaseImage: base, Shape: fam.Shape, Suite: "shapevalidation",
				Family: fam.Name, Arch: fam.Arch, CPUCost: fam.CPUs, BudgetKey: fam.Family,
				Series: shapeSeries(fam.Shape), Zones: zones,
				Timeout: svSuite.Timeout, Est: svSuite.Est,
			}
			applyQuarantine(cfg, &j)
			if err := add(j); err != nil {
				return nil, err
			}
		}
	}
	return jobs, nil
}
