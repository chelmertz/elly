package server

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// contentSecurityPolicy goes on every response. The page's one inline script
// is allowed by its hash and nothing else may run; styles stay inline.
var contentSecurityPolicy = mustPageCSP()

var scriptElement = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// inlineScript returns the contents of the page's one inline script. A
// template action inside it would make the hash depend on the data, so
// that is an error too.
func inlineScript(page string) (string, error) {
	scripts := scriptElement.FindAllStringSubmatch(page, -1)
	if len(scripts) != 1 {
		return "", fmt.Errorf("the page has %d inline scripts, want 1", len(scripts))
	}
	if strings.Contains(scripts[0][1], "{{") {
		return "", errors.New("the page's script holds a template action, so its hash would depend on the data")
	}
	return scripts[0][1], nil
}

// mustPageCSP hashes the script as the page serves it: html/template strips
// JS comments, so next.html's own text would not match.
func mustPageCSP() string {
	src, err := index.ReadFile("next.html")
	check(err)
	_, err = inlineScript(string(src))
	check(err)
	tmpl, err := parseNext()
	check(err)
	var page strings.Builder
	check(tmpl.Execute(&page, NextData{}))
	script, err := inlineScript(page.String())
	check(err)
	sum := sha256.Sum256([]byte(script))
	return "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; style-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"
}
