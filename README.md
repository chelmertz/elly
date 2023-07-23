# elly

Github pull requests presented in a prioritized order, via a keyboard driven web
gui & API.

Configured by a Github PAT (personal access token) and the Github username,
using the env vars `GITHUB_USER` and `GITHUB_PAT` respectively. Should be hosted
locally.

## PAT Oauth permissions

A Github personal access token these permissions:

- commit status (read only)
- contents (read only)
- metadata (read only)
- pull requests (read only)

# Developing

With a .env file containing something like:

```
export GITHUB_USER=chelmertz
export GITHUB_PATH=github_pat_123k135hjhhjtjethwejhtjh5jhj
```

```sh
find . | grep -E 'html|go' | entr -r -s 'source .env && go run .'
```

# Todos

- [ ] empty state gui
- [ ] run webserver with old data even if PAT is expired/bad

# Maybes
- [ ] ease setup (html template with intro)
  - [ ] store pattern + username in sqlite instead
    - encrypt, and decrypt patterns on startup?
  - [ ] store prs in sqlite instead
- [ ] support multiple users/tokens/owners (user/organization)
  - ADR-0003

# Out of scope

- adapted repositories scanning frequency
- Github issues
- notifications?
  - there are many other solutions for getting notifications from github
- see what changed since _timestamp_, i.e. when last logged in or such
  - this goes against "things need to be done, e.g. there are unanswered comments"
  - would need to track _timestamp x user x pr_ which is bloated, and hard to know when to update the timestamp
