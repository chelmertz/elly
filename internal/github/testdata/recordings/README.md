# Recordings

Each `poll-N.json` is one poll, recorded with `elly -record` and anonymised;
`poll-N.want.json` is what elly makes of it (`go test ./internal/github -update`).

- `poll-1.json`: the main token's orgs.
- `poll-2.json`: an org polled with its owner token, 2026-11-17. `repo1/pull/2`
  asks the user and a team the token cannot read, so the team node is `null`.
- `poll-3.json`: derived from `poll-2.json`, not recorded. The user's own node
  is removed from `repo1/pull/2`, which leaves a request only to the hidden
  team. No PR asked only a team when `poll-2` was recorded.
- 2026-11-20: `issueCount` was added to the two search requests by hand in
  all three, without re-recording: the responses lack it, which reads as a
  search that returned every match, as these did. Re-record when convenient.
