# Use JSON as data store

Status: deprecated by ADR-0001

## Context and Problem Statement

The resulting payload of querying Github must be persisted, so that we can save
API calls and not rely on Github being online (or not having requests left per
hour, etc.). We can also filter data later, without having to refetch.

## Considered Options

- Multiple plain text files, with a directory structure
- SQLite

## Decision Outcome

Start with a single JSON file. Use a single go struct, such as

```go
type storage struct {
    LastFetchedAt time.Time
    Prs           struct {...}
}
```
and see which part of the code that accesses the data. Create methods for those,
and evolve the data storage to suit those methods.

Just load the JSON file, deserialize it, update it, serialize it, save to file
again. This should be enough when prototyping. Go makes it easy to safeguard
with a mutex.