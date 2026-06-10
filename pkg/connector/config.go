package connector

import (
	_ "embed"
	"errors"
	"strings"
	"text/template"

	up "go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"
)

//go:embed example-config.yaml
var ExampleConfig string

type Config struct {
	DisplaynameTemplate string             `yaml:"displayname_template"`
	displaynameTemplate *template.Template `yaml:"-"`

	HomeserverURL string `yaml:"homeserver_url"`

	Sync struct {
		PollTimeoutSeconds  int `yaml:"poll_timeout_seconds"`
		ErrorBackoffSeconds int `yaml:"error_backoff_seconds"`
	} `yaml:"sync"`
}

type umConfig Config

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	if err := node.Decode((*umConfig)(c)); err != nil {
		return err
	}
	return c.PostProcess()
}

func (c *Config) PostProcess() (err error) {
	c.displaynameTemplate, err = template.New("displayname").Parse(c.DisplaynameTemplate)
	return
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "displayname_template")
	helper.Copy(up.Str, "homeserver_url")
	helper.Copy(up.Int, "sync", "poll_timeout_seconds")
	helper.Copy(up.Int, "sync", "error_backoff_seconds")
}

func (rc *RedditConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &rc.Config, up.SimpleUpgrader(upgradeConfig)
}

func (rc *RedditConnector) ValidateConfig() error {
	if strings.TrimSpace(rc.Config.DisplaynameTemplate) == "" {
		return errors.New("displayname_template must not be empty")
	}
	return nil
}

type DisplaynameParams struct {
	Username string
	UserID   string
}

func (c *Config) FormatDisplayname(params DisplaynameParams) string {
	var buf strings.Builder
	_ = c.displaynameTemplate.Execute(&buf, params)
	return buf.String()
}

func (c *Config) PollTimeoutMS() int {
	secs := c.Sync.PollTimeoutSeconds
	if secs <= 0 {
		secs = 30
	}
	return secs * 1000
}

func (c *Config) ErrorBackoffSeconds() int {
	if c.Sync.ErrorBackoffSeconds <= 0 {
		return 5
	}
	return c.Sync.ErrorBackoffSeconds
}
