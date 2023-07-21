# Dependencies

Status: in use

## Context and Problem Statement

This project aims to be written once-ish and not require a lot of maintenance.

## Considered Options

- NPM/yarn for the front end
  - Requires loads of management since nothing in javascript/typescript land is
    ever done.
- No front end framework
  - Browser support is large enough, and we will aim for simple/minimalistic UX.
  - Supporting simple `find | entr 'go run main'`-ish workflows, without webpack
    et al., will support a very fast feedback cycle.
- Go deps
  - Seems to require fewer hours of maintenance, and (hopefully) fewer breaking
    changes.
  - Could be handled with proper boundaries (and tests) around the dependencies.
  - A rich standard library means that we don't need a framework (for this small
    of a project).
- Vendoring deps
  - Would at least make builds reproducible & work offline. Still a maintenance
    burden.
- Dependabot 
  - Since we're using Github as a forge for this repository, using its
    capabilities makes for a high ROI.

## Decision Outcome

NPM dependencies for the frontend is out of the question, that's way too much of
a time sink. 

Try to minimize the amount of Go deps. We really only speak HTTP (stdlib),
renders HTML (stdlib), and store files (so far; in the future some SQLite dep.
will be required).
