create table prs (
    url text not null unique,
    review_status text not null,
    title text not null,
    author text not null,
    repo_name text not null,
    repo_owner text not null,
    repo_url text not null,
    is_draft boolean not null,
    last_updated text not null,
    last_pr_commenter text not null,
    unresponded_threads integer not null,
    additions integer not null,
    deletions integer not null,
    review_requested_from_users text not null,
    buried boolean not null
);
