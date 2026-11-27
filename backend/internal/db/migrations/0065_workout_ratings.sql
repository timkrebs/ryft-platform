-- Recovery without a watch: the session RPE of a workout (docs/DECISIONS.md D291–D295, D293; docs/API.md →
-- PUT /v1/workouts/{id}/rpe). The user's own rating of how hard a workout felt, 1–10 in steps of 0.5 (Foster et
-- al. 2001); the coaching load counts it where heart rate and power cannot. One row per workout.
--
-- The rating belongs to the workout, not to a HealthKit id: a sync of the same HealthKit workout upserts the
-- workouts row and leaves this one, the same-source fold and a session's re-attach move it to the surviving
-- workout BEFORE they delete or detach the old one (store: fold.go, train.go), and a HealthKit deletion of the
-- workout removes it through the foreign key below (ON DELETE CASCADE, run by the owner, so no service needs a
-- grant on this table to delete workouts).
--
-- The key to workouts is its primary key (user_id, id): since 0007 a workout id is unique per user only (one iPhone
-- can be signed into by two accounts), so a foreign key on id alone has no unique constraint to point at. The
-- composite key also makes the database keep the rating's user_id equal to the workout's. ON UPDATE CASCADE: the
-- account merge moves a workout with UPDATE workouts SET user_id, and its rating follows it (the cascade runs as
-- the owner).
--
-- Written and read by the api on its request path (the coaching load is computed in the api's store). ingest
-- gets the one thing the fold needs: the same-source fold runs in ingest's sync (store.foldTx, and the
-- fold-workouts command), so it must move a dropped duplicate's rating to the survivor before it deletes the
-- duplicate (the delete cascades, as the owner, to a rating the survivor already has): it may read the key columns
-- and updated_at (the latest rating wins) and change which workout a rating points at (and when), never read or
-- write a value, create one or delete one. scoring gets the same without updated_at: it re-attaches a Train
-- session to another workout, or back to its own (store.ReleaseSessionWorkouts), and the rating moves with the
-- session (SELECT of the two key columns, UPDATE of workout_id and updated_at; it never reads a rating's value).
-- A completed session's own workout (source 'ryft-session') whose times were edited is updated in place
-- (store.EnsureSessionWorkout, through the function scoring_follow_session_times below, which takes no times), not
-- deleted and inserted again, so its rating stays. Day strain does not use the rating (D293). notify gets nothing.
-- The account's deletion and merge and the export read it from the store; ON DELETE CASCADE on user_id is the
-- safety net for the users row.
--
-- Additive: a new table, nothing renamed or dropped. Rollback = re-pin the old images; the table stays and nothing
-- old reads it. The migrator runs the file in one transaction, so the foreign keys' locks on users and workouts
-- (SHARE ROW EXCLUSIVE: writes of both wait, reads don't) are held to the commit; the table is new and empty, so
-- nothing is scanned, and lock_timeout bounds the wait (D116).
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS workout_ratings (
    user_id     uuid          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workout_id  uuid          NOT NULL,
    -- 1, 1.5, ..., 10: a multiple of 0.5 within the range
    session_rpe numeric(3,1)  NOT NULL CONSTRAINT workout_ratings_rpe_check
                                  CHECK (session_rpe BETWEEN 1 AND 10 AND mod(session_rpe * 2, 1) = 0),
    created_at  timestamptz   NOT NULL DEFAULT now(),
    updated_at  timestamptz   NOT NULL DEFAULT now(),
    -- the cascade from workouts (a HealthKit deletion, the fold) and the move of a rating find it by this key
    PRIMARY KEY (user_id, workout_id),
    CONSTRAINT workout_ratings_workout_fkey FOREIGN KEY (user_id, workout_id) REFERENCES workouts (user_id, id)
        ON DELETE CASCADE ON UPDATE CASCADE
);

-- api: the app's read and write side, the account's deletion and the merge.
GRANT SELECT, INSERT, UPDATE, DELETE ON workout_ratings TO ryft_api;
-- ingest: the fold's move of a rating to the surviving copy (which of two ratings is the latest: updated_at), never
-- its value and never a delete (a dropped copy's rating goes with its workout through the foreign key).
GRANT SELECT (user_id, workout_id, updated_at) ON workout_ratings TO ryft_ingest;
GRANT UPDATE (workout_id, updated_at) ON workout_ratings TO ryft_ingest;
-- scoring: the re-attach's move of a rating with its session, by key columns only.
GRANT SELECT (user_id, workout_id) ON workout_ratings TO ryft_scoring;
GRANT UPDATE (workout_id, updated_at) ON workout_ratings TO ryft_scoring;
-- scoring: a completed session's own workout follows an edit of the session's times in place, so its rating stays.
-- Scoring gets no column grant on start_at/end_at (a column grant is per column, not per row: it would let scoring
-- rewrite any workout's times, and the session's own workouts of every account). It gets one function instead, which
-- takes no times: it sets the workout of source 'ryft-session' to the times of the owner's completed session the
-- workout belongs to (same user_id, workouts.session_id), the end as scoring reads it (ended_at, else the last
-- completed set, else the start: scoring.SessionEnd), and reports whether it found such a session. SECURITY DEFINER
-- (it writes with the owner's rights), a fixed search_path and schema-qualified names; EXECUTE for ryft_scoring only.
-- The two statements below remove what an earlier draft of this file created (a column grant and a trigger).
REVOKE UPDATE (start_at, end_at) ON workouts FROM ryft_scoring;
DROP TRIGGER IF EXISTS workouts_scoring_moves_own_only ON workouts;
DROP FUNCTION IF EXISTS workouts_scoring_moves_own_only();

CREATE OR REPLACE FUNCTION public.scoring_follow_session_times(p_user uuid, p_workout uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    UPDATE public.workouts w SET start_at = s.started_at,
        end_at = COALESCE(s.ended_at, GREATEST(s.started_at, (
            SELECT max(st.completed_at) FROM public.session_sets st
            JOIN public.session_exercises e ON e.user_id = st.user_id AND e.id = st.session_exercise_id
            WHERE e.user_id = s.user_id AND e.session_id = s.id)))
    FROM public.sessions s
    WHERE w.user_id = p_user AND w.id = p_workout AND w.source = 'ryft-session' AND w.session_id IS NOT NULL
      AND s.user_id = w.user_id AND s.id = w.session_id AND s.status = 'completed';
    RETURN FOUND;
END $$;
REVOKE ALL ON FUNCTION public.scoring_follow_session_times(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.scoring_follow_session_times(uuid, uuid) TO ryft_scoring;

DROP POLICY IF EXISTS workout_ratings_services ON workout_ratings;
CREATE POLICY workout_ratings_services ON workout_ratings TO ryft_api, ryft_ingest, ryft_scoring USING (true) WITH CHECK (true);

-- Supabase only, as in 0007 and 0016: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE workout_ratings ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON workout_ratings FROM anon, authenticated, service_role;
    -- Supabase's default privileges give these roles EXECUTE on every new function in public: not on this one.
    REVOKE ALL ON FUNCTION public.scoring_follow_session_times(uuid, uuid) FROM anon, authenticated, service_role;
END $$;
