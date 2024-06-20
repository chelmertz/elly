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
    threads_actionable,
    threads_waiting,
    additions,
    deletions,
    review_requested_from_users,
    buried
) values (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
) returning *;

-- name: DeletePrs :exec
delete from prs;

-- name: ListPrs :many
select * from prs;

-- name: Bury :exec
update prs set buried = true where url = ?;

-- name: Unbury :exec
update prs set buried = false where url = ?;

-- name: BuriedPrs :many
select url from prs where buried = true;

-- name: StoreLastFetched :exec
replace into meta (key, value) values ('last_fetched', ?);

-- name: GetLastFetched :one
select value from meta where key = 'last_fetched' limit 1;
