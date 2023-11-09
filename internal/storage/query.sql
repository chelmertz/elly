-- name: GetPr :one
select * from prs where id = ? limit 1;