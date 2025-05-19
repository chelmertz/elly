create table if not exists prs (
    url text not null primary key,
    review_status text not null,
    title text not null,
    author text not null,
    repo_name text not null,
    repo_owner text not null,
    repo_url text not null,
    is_draft boolean not null,
    last_updated text not null,
    last_pr_commenter text not null,
    threads_actionable integer not null,
    threads_waiting integer not null,
    additions integer not null,
    deletions integer not null,
    review_requested_from_users text not null,
    buried boolean not null,
    raw_json_response blob not null
);

create table if not exists meta (
    key text not null unique,
    value text not null
);
