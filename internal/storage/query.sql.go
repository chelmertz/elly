// Code generated by sqlc. DO NOT EDIT.
// versions:
//   sqlc v1.15.0
// source: query.sql

package storage

import (
	"context"
)

const bury = `-- name: Bury :exec
update prs set buried = true where url = ?
`

func (q *Queries) Bury(ctx context.Context, url string) error {
	_, err := q.db.ExecContext(ctx, bury, url)
	return err
}

const createPr = `-- name: CreatePr :one
insert into prs (
    url,
    review_status,
    title,
    author,
    repo_name,
    repo_owner,
    repo_url,
    is_draft,
    last_updated,
    last_pr_commenter,
    unresponded_threads,
    additions,
    deletions,
    review_requested_from_users,
    buried
) values (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
) returning url, review_status, title, author, repo_name, repo_owner, repo_url, is_draft, last_updated, last_pr_commenter, unresponded_threads, additions, deletions, review_requested_from_users, buried
`

type CreatePrParams struct {
	Url                      string
	ReviewStatus             string
	Title                    string
	Author                   string
	RepoName                 string
	RepoOwner                string
	RepoUrl                  string
	IsDraft                  bool
	LastUpdated              string
	LastPrCommenter          string
	UnrespondedThreads       int64
	Additions                int64
	Deletions                int64
	ReviewRequestedFromUsers string
	Buried                   bool
}

func (q *Queries) CreatePr(ctx context.Context, arg CreatePrParams) (Pr, error) {
	row := q.db.QueryRowContext(ctx, createPr,
		arg.Url,
		arg.ReviewStatus,
		arg.Title,
		arg.Author,
		arg.RepoName,
		arg.RepoOwner,
		arg.RepoUrl,
		arg.IsDraft,
		arg.LastUpdated,
		arg.LastPrCommenter,
		arg.UnrespondedThreads,
		arg.Additions,
		arg.Deletions,
		arg.ReviewRequestedFromUsers,
		arg.Buried,
	)
	var i Pr
	err := row.Scan(
		&i.Url,
		&i.ReviewStatus,
		&i.Title,
		&i.Author,
		&i.RepoName,
		&i.RepoOwner,
		&i.RepoUrl,
		&i.IsDraft,
		&i.LastUpdated,
		&i.LastPrCommenter,
		&i.UnrespondedThreads,
		&i.Additions,
		&i.Deletions,
		&i.ReviewRequestedFromUsers,
		&i.Buried,
	)
	return i, err
}

const deletePrs = `-- name: DeletePrs :exec
delete from prs
`

func (q *Queries) DeletePrs(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, deletePrs)
	return err
}

const getLastFetched = `-- name: GetLastFetched :one
select value from meta where key = 'last_fetched' limit 1
`

func (q *Queries) GetLastFetched(ctx context.Context) (string, error) {
	row := q.db.QueryRowContext(ctx, getLastFetched)
	var value string
	err := row.Scan(&value)
	return value, err
}

const getPr = `-- name: GetPr :one
select url, review_status, title, author, repo_name, repo_owner, repo_url, is_draft, last_updated, last_pr_commenter, unresponded_threads, additions, deletions, review_requested_from_users, buried from prs where url = ? limit 1
`

func (q *Queries) GetPr(ctx context.Context, url string) (Pr, error) {
	row := q.db.QueryRowContext(ctx, getPr, url)
	var i Pr
	err := row.Scan(
		&i.Url,
		&i.ReviewStatus,
		&i.Title,
		&i.Author,
		&i.RepoName,
		&i.RepoOwner,
		&i.RepoUrl,
		&i.IsDraft,
		&i.LastUpdated,
		&i.LastPrCommenter,
		&i.UnrespondedThreads,
		&i.Additions,
		&i.Deletions,
		&i.ReviewRequestedFromUsers,
		&i.Buried,
	)
	return i, err
}

const listPrs = `-- name: ListPrs :many
select url, review_status, title, author, repo_name, repo_owner, repo_url, is_draft, last_updated, last_pr_commenter, unresponded_threads, additions, deletions, review_requested_from_users, buried from prs
`

func (q *Queries) ListPrs(ctx context.Context) ([]Pr, error) {
	rows, err := q.db.QueryContext(ctx, listPrs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Pr
	for rows.Next() {
		var i Pr
		if err := rows.Scan(
			&i.Url,
			&i.ReviewStatus,
			&i.Title,
			&i.Author,
			&i.RepoName,
			&i.RepoOwner,
			&i.RepoUrl,
			&i.IsDraft,
			&i.LastUpdated,
			&i.LastPrCommenter,
			&i.UnrespondedThreads,
			&i.Additions,
			&i.Deletions,
			&i.ReviewRequestedFromUsers,
			&i.Buried,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const storeLastFetched = `-- name: StoreLastFetched :exec
replace into meta (key, value) values ('last_fetched', ?)
`

func (q *Queries) StoreLastFetched(ctx context.Context, value string) error {
	_, err := q.db.ExecContext(ctx, storeLastFetched, value)
	return err
}

const unbury = `-- name: Unbury :exec
update prs set buried = false where url = ?
`

func (q *Queries) Unbury(ctx context.Context, url string) error {
	_, err := q.db.ExecContext(ctx, unbury, url)
	return err
}