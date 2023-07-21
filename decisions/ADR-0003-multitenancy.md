# Multitenancy

Status: non-prioritized

## Context and Problem Statement

Building something locally for a single user is easy. There should be a path
forward for handling multiple users of some kind.

Example future scenarios we might want to support:
- Multiple Github users in the same instance
  - Either representing multiple humans, or the same human having e.g. different
    accounts for personal and work related coding.
  - We still need to use the graphql query for `involves:username`, so it would
    be one query per user, that would need to be batched.
- Additional forges, like Gitlab
- (Github) issue support, i.e. not only pull requests

## Considered Options

- When having an RDBMS (SQLite, most probably), we could map platform (Github),
  user (Github user), PAT (Github personal access token) etc., and support all
  of it in a single instance.
- Launching separate instances, each configured to support a single user system.

## Decision Outcome

Revisit this in the future, after dogfooding for a while. Not prioritized enough
to be solved at the moment.
