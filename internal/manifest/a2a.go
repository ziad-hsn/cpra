package manifest

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// InterventionTargetA2A selects an explicitly configured A2A HTTP+JSON agent.
type InterventionTargetA2A struct {
	URL               string        `yaml:"url" json:"url"`
	Token             string        `yaml:"token" json:"token"`
	Tenant            string        `yaml:"tenant" json:"tenant"`
	RequestTimeout    time.Duration `yaml:"request_timeout" json:"request_timeout"`
	TaskTimeout       time.Duration `yaml:"task_timeout" json:"task_timeout"`
	PollInterval      time.Duration `yaml:"poll_interval" json:"poll_interval"`
	AllowInsecureHTTP bool          `yaml:"allow_insecure_http" json:"allow_insecure_http"`
}

func (*InterventionTargetA2A) GetTargetType() string      { return "a2a" }
func (c *InterventionTargetA2A) Copy() InterventionTarget { copy := *c; return &copy }

// Validate performs no I/O and never puts credential values in errors.
// Network policy is enforced by the execution transport.
func (c *InterventionTargetA2A) Validate() error {
	if c == nil || len(c.URL) > 4096 || !utf8.ValidString(c.URL) {
		return errors.New("A2A endpoint is invalid")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("A2A endpoint must be an HTTP URL without credentials, query, or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("A2A endpoint port is invalid")
		}
	}
	if u.Scheme != "https" && !c.AllowInsecureHTTP {
		return errors.New("A2A requires HTTPS; HTTP must be explicitly enabled for a trusted endpoint")
	}
	if len(c.Tenant) > 256 || !utf8.ValidString(c.Tenant) || strings.ContainsFunc(c.Tenant, unicode.IsControl) {
		return errors.New("A2A tenant is invalid")
	}
	if len(c.Token) > 8192 || strings.ContainsFunc(c.Token, func(r rune) bool { return r < 0x21 || r > 0x7e }) {
		return errors.New("A2A bearer token is invalid")
	}
	if c.RequestTimeout < 0 || c.RequestTimeout > 5*time.Minute {
		return errors.New("A2A request timeout must be between zero and five minutes")
	}
	task, poll := c.TaskTimeout, c.PollInterval
	if task == 0 {
		task = time.Minute
	}
	if poll == 0 {
		poll = min(5*time.Second, task)
	}
	if task < time.Second || task > 5*time.Minute || poll < time.Second || poll > 30*time.Second || poll > task {
		return errors.New("A2A task timeout must be 1s to 5m and polling interval 1s to 30s within that timeout")
	}
	return nil
}

// CodeNotificationA2A delivers incident context. Acceptance does not establish
// recovery, and the notification does not poll or cancel a remote task.
type CodeNotificationA2A struct {
	URL               string        `yaml:"url" json:"url"`
	Token             string        `yaml:"token" json:"token"`
	Tenant            string        `yaml:"tenant" json:"tenant"`
	RequestTimeout    time.Duration `yaml:"request_timeout" json:"request_timeout"`
	Timeout           time.Duration `yaml:"timeout" json:"timeout"`
	AllowInsecureHTTP bool          `yaml:"allow_insecure_http" json:"allow_insecure_http"`
}

func (*CodeNotificationA2A) IsCodeNotification()      {}
func (c *CodeNotificationA2A) Copy() CodeNotification { copy := *c; return &copy }
func (c *CodeNotificationA2A) Validate() error {
	if c == nil {
		return errors.New("A2A notification configuration is required")
	}
	return (&InterventionTargetA2A{URL: c.URL, Token: c.Token, Tenant: c.Tenant,
		RequestTimeout: c.RequestTimeout, TaskTimeout: c.Timeout, AllowInsecureHTTP: c.AllowInsecureHTTP}).Validate()
}
