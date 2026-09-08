# Re-review signal

Status: in use

## Context and Problem Statement

A reviewer leaves comments or requests changes. The author pushes fixes and
answers every thread. Github now shows the PR as "review required" again, but
the reviewer gets no notification unless the author explicitly requests a
re-review, which authors forget. The PR sits, and neither side knows whose
turn it is. On 2026-09-08 this was the state of most of one user's 17 open
PRs waiting for review.

elly already scores "someone should respond to our comments" and "you should
add reviewers", but not this: the reviewer has spoken, the author has moved,
and nobody asked the reviewer back.

## Decision

Per PR authored by the user, `rereview_from` lists the reviewers whose latest
review is `COMMENTED` or `CHANGES_REQUESTED`, submitted before the author's
latest activity (last commit, last PR comment, last thread reply), and who are
not currently re-requested. Drafts are excluded (a draft is not asking for
review), as are PRs where the author still has unanswered threads (the ball is
still with the author). Bots and the author's own reviews never count.

Points: +20 with the reason "Ask <reviewers> to re-review". The action is the
author's; elly does not request the review itself.

The column is added to existing databases by an `alter table` on start,
because the schema is applied with `create table if not exists`, which never
adds columns. Later columns follow the same list.

## Consequences

- Downstream readers (p-launcher) can show "ask X to re-review" instead of a
  generic "waiting for review".
- The signal depends on all review threads being fetched; the same day's
  pagination fix (per-PR follow-up pages) is a prerequisite.
- A reviewer who reviews often but never approves will keep appearing after
  every push; that is the intended nudge.

## Rejected

- Requesting the re-review automatically: it is a social act and sometimes
  wrong (the reviewer said "looks fine after this, no need to ping me").
- Deriving the state from `reviewDecision` alone: it says `REVIEW_REQUIRED`
  both before and after the author's fixes.
