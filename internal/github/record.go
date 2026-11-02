package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/chelmertz/elly/anonymise"
)

// recording is one poll's exchanges with github, in the order they were made,
// and the time the poll started. Committed recordings are anonymised, so the
// poll time is shifted along with every other timestamp in them.
type recording struct {
	PollTime  time.Time  `json:"pollTime"`
	Exchanges []exchange `json:"exchanges"`
}

type exchange struct {
	Request  json.RawMessage `json:"request"`
	Status   int             `json:"status"`
	Response json.RawMessage `json:"response"`
}

var errRecordingRefused = errors.New("recording refused")

// Record runs one poll against github and returns it as an anonymised
// recording, ready to commit. Only PRs whose repository owner is in owners are
// kept: the rest are dropped from the search responses before elly reads
// them, so they get no follow-up requests either. A poll that saw anything
// but a 200 with a JSON body, or a rate limit, is refused: it would be a
// recording of a fault. The raw exchange stays in memory; nothing raw is
// returned, and log lines lose their attributes, which carry PR urls.
func Record(baseURL, token, username string, owners []string, logger *slog.Logger) ([]byte, error) {
	if len(owners) == 0 {
		return nil, fmt.Errorf("%w: no owner allowlist", errRecordingRefused)
	}
	rec := &recorder{owners: owners}
	quiet := slog.New(attrlessHandler{logger.Handler()})
	start := time.Now()
	_, _, err := api{baseURL: baseURL, token: token, logger: quiet, tap: rec.tap}.poll(username)
	if rec.refused != nil {
		return nil, rec.refused
	}
	if err != nil {
		// the error can carry a PR url, so only its kind is passed on
		return nil, fmt.Errorf("%w: the poll failed (%s)", errRecordingRefused, errorKind(err))
	}
	raw, err := json.Marshal(recording{PollTime: start, Exchanges: rec.exchanges})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRecordingRefused, err)
	}
	out, err := anonymise.Anonymise(recordingRules(username), raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRecordingRefused, err)
	}
	return out, nil
}

func errorKind(err error) string {
	var rl *ErrRateLimited
	switch {
	case errors.As(err, &rl):
		return "rate limited"
	case errors.Is(err, ErrInvalidToken):
		return "invalid token"
	case errors.Is(err, ErrGithubServer):
		return "server error"
	case errors.Is(err, ErrClient):
		return "client error"
	}
	return "other error"
}

type recorder struct {
	owners    []string
	exchanges []exchange
	refused   error
}

func (r *recorder) refuse(format string, args ...any) error {
	if r.refused == nil {
		r.refused = fmt.Errorf("%w: exchange %d: %s", errRecordingRefused, len(r.exchanges)+1, fmt.Sprintf(format, args...))
	}
	return r.refused
}

func (r *recorder) tap(request []byte, status int, response []byte) ([]byte, error) {
	if status != 200 {
		return nil, r.refuse("status %d", status)
	}
	if !json.Valid(response) {
		return nil, r.refuse("the body is not JSON")
	}
	var envelope struct {
		Errors []struct{ Type string }
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, r.refuse("the body is not a graphql response")
	}
	for _, e := range envelope.Errors {
		if e.Type == "RATE_LIMITED" {
			return nil, r.refuse("rate limited")
		}
	}
	var q graphqlQuery
	if err := json.Unmarshal(request, &q); err != nil {
		return nil, r.refuse("the request is not a graphql body")
	}
	if q.Query == searchPrsQuery {
		filtered, err := r.keepOwners(response)
		if err != nil {
			return nil, r.refuse("%v", err)
		}
		response = filtered
	}
	r.exchanges = append(r.exchanges, exchange{Request: request, Status: status, Response: response})
	return response, nil
}

// keepOwners drops every search result whose repository owner is not on the
// allowlist, before anything else sees it.
func (r *recorder) keepOwners(response []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(response))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, errors.New("the search response is not an object")
	}
	data, _ := doc["data"].(map[string]any)
	search, _ := data["search"].(map[string]any)
	edges, ok := search["edges"].([]any)
	if !ok {
		return nil, errors.New("the search response has no edges")
	}
	kept := []any{}
	for _, e := range edges {
		node, _ := e.(map[string]any)["node"].(map[string]any)
		repo, _ := node["repository"].(map[string]any)
		owner, _ := repo["owner"].(map[string]any)
		login, _ := owner["login"].(string)
		if slices.ContainsFunc(r.owners, func(o string) bool { return strings.EqualFold(o, login) }) {
			kept = append(kept, e)
		}
	}
	search["edges"] = kept
	return json.Marshal(doc)
}

// attrlessHandler drops every attribute and group from a log record. A poll
// logs PR urls and, at debug level, whole bodies; during a recording neither
// may reach a log.
type attrlessHandler struct{ next slog.Handler }

func (h attrlessHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h attrlessHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.next.Handle(ctx, slog.NewRecord(r.Time, r.Level, r.Message, r.PC))
}

func (h attrlessHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h attrlessHandler) WithGroup(string) slog.Handler      { return h }
