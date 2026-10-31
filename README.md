# elly

Github pull requests presented in a prioritized order, via a keyboard driven web
GUI & API.

Requires a Github PAT (personal access token). Configure it through the web UI
settings, or pass it via the `GITHUB_PAT` environment variable. elly should be hosted
locally.

![Screenshot of GUI](gui.png)

![Screenshot of GUI, with dark mode](darkmode.png)

![Screenshot of GUI's about screen](about.png)

```mermaid
flowchart TB
    subgraph elly daemon
    A[./elly] --> B(Request PRs)
    H(systemd) -.->|Runs| A
    B -->|Timer, default 10 min| B
    F[(sqlite)]
    B -->|Persists|F
    A --> C(HTTP server)
    C -->|Reads| F
    C --> D(Server rendered GUI)
    C --> E(API)
    end
    D -.->|Opens in browser| G(Github PR view)
    I(i3blocks) -.-> E
```

## Desktop notifications

`elly -notify` sends a desktop notification through `notify-send` when a PR
gains a reason to act: your PR approved or with changes requested, someone
else commenting on your PR, a reply to your thread, or a review requested from
you. Each reason notifies once, until it goes away and comes back. The first
start records what is already there without notifying, and more than three in
one fetch arrive as one summary. Clicking a notification opens elly.

`-work-owners=org1,org2` limits those owners to the reasons above. A PR whose
repo owner is not listed, such as an open-source contribution, notifies on
every change by someone else: a push, a comment, a review. With no
`-work-owners`, every owner gets that.

## Annotations

Another local tool can mark a PR as `handled` or `needs_you`, with a short
note and up to four links. `handled` drops the PR's review-owed reasons from
its score; `needs_you` counts it whatever it scored, and wins over `handled`.
Annotations expire (1 minute to 24 hours after writing), so a tool that stops
running stops hiding PRs.

```
PUT    /api/v0/prs/{base64 PR url}/annotations/{source}
       {"state":"needs_you","note":"…","links":[{"label":"…","url":"…"}],"expiresAt":"<RFC 3339>"}
DELETE /api/v0/prs/{base64 PR url}/annotations/{source}
```

Writes need `Authorization: Bearer <token>`, where the token is in
`$XDG_RUNTIME_DIR/elly/annotation-token` (or `-annotation-token-file`),
readable only by the user and new on every start. Links must be https, or use
a scheme named in `-annotation-schemes`; elly shows a custom-scheme link's
last path segment beside its label. `GET /api/v0/prs` returns each PR's live
annotations.

## PAT Oauth permissions

Two token classes work, and they differ in exactly one thing: whether elly can
name a failing Github Actions job.

### Fine-grained (recommended, read-only)

A **fine-grained** token (`github_pat_…`, created at
<https://github.com/settings/personal-access-tokens>) with these _repository_
permissions:

- commit status (read only)
- contents (read only)
- metadata (read only)
- pull requests (read only)

Also:

- allow the token access to "all repositories"
- adjust the "resource owner" to your personal or your workplace's organisation
- set a proper expiration date

**A fine-grained token cannot name a failing Github Actions job.** Github
restricts the _checks_ permission to Github Apps and does not offer it in the
fine-grained token UI at all, so there is nothing to grant. A pull request's
checks arrive as two types: Github Actions jobs are `CheckRun` and need
_checks_, while everything reported from outside Github - buildkite, jenkins,
sonarcloud - is `StatusContext` and is covered by _commit status_. The overall
red/green verdict is an aggregate Github computes for you and stays correct
either way; what disappears is the name of the Actions job that failed, and it
disappears silently, as nulls rather than as an error. Elly notices and says so
on its own page rather than showing you an empty list.

### Classic (names the Actions jobs, much broader)

A **classic** token (`ghp_…`, <https://github.com/settings/tokens>) with the
`repo` scope reads `CheckRun` and so gets the names. The cost is that `repo` is
read _and write_ on every repository you can reach, where the fine-grained
token above is read-only on four things. Authorise it for any SSO organisation
you need, and note that an organisation can forbid classic tokens outright.

Pick the fine-grained one unless the names of failing Actions jobs are worth
that blast radius.

## Installation


### Go package

```shell
go install github.com/chelmertz/elly@latest
```

will fetch you the latest binary. See contrib/elly.service for a systemd
example of managing the service.

### Docker

```shell { name=docker }
docker run -d \
  --name elly \
  --restart unless-stopped \
  -v elly-data:/data \
  -p 9876:9876 \
  ghcr.io/chelmertz/elly:latest
```

If you have `gh` CLI installed and authenticated, you can use `$(gh auth
token)` instead of a PAT you stashed away somewhere:

This creates a named volume `elly-data` for the SQLite database (Docker manages it automatically).

The `--restart unless-stopped` flag ensures elly starts automatically on boot.

**Useful commands:**

```shell
docker logs -f elly                             # View logs
docker stop elly                                # Stop
docker rm elly                                  # Remove
docker pull ghcr.io/chelmertz/elly:latest       # Update image
```

### Nix

Run directly: `nix run github:chelmertz/elly`

Or, install permanently:

- Add the flake to your system configuration
- Or: `nix profile install github:chelmertz/elly`

## Developing

`make test` runs the unit and property tests (rapid). `make props-long` runs
the property tests with 10000 cases each and `make mutate` runs gremlins;
both are slow, so `.github/workflows/slow-tests.yml` runs them daily instead
of on every push.

See [dev.md](dev.md) for some useful commands during development.

## Design decisions

See [decisions/](decisions/) for ADRs.
