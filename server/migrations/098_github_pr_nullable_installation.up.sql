-- github_pull_request rows created by the Ship Hub webhook bridge carry
-- no GitHub App installation ID. Drop NOT NULL so the column accepts NULL
-- for Ship Hub-originated rows while GitHub App rows keep their value.
ALTER TABLE github_pull_request ALTER COLUMN installation_id DROP NOT NULL;
