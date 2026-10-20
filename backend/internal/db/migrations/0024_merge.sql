-- Merging two accounts of one person (docs/DECISIONS.md D90): the api moves the raw
-- data of the account it merges away to the one it keeps. It already reads and deletes
-- these rows; moving needs the owner column, and nothing else, of the tables only
-- ingest writes.
GRANT UPDATE (user_id) ON samples, sleep_segments, workouts, devices TO ryft_api;
GRANT INSERT, UPDATE ON user_sources TO ryft_api;
