# Development recipes

I'm trying out [runme](https://docs.runme.dev/) as a playbook-runner-thingy. Install it and run `runme` from the `elly/` folder.

## Lint

Run golangci-lint locally (same version as CI):

```sh { name=lint }
go tool golangci-lint run
```

## Local dev
With a .env file containing something like:

```shell
export GITHUB_PATH=github_pat_123k135hjhhjtjethwejhtjh5jhj
```

```sh { name=watch }
find . | grep -E 'html|go' | entr -r -s 'source .env && go run .'
```

## Install the latest released version

Assumes using contrib/elly.service

```shell { name=upgrade }
git fetch --all; rm -f $(which elly); go install github.com/chelmertz/elly@$(git tag --sort=version:refname | tail -n1) && systemctl restart --user elly
```

## Tag new release

```shell { name=release }
make release
```

## Run with Docker Compose

Requires a `.env` file with `GITHUB_PAT=...` (no `export` prefix).

```sh { name=docker-compose }
docker compose up --build
```

## Install or update docker image locally

```sh { name=update-docker }
docker pull ghcr.io/chelmertz/elly:latest
```
