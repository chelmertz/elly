-- name: GetPr :one
select * from prs where url = ? limit 1;

-- name: CreatePr :one
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
) returning *;

-- name: DeletePrs :exec
delete from prs;

-- name: ListPrs :many
select * from prs;

-- name: StoreLastFetched :exec
replace into meta (key, value) values ('last_fetched', ?);

-- name: GetLastFetched :one
select value from meta where key = 'last_fetched' limit 1;