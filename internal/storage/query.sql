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
    rereview_from,
    reviewed_by,
    buried,
    checks_state,
    checks_failing,
    checks_complete,
    mergeable,
    merge_state_status,
    head_ref_oid,
    review_requested_of_user,
    asked,
    mentions,
    mentions_readable,
    raw_json_response
) values (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
) returning *;

-- name: DeletePrs :exec
delete from prs;

-- name: ListPrs :many
select * from prs;

-- name: Bury :exec
update prs set buried = true where url = ?;

-- name: Unbury :exec
update prs set buried = false where url = ?;

-- name: PrAskeds :many
select url, asked from prs;

-- name: BuriedPrs :many
select url, last_updated from prs where buried = true;

-- name: StoreLastFetched :exec
replace into meta (key, value) values ('last_fetched', ?);

-- name: GetLastFetched :one
select value from meta where key = 'last_fetched' limit 1;

-- name: StoreRateLimitUntil :exec
update token set rate_limited_until = ? where kind = 'main';

-- name: GetRateLimitUntil :one
select rate_limited_until from token where kind = 'main';

-- name: GetActivePAT :one
select pat, set_at, expires_at, username, owner from token where kind = 'main';

-- name: DeleteAllPATs :exec
delete from token where kind = 'main';

-- name: InsertPAT :exec
insert into token (kind, pat, expires_at, username) values ('main', ?, ?, ?);


-- name: StoreDegradations :exec
replace into meta (key, value) values ('degradations', ?);

-- name: GetDegradations :one
select value from meta where key = 'degradations' limit 1;

-- name: StoreNotifySeen :exec
replace into meta (key, value) values ('notify_seen', ?);

-- name: GetNotifySeen :one
select value from meta where key = 'notify_seen' limit 1;

-- name: PutAnnotation :exec
replace into annotations (pr_url, source, state, note, links, expires_at, expires_unix)
values (?, ?, ?, ?, ?, ?, ?);

-- name: DeleteAnnotation :exec
delete from annotations where pr_url = ? and source = ?;

-- name: PurgeExpiredAnnotations :exec
delete from annotations where expires_unix <= ?;

-- name: LiveAnnotations :many
select pr_url, source, state, note, links, expires_at from annotations
where expires_unix > ? order by pr_url, source;

-- name: LastLogin :one
select value from meta where key = 'last_login' limit 1;

-- name: SetLastLogin :exec
replace into meta (key, value) values ('last_login', ?);

-- name: ClearLastFetched :exec
delete from meta where key = 'last_fetched';

-- name: ClearNotifySeen :exec
delete from meta where key = 'notify_seen';

-- name: DeleteAllAnnotations :exec
delete from annotations;

-- name: ListOwnerPATs :many
select owner, pat, username, set_at, expires_at, last_success, rate_limited_until, refused
from token where kind = 'org' order by owner;

-- name: PutOwnerPAT :exec
insert into token (kind, owner, pat, username, expires_at) values ('org', ?, ?, ?, ?)
on conflict (owner) where kind = 'org' do update set owner = excluded.owner, pat = excluded.pat,
    username = excluded.username, expires_at = excluded.expires_at,
    set_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), rate_limited_until = '', refused = '';

-- name: DeleteOwnerPAT :execrows
delete from token where kind = 'org' and owner = ?;

-- name: DeleteAllOwnerPATs :exec
delete from token where kind = 'org';

-- name: SetOwnerPollState :exec
update token set last_success = ?, rate_limited_until = ?, refused = ?
where kind = 'org' and owner = ? and pat = ?;

-- name: SetPATExpiry :exec
update token set expires_at = ? where kind = 'main' and pat = ?;

-- name: SetPATOwner :exec
update token set owner = ? where kind = 'main' and pat = ?;

-- name: SetOwnerPATExpiry :exec
update token set expires_at = ? where kind = 'org' and owner = ? and pat = ?;
