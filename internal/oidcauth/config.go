// Package oidcauth implements optional OpenID Connect sign-in for the web UI.
package oidcauth

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Config describes one OIDC provider and the subjects permitted to sign in.
type Config struct {
	Issuer          string
	ClientID        string
	ClientSecret    string
	PublicURL       string
	AllowedSubjects string
	AdminSubjects   string
	Webroot         string
}

// Enabled reports whether any OIDC setting was provided.
func (c Config) Enabled() bool {
	return c.Issuer != "" || c.ClientID != "" || c.ClientSecret != "" || c.PublicURL != "" || c.AllowedSubjects != "" || c.AdminSubjects != ""
}

// Validate rejects incomplete or unsafe OIDC configurations.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if strings.TrimSpace(c.Issuer) == "" || strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.PublicURL) == "" {
		return errors.New("OIDC requires an issuer, client ID, and public URL")
	}
	allowed := c.subjects()
	if len(allowed) == 0 {
		return errors.New("OIDC requires at least one allowed subject")
	}
	for subject := range parseSubjects(c.AdminSubjects) {
		if _, ok := allowed[subject]; !ok {
			return errors.New("OIDC administrator subjects must also be allowed subjects")
		}
	}
	if _, err := secureURL(c.Issuer, true); err != nil {
		return fmt.Errorf("OIDC issuer: %w", err)
	}
	if _, err := secureURL(c.PublicURL, false); err != nil {
		return fmt.Errorf("OIDC public URL: %w", err)
	}
	if !strings.HasPrefix(c.Webroot, "/") || !strings.HasSuffix(c.Webroot, "/") || strings.HasPrefix(c.Webroot, "//") {
		return errors.New("OIDC requires a normalized webroot")
	}
	return nil
}

func (c Config) subjects() map[string]struct{} {
	return parseSubjects(c.AllowedSubjects)
}

func parseSubjects(value string) map[string]struct{} {
	subjects := make(map[string]struct{})
	for _, subject := range strings.Split(value, ",") {
		if subject = strings.TrimSpace(subject); subject != "" {
			subjects[subject] = struct{}{}
		}
	}
	return subjects
}

func secureURL(raw string, allowPath bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("must be an absolute URL without user info, query, or fragment")
	}
	if !allowPath && u.Path != "" && u.Path != "/" {
		return nil, errors.New("must be an origin without a path")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("must use HTTPS except on localhost")
	}
	return u, nil
}
