package config

import (
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// DefaultPostHogHost is PostHog EU Cloud's ingestion origin (D231).
const DefaultPostHogHost = "https://eu.i.posthog.com"

// Analytics is where the app sends its product analytics (D231): GET /v1/config → analytics.
// The api serves it because both repositories are public and the key must not be compiled
// into the app. ProjectKey empty means none: the api serves analytics null.
type Analytics struct {
	Host       string // POSTHOG_HOST: an https origin, DefaultPostHogHost by default
	ProjectKey string // POSTHOG_PROJECT_KEY (the posthog-project-key secret): never in an error, a log line or a metric label
}

// postHogProjectKey is the form of a PostHog project API key: phc_ and the key. A personal
// API key (phx_…) reads and changes the whole organisation, so it must never be served to the
// app by mistake: anything else is refused.
var postHogProjectKey = regexp.MustCompile(`^phc_[A-Za-z0-9_-]{20,128}$`)

// LoadAnalytics reads POSTHOG_PROJECT_KEY and POSTHOG_HOST. The key is trimmed (a secret
// file's newline) and optional; when set it must be a project API key (phc_…). The host is
// checked whether or not the key is set: an https origin (no userinfo, port, path, trailing
// slash, query or fragment, lower case), so a typo stops the api at start-up instead of
// sending the app elsewhere. No error quotes either value.
func LoadAnalytics() (Analytics, error) {
	a := Analytics{
		Host:       strings.TrimSpace(getenv("POSTHOG_HOST", DefaultPostHogHost)),
		ProjectKey: strings.TrimSpace(os.Getenv("POSTHOG_PROJECT_KEY")),
	}
	if a.Host == "" {
		a.Host = DefaultPostHogHost
	}
	if err := analyticsHost(a.Host); err != nil {
		return Analytics{}, err
	}
	if a.ProjectKey != "" && !postHogProjectKey.MatchString(a.ProjectKey) {
		return Analytics{}, errors.New("POSTHOG_PROJECT_KEY must be a PostHog project API key (phc_…, PostHog → Project settings → " +
			"Project API key), never a personal API key; unset, the api serves analytics null")
	}
	return a, nil
}

// analyticsHostname is a DNS name of two or more labels, lower case, no trailing dot: no IP
// literal, no port, nothing url.Parse lets through in a host (a comma, quotes, brackets).
var analyticsHostname = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// analyticsHost accepts exactly https://<hostname> (written so: the scheme lower case, nothing
// after the name), and the app is served the string as it is. Its errors quote nothing of s: a
// key pasted into the wrong variable must not reach the log.
func analyticsHost(s string) error {
	const want = "an https origin such as " + DefaultPostHogHost
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return errors.New("POSTHOG_HOST is not a URL; want " + want)
	case u.Scheme != "https" || u.Opaque != "" || u.Host == "" || !strings.HasPrefix(s, "https://"):
		return errors.New("POSTHOG_HOST must be https (the app sends its events there); want " + want)
	case u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "":
		return errors.New("POSTHOG_HOST must be an origin only: no userinfo, path, trailing slash, query or fragment; want " + want)
	case strings.Contains(u.Host, ":"):
		return errors.New("POSTHOG_HOST must name no port; want " + want)
	case !analyticsHostname.MatchString(u.Host) || s != "https://"+u.Host:
		return errors.New("POSTHOG_HOST must be a host name, lower case (no IP address, no trailing dot); want " + want)
	}
	return nil
}
