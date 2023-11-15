# Use SQLite as data store

Status: in use, deprecates ADR-0001

## Context and Problem Statement

ADR-0001 used a single JSON file which worked well for the "fetch all PRs, dump
on disk, repeat" flow.

We recently added the ability to modify the "view" state of a PR by burying it.
Burying a PR means giving it minus 1000 points. The PR is then hidden below the
fold in the web page. The PR is also exclude from the i3blocks rendered count of
"PRs to look at", which uses the API endpoint for "give me all PRs with at least
X points".

## Considered Options

1. Read, modify and dump the JSON blob more often
1. Keep a separate list of buried PR URLs to "join" with the Github provided PR
   data in the main JSON file
1. Rewrite data layer to use SQLite

## Decision Outcome

Since I'm a relational kind of person, who has been eyeing
[sqlc](https://sqlc.dev/) for a while, I went with using SQLite.

sqlc is exciting because unlike regular ORMs, you write the SQL and get the ORM.
