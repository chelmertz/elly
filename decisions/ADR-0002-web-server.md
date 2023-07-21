# HTTP API and web server

Status: in use

## Context and Problem Statement

There needs to be a way to get PR data, decorated or not.

## Considered Options

- Clients looking at the data store directly (json/sqlite/...)
  - Less guarantees for scripts to work, storage will change over time.
  - Would not require a daemon.
  - Hard to deploy & access non-locally.
- HTTP server
  - Would require a daemon.
  - Can provide a simple web GUI in the same process. The rendering is are nicer
    than in TUIs.
  - Easy to implement in Go.
  - No large security surface, local usage is prioritized.
  - Can easily grow into something more.
  - Common patterns for hiding access to it, with reverse proxies or firewalls.
- UNIX sockets
  - Would require a daemon.
  - Not too much simpler to work with, compared against HTTP.
  
Permissions and ownership will always be impactful, but it feels like HTTP
servers can be contained pretty well.

All options are easy to script against.

## Decision Outcome

A web server seems to provide most bang for the buck.
