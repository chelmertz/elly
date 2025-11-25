# Development recipes

## Local dev
With a .env file containing something like:

```shell
export GITHUB_PATH=github_pat_123k135hjhhjtjethwejhtjh5jhj
```

```sh
find . | grep -E 'html|go' | entr -r -s 'source .env && go run .'
```

## Install the latest released version

Assumes using contrib/elly.service

```shell
git fetch --all; rm -f $(which elly); go install github.com/chelmertz/elly@$(git tag --sort=version:refname | tail -n1) && systemctl restart --user elly
```

