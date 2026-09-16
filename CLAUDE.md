# elly

A Github PR dashboard that polls every PR involving me and stores what decides
whose turn it is. It runs on this laptop as a systemd user unit, and the
dotfiles repo pins it as a flake input — see the `pr-status` skill for how the
data is meant to be read.

## Workflow: trunk-based

Commit to `main` and push. No branch, no PR, no review gate.
https://github.com/chelmertz/elly/pull/75 and
https://github.com/chelmertz/elly/pull/76 were the last two, and were only
ceremony against a single-author repo.

Two things stand in for the review that is no longer there:

- `CGO_ENABLED=0 go test ./...` before every push. There is no gcc on this
  machine, so the variable is not optional.
- `go tool golangci-lint run`, or `make lint` for the same plus the Github
  Actions and Dockerfile linters.

Anything that does not pass both stays uncommitted. A revert is one commit.

## Degrading honestly

When Github refuses data, elly says so rather than showing an empty result.
`fetchFailingChecks` in `internal/github` returns a `types.Degradation`, which
reaches `GET /api/v0/config/status` and the banner on the page. A missing
capability is reported as missing: an empty `ChecksFailing` on a red PR means
"names unavailable", never "nothing is failing". Keep any new blind spot on
that path.
