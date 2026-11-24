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
    rereview_from text not null default '',
    reviewed_by text not null default '',
    buried boolean not null,
    checks_state text not null default '',
    checks_failing text not null default '',
    checks_complete boolean not null default 1,
    mergeable text not null default '',
    merge_state_status text not null default '',
    head_ref_oid text not null default '',
    review_requested_of_user boolean not null default 0,
    asked text not null default '',
    mentions text not null default '[]',
    mentions_readable boolean not null default 0,
    raw_json_response blob not null
);

create table if not exists meta (
    key text not null unique,
    value text not null
);

-- Every GitHub token elly holds. kind 'main' is the one the user's login
-- and PR search come from (at most one); kind 'org' is one per repo owner
-- the main token cannot see, such as an org that takes only fine-grained
-- tokens, and its login must be the main one's. owner is the token's
-- resource owner as GitHub spells it: for an org row the org, for the main
-- row '' until elly finds it. refused is '' while usable, else the
-- degradation kind that stopped it (main-token trouble is reported in
-- meta.degradations instead). last_success is an org row's last poll (the
-- main token's is meta.last_fetched, which belongs to the stored PRs).
-- Times are RFC3339 or ''; '' in rate_limited_until means not limited.
create table if not exists token (
    kind text not null check (kind in ('main', 'org')),
    owner text not null default '' collate nocase,
    pat text not null,
    username text not null,
    set_at text not null default (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    expires_at text not null,
    last_success text not null default '',
    rate_limited_until text not null default '',
    refused text not null default '',
    check (kind = 'main' or owner <> '')
);
create unique index if not exists token_one_main on token (kind) where kind = 'main';
create unique index if not exists token_one_per_org on token (owner) where kind = 'org';

-- Kept apart from prs, which is replaced on every fetch. expires_at is
-- returned exactly as written; expires_unix is what expiry compares.
create table if not exists annotations (
    pr_url       text not null,
    source       text not null,
    state        text not null check (state in ('handled', 'needs_you', 'running', 'stuck', 'info')),
    note         text not null default '',
    links        text not null default '[]',
    expires_at   text not null,
    expires_unix integer not null,
    -- last, where the add column in NewStorage puts it on an older table
    handover     boolean not null default 0,
    primary key (pr_url, source)
);
