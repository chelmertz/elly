# Hiding PRs

Status: in use

## Context and Problem Statement

Some pull requests are just not interesting.

There seems to be two categories of unwanted PRs ending up wanting your attention:

- Perpetual: PRs you just want to ignore altogether
  - AFAICT, interacting with the PR means that you [cannot be unassigned from
  it](https://github.com/orgs/community/discussions/23054).
  - The PR is better off with other reviewers than you.
  - Being mentioned in a comment, will include you in the graphql query
    currently in use, since we use
   [`involves:username`](https://docs.github.com/en/search-github/searching-on-github/searching-issues-and-pull-requests#search-by-a-user-thats-involved-in-an-issue-or-pull-request).
    - Bots that auto-assigns, or mentions, based on git blame, for example.
- Temporary: PRs you are actually interested in, but you acted last on
  - We're interested in comment threads that are unanswered.
  - We're _uninterested_ in comments made by bots, such as "test environment
  deployed" or "passed type checks".

## Considered Options

- Keeping denylists of PRs locally
  - As little state as possible should be stored locally, state should be close
  to transient.
  - If all state could be managed by github, using
  drafts/reviewers/comments/reactions, we also share that state with everyone
  else.
- Adapt the scoring algorithm to opt-out from PRs with `#uninterested`-like
  - Could work but would force out a lot of spammy comments, especially if
  there's more than one user.
- Ignoring comments made by users like "github-actions", "vercel", "rustbot", ...
  - Would get tiring quickly, trying to keep up.

## Decision Outcome

For the perpetual ones:
- "bury" them, as in: don't hide them, just put them at the bottom of the prio list, so that they are out of the way but still accessible.

For the temporary ones:
- consider a "reaction" (where you put an emoji) as "marking the comment as
seen"
  - this doesn't take up as much space as an extra comment
  - still requires interacting with it once, revisit this if it's too annoying.
