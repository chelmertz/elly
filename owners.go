package main

import (
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/chelmertz/elly/internal/github"
	"github.com/chelmertz/elly/internal/storage"
	"github.com/chelmertz/elly/internal/types"
)

// ownerCarry is how long an owner's PRs outlive a transient failure of its
// token, counted from its last successful poll. A refusal drops them at once.
const ownerCarry = time.Hour

// The degradations an owner token reports. Each names the owner and never
// GitHub's own text, which goes to the log.
const (
	degradationOwnerRefused     = "owner_token_refused"
	degradationOwnerNoAccess    = "owner_token_no_access"
	degradationOwnerUnreachable = "owner_token_unreachable"
	degradationOwnerOtherLogin  = "owner_token_other_login"
)

// mergeOwners polls each owner token and merges its owner's PRs into the
// primary's. An owner whose token answered is taken from that token only; on
// a refusal its PRs are left out and the owner is not polled again until a
// new token; on any other failure its PRs from previous are carried for
// ownerCarry after its last success. states are the owner tokens whose poll
// state changed.
func mergeOwners(primary []types.ViewPr, username string, owners []storage.OwnerPAT, previous []types.ViewPr, now time.Time,
	poll func(storage.OwnerPAT) ([]types.ViewPr, []types.Degradation, error), logger *slog.Logger,
) (prs []types.ViewPr, ds []types.Degradation, states []storage.OwnerPAT) {
	answered := map[string]bool{}
	var ownerPrs []types.ViewPr
	for _, o := range owners {
		if !strings.EqualFold(o.Username, username) {
			ds = append(ds, ownerDegradation(o, degradationOwnerOtherLogin, username, now))
			continue
		}
		if o.Refused != "" {
			ds = append(ds, ownerDegradation(o, o.Refused, username, now))
			continue
		}
		next := o
		if !now.Before(o.RateLimitedUntil) {
			got, gotDs, err := poll(o)
			if err == nil {
				answered[strings.ToLower(o.Owner)] = true
				ownerPrs = append(ownerPrs, got...)
				for _, d := range gotDs {
					d.Owner = o.Owner
					ds = append(ds, d)
				}
				next.LastSuccess, next.RateLimitedUntil = now, time.Time{}
				states = append(states, next)
				continue
			}
			logger.Warn("could not poll an owner token", slog.String("owner", o.Owner), slog.Any("error", err))
			var rl *github.ErrRateLimited
			switch {
			case errors.Is(err, github.ErrInvalidToken):
				next.Refused = degradationOwnerRefused
			case errors.Is(err, github.ErrNoAccess):
				next.Refused = degradationOwnerNoAccess
			case errors.As(err, &rl):
				next.RateLimitedUntil = rl.UnblockedAt
			}
			states = append(states, next)
			if next.Refused != "" {
				ds = append(ds, ownerDegradation(next, next.Refused, username, now))
				continue
			}
		}
		carry := !o.LastSuccess.IsZero() && now.Sub(o.LastSuccess) < ownerCarry
		ds = append(ds, unreachable(next, carry, now))
		if carry {
			for _, pr := range previous {
				if strings.EqualFold(pr.RepoOwner, o.Owner) {
					ownerPrs = append(ownerPrs, pr)
				}
			}
		}
	}

	// The primary sees an answered owner's public repos at most; that
	// owner's token is the one source for it.
	seen := map[string]bool{}
	for _, pr := range primary {
		if !answered[strings.ToLower(pr.RepoOwner)] {
			seen[pr.Url] = true
			prs = append(prs, pr)
		}
	}
	for _, pr := range ownerPrs {
		if !seen[pr.Url] {
			seen[pr.Url] = true
			prs = append(prs, pr)
		}
	}
	return prs, ds, states
}

func ownerDegradation(o storage.OwnerPAT, kind, username string, now time.Time) types.Degradation {
	d := types.Degradation{Kind: kind, Owner: o.Owner, Seen: now}
	switch kind {
	case degradationOwnerRefused:
		d.Message = "GitHub refused the token for " + o.Owner + "; its PRs are left out."
		d.Remedy = "Paste a new token for " + o.Owner + "."
	case degradationOwnerNoAccess:
		d.Message = "The token for " + o.Owner + " sees none of its private repositories; its PRs are left out."
		d.Remedy = "Check that " + o.Owner + " approved the token and that its resource owner is " + o.Owner + ", then paste it again."
	default: // degradationOwnerOtherLogin
		d.Message = "The token for " + o.Owner + " belongs to another login; its PRs are left out."
		d.Remedy = "Paste a token for " + o.Owner + " made by @" + username + "."
	}
	return d
}

func unreachable(o storage.OwnerPAT, carried bool, now time.Time) types.Degradation {
	d := types.Degradation{Kind: degradationOwnerUnreachable, Owner: o.Owner, Seen: now,
		Remedy: "elly tries again on the next poll."}
	switch {
	case now.Before(o.RateLimitedUntil):
		d.Message = "GitHub rate limits the token for " + o.Owner + " until " + o.RateLimitedUntil.Local().Format("15:04") + "."
	default:
		d.Message = "Could not poll " + o.Owner + "."
	}
	if carried {
		d.Message += " Its PRs are as of " + o.LastSuccess.Local().Format("15:04") + "."
	} else {
		d.Message += " Its PRs are left out until a poll succeeds."
	}
	return d
}

// sameTokens says whether a and b hold the same token for each owner.
func sameTokens(a, b []storage.OwnerPAT) bool {
	return slices.EqualFunc(a, b, func(x, y storage.OwnerPAT) bool { return x.Owner == y.Owner && x.Token == y.Token })
}
