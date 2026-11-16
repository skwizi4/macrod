package config

import (
	"errors"
	"fmt"
	"strings"
)

// EnvSiteSSHKey is the private deploy key with write access to site.repo
// only (a key of its own: the exchange key stays on the private repository).
const EnvSiteSSHKey = "SITE_SSH_KEY_PATH"

// Site publishes the demo dashboard (site --demo: only open data, D24) to a
// public repository served by GitHub Pages after each weekly export.
type Site struct {
	Enabled       bool   `yaml:"enabled"`
	Repo          string `yaml:"repo"`           // owner/name on GitHub
	Branch        string `yaml:"branch"`         // default main
	Remote        string `yaml:"remote"`         // default git@github.com:<repo>.git
	URL           string `yaml:"url"`            // the public address; default https://<owner>.github.io/<name>/
	Dir           string `yaml:"dir"`            // working tree; default ${data_dir}/site
	CommitMessage string `yaml:"commit_message"` // {iso_week}; default "site {iso_week}"
}

const defaultSiteCommit = "site {iso_week}"

func (c *Config) validateSite() error {
	s := &c.Site
	if s.Branch == "" {
		s.Branch = defaultBranch
	}
	if s.CommitMessage == "" {
		s.CommitMessage = defaultSiteCommit
	}
	if s.Dir == "" {
		s.Dir = "${data_dir}/site"
	}
	s.Dir = c.expand(s.Dir)
	if !s.Enabled {
		return nil
	}
	var errs []error
	if !repoRe.MatchString(s.Repo) {
		errs = append(errs, fmt.Errorf("site.repo %q: want owner/name", s.Repo))
	} else {
		if s.Remote == "" {
			s.Remote = "git@github.com:" + s.Repo + ".git"
		}
		if s.URL == "" {
			owner, name, _ := strings.Cut(s.Repo, "/")
			s.URL = "https://" + strings.ToLower(owner) + ".github.io/" + name + "/"
		}
	}
	if c.Git.Enabled && s.Remote == c.Git.Remote {
		errs = append(errs, errors.New("site.repo is git.repo: the public site needs a repository of its own"))
	}
	if s.URL != "" {
		errs = append(errs, checkURL("site.url", s.URL))
	}
	if len(c.Dashboard.DemoAllow) == 0 {
		errs = append(errs, errors.New("site.enabled: dashboard.demo_allow is empty, the public site would have no series"))
	}
	return errors.Join(errs...)
}

// Message fills the site commit message template.
func (s Site) Message(isoWeek string) string {
	return strings.ReplaceAll(s.CommitMessage, "{iso_week}", isoWeek)
}
