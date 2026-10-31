package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chelmertz/elly/internal/types"
)

// Limits on what another tool may write; elly renders all of it, escaped.
const (
	maxAnnotationBody  = 4 << 10
	maxAnnotationNote  = 200
	maxAnnotationLinks = 4
	maxLinkLabel       = 40
	maxLinkUrl         = 2048
	minAnnotationTTL   = time.Minute
	maxAnnotationTTL   = 24 * time.Hour
)

var annotationSource = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// WriteAnnotationToken creates a fresh bearer token for annotation writes and
// stores it in a file only the user can read. It rotates on every start, so a
// leaked token dies with the process.
func WriteAnnotationToken(path string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("could not generate annotation token: %w", err)
	}
	token := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("could not create annotation token dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("could not write annotation token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("could not write annotation token: %w", err)
	}
	return token, nil
}

// validateAnnotation parses a PUT body and checks it against the limits. It
// returns the annotation as stored and the parsed expiry.
func validateAnnotation(source string, body io.Reader, schemes []string, now time.Time) (types.Annotation, time.Time, error) {
	var req struct {
		State string `json:"state"`
		Note  string `json:"note"`
		Links []struct {
			Label string `json:"label"`
			Url   string `json:"url"`
		} `json:"links"`
		ExpiresAt string `json:"expiresAt"`
	}
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return types.Annotation{}, time.Time{}, fmt.Errorf("invalid body: %w", err)
	}

	if !annotationSource.MatchString(source) {
		return types.Annotation{}, time.Time{}, errors.New("source must be 1-40 characters of [a-z0-9-]")
	}
	if req.State != types.AnnotationHandled && req.State != types.AnnotationNeedsYou {
		return types.Annotation{}, time.Time{}, errors.New("state must be handled or needs_you")
	}
	if err := plainText(req.Note, maxAnnotationNote); err != nil {
		return types.Annotation{}, time.Time{}, fmt.Errorf("note: %w", err)
	}
	if len(req.Links) > maxAnnotationLinks {
		return types.Annotation{}, time.Time{}, fmt.Errorf("at most %d links", maxAnnotationLinks)
	}
	a := types.Annotation{Source: source, State: req.State, Note: req.Note, ExpiresAt: req.ExpiresAt, Links: []types.AnnotationLink{}}
	for _, l := range req.Links {
		if err := plainText(l.Label, maxLinkLabel); err != nil {
			return types.Annotation{}, time.Time{}, fmt.Errorf("label: %w", err)
		}
		if !allowedLink(l.Url, schemes) {
			return types.Annotation{}, time.Time{}, fmt.Errorf("link %q: https, or a scheme named in -annotation-schemes, at most %d characters", l.Url, maxLinkUrl)
		}
		a.Links = append(a.Links, types.AnnotationLink{Label: l.Label, Url: l.Url})
	}

	expires, err := time.Parse(time.RFC3339, req.ExpiresAt)
	if err != nil {
		return types.Annotation{}, time.Time{}, fmt.Errorf("expiresAt: %w", err)
	}
	if ttl := expires.Sub(now); ttl < minAnnotationTTL || ttl > maxAnnotationTTL {
		return types.Annotation{}, time.Time{}, fmt.Errorf("expiresAt must be between %v and %v from now", minAnnotationTTL, maxAnnotationTTL)
	}
	return a, expires, nil
}

func plainText(s string, max int) error {
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("at most %d characters", max)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return errors.New("control characters are not allowed")
	}
	return nil
}

// allowedLink is checked on write and again on render, so restarting elly
// without a scheme in -annotation-schemes revokes links already stored.
func allowedLink(raw string, schemes []string) bool {
	if raw == "" || len(raw) > maxLinkUrl {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "https" {
		return u.Host != ""
	}
	return scheme != "" && slices.Contains(schemes, scheme)
}

// annotationHref is the page's href for a stored link: the link itself when
// it still passes allowedLink, else "#". A custom scheme has to be passed as
// template.URL, or html/template replaces it with #ZgotmplZ.
func annotationHref(schemes []string) func(string) template.URL {
	return func(raw string) template.URL {
		if !allowedLink(raw, schemes) {
			return "#"
		}
		return template.URL(raw) //nolint:gosec // checked against the allowlist just above
	}
}

// annotationVerb shows what a custom-scheme link does, beside its label, so a
// label cannot pass off one action as another: "claude-review · approve".
func annotationVerb(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || strings.EqualFold(u.Scheme, "https") {
		return ""
	}
	segments := strings.Split(strings.Trim(u.Host+u.Path, "/"), "/")
	return u.Scheme + " · " + segments[len(segments)-1]
}

func registerAnnotationRoutes(mux *http.ServeMux, cfg HttpServerConfig) {
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if cfg.AnnotationToken == "" {
			http.Error(w, "annotation writes are disabled: elly has no token file", http.StatusServiceUnavailable)
			return false
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(cfg.AnnotationToken)) != 1 {
			http.Error(w, "missing or wrong bearer token", http.StatusUnauthorized)
			return false
		}
		return true
	}
	prUrl := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		b, err := base64.StdEncoding.DecodeString(r.PathValue("prUrl"))
		if err != nil {
			http.Error(w, "invalid PR ID", http.StatusBadRequest)
			return "", false
		}
		return string(b), true
	}

	mux.HandleFunc("PUT /api/v0/prs/{prUrl}/annotations/{source}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		u, ok := prUrl(w, r)
		if !ok {
			return
		}
		a, expires, err := validateAnnotation(r.PathValue("source"), http.MaxBytesReader(w, r.Body, maxAnnotationBody), cfg.AnnotationSchemes, time.Now())
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "body over 4 KiB", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !slices.ContainsFunc(cfg.Store.Prs().Prs, func(p types.ViewPr) bool { return p.Url == u }) {
			http.Error(w, "elly does not list that PR", http.StatusNotFound)
			return
		}
		if err := cfg.Store.PutAnnotation(u, a, expires); err != nil {
			cfg.logger().Error("could not store annotation", "error", err)
			http.Error(w, "could not store annotation", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /api/v0/prs/{prUrl}/annotations/{source}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		u, ok := prUrl(w, r)
		if !ok {
			return
		}
		if err := cfg.Store.DeleteAnnotation(u, r.PathValue("source")); err != nil {
			cfg.logger().Error("could not delete annotation", "error", err)
			http.Error(w, "could not delete annotation", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
