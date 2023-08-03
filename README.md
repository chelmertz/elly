# elly

Github pull requests presented in a prioritized order, via a keyboard driven web
gui & API.

Configured by a Github PAT (personal access token) and the Github username,
using the env vars `GITHUB_USER` and `GITHUB_PAT` respectively. Should be hosted
locally.

```mermaid
flowchart TB
    subgraph elly daemon
    A[run ./elly] -->|Runs| B(Request PRs)
    H(systemd) -.-> A
    B -->|Timer, default 10 min| B
    F[(prs.json)]
    B -->|Persists|F
    A -->|Runs| C(HTTP server)
    C -->|Reads| F
    C --> D(Server rendered GUI)
    C --> E(API)
    end
    D -.->|Opens in browser| G(Github PR view)
    I(i3blocks) -.-> E
```

![Screenshot of GUI](gui.png)

![Screenshot of GUI's about screen](about.png)

## PAT Oauth permissions

A Github personal access token these permissions:

- commit status (read only)
- contents (read only)
- metadata (read only)
- pull requests (read only)

## Installation

```
go install github.com/chelmertz/elly@latest
```
will fetch you the latest binary. See contrib/elly.service for a systemd
example of managing the service.

## Design decisions

See `/decisions` for ADRs.

## Developing

With a .env file containing something like:

```
export GITHUB_USER=chelmertz
export GITHUB_PATH=github_pat_123k135hjhhjtjethwejhtjh5jhj
```

```sh
find . | grep -E 'html|go' | entr -r -s 'source .env && go run .'
```
