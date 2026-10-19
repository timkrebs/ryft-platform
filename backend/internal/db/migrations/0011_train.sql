-- Train (M3b): the exercise library, plans with an enrolment, logged strength
-- sessions and personal records (docs/DECISIONS.md D58–D62, docs/SCORING.md →
-- Strength). Ids are UUIDs the app generates, so it can log offline; built-in
-- exercises and plans (user_id NULL) come from the seeds the migrate job loads
-- (internal/db/seed) with fixed ids. Sessions and their rows are keyed by
-- (user_id, id) like devices and workouts.

CREATE TABLE IF NOT EXISTS exercises (
    id                uuid PRIMARY KEY,
    user_id           uuid REFERENCES users(id),          -- NULL = built-in
    slug              text NOT NULL,
    name_en           text NOT NULL,
    name_de           text NOT NULL,
    kind              text NOT NULL CHECK (kind IN ('barbell', 'dumbbell', 'kettlebell', 'bodyweight', 'machine', 'cable', 'cardio')),
    primary_muscles   text[] NOT NULL,
    secondary_muscles text[] NOT NULL DEFAULT '{}',
    equipment         text[] NOT NULL DEFAULT '{}',
    load_type         text NOT NULL CHECK (load_type IN ('weight', 'bodyweight', 'time', 'distance')),
    bodyweight_factor real NOT NULL DEFAULT 0,
    load_factor       real NOT NULL DEFAULT 1,             -- muscleFactor's per-exercise part (SCORING.md)
    is_unilateral     boolean NOT NULL DEFAULT false,
    archived          boolean NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
-- Slugs are unique per owner: once among the built-ins, once per user.
CREATE UNIQUE INDEX IF NOT EXISTS exercises_builtin_slug ON exercises (slug) WHERE user_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS exercises_user_slug ON exercises (user_id, slug) WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS plans (
    id            uuid PRIMARY KEY,
    user_id       uuid REFERENCES users(id),              -- NULL = built-in
    slug          text,                                    -- built-ins only
    name          text NOT NULL,
    goal          text NOT NULL CHECK (goal IN ('strength', 'hypertrophy', 'weightlifting', 'general')),
    weeks         int CHECK (weeks BETWEEN 1 AND 52),     -- NULL repeats forever
    days_per_week int NOT NULL CHECK (days_per_week BETWEEN 1 AND 7),
    description   text NOT NULL DEFAULT '',
    progression   jsonb,
    archived      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS plans_builtin_slug ON plans (slug) WHERE user_id IS NULL;
CREATE INDEX IF NOT EXISTS plans_user ON plans (user_id);

-- user_id repeats the plan's owner on its days and exercises, so every row can be
-- scoped (and, on Supabase, policed) without a join.
CREATE TABLE IF NOT EXISTS plan_days (
    id         uuid PRIMARY KEY,
    plan_id    uuid NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    user_id    uuid REFERENCES users(id),
    week       int,                                        -- NULL = the same every week
    day_index  int NOT NULL CHECK (day_index BETWEEN 0 AND 6),
    name       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS plan_days_plan ON plan_days (plan_id);

CREATE TABLE IF NOT EXISTS plan_exercises (
    id             uuid PRIMARY KEY,
    plan_day_id    uuid NOT NULL REFERENCES plan_days(id) ON DELETE CASCADE,
    user_id        uuid REFERENCES users(id),
    exercise_id    uuid NOT NULL REFERENCES exercises(id),
    position       int NOT NULL,
    sets           int NOT NULL CHECK (sets BETWEEN 1 AND 20),
    reps_min       int NOT NULL,
    reps_max       int NOT NULL,
    target_kind    text NOT NULL CHECK (target_kind IN ('pct_1rm', 'rpe', 'kg', 'bodyweight')),
    target_value   real,
    rest_sec       int NOT NULL DEFAULT 120,
    superset_group int,
    notes          text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (1 <= reps_min AND reps_min <= reps_max AND reps_max <= 100)
);
CREATE INDEX IF NOT EXISTS plan_exercises_day ON plan_exercises (plan_day_id);

CREATE TABLE IF NOT EXISTS plan_enrollments (
    id                uuid PRIMARY KEY,
    user_id           uuid NOT NULL REFERENCES users(id),
    plan_id           uuid NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    started_at        timestamptz NOT NULL DEFAULT now(),
    current_week      int NOT NULL DEFAULT 1,
    current_day_index int NOT NULL DEFAULT 0,
    status            text NOT NULL CHECK (status IN ('active', 'paused', 'completed')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS plan_enrollments_one_active ON plan_enrollments (user_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS plan_enrollments_user_plan ON plan_enrollments (user_id, plan_id);

CREATE TABLE IF NOT EXISTS sessions (
    user_id        uuid NOT NULL REFERENCES users(id),
    id             uuid NOT NULL,
    title          text NOT NULL DEFAULT '',
    started_at     timestamptz NOT NULL,
    ended_at       timestamptz,
    status         text NOT NULL CHECK (status IN ('in_progress', 'completed', 'discarded')),
    plan_day_id    uuid REFERENCES plan_days(id) ON DELETE SET NULL,
    workout_id     uuid,                                   -- the workout carrying its load (scoring)
    notes          text NOT NULL DEFAULT '',
    bodyweight_kg  real,
    muscular_trimp real,
    strain         real,
    volume_kg      real,
    device_id      uuid,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id)
);
CREATE INDEX IF NOT EXISTS sessions_user_started ON sessions (user_id, started_at);

CREATE TABLE IF NOT EXISTS session_exercises (
    user_id          uuid NOT NULL,
    id               uuid NOT NULL,
    session_id       uuid NOT NULL,
    exercise_id      uuid NOT NULL REFERENCES exercises(id),
    plan_exercise_id uuid REFERENCES plan_exercises(id) ON DELETE SET NULL,
    position         int NOT NULL,
    notes            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id),
    FOREIGN KEY (user_id, session_id) REFERENCES sessions (user_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS session_exercises_session ON session_exercises (user_id, session_id);
CREATE INDEX IF NOT EXISTS session_exercises_exercise ON session_exercises (user_id, exercise_id);

CREATE TABLE IF NOT EXISTS session_sets (
    user_id             uuid NOT NULL,
    id                  uuid NOT NULL,
    session_exercise_id uuid NOT NULL,
    set_index           int NOT NULL,
    reps                int,
    weight_kg           real,
    rpe                 real,
    duration_sec        int,
    distance_m          real,
    is_warmup           boolean NOT NULL DEFAULT false,
    completed_at        timestamptz,
    updated_at          timestamptz NOT NULL,              -- the app's clock: decides conflicts (D61)
    created_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id),
    FOREIGN KEY (user_id, session_exercise_id) REFERENCES session_exercises (user_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS session_sets_exercise ON session_sets (user_id, session_exercise_id);

-- One row per user, exercise and kind: the current best. set_id is no foreign key:
-- records are recomputed from the sets whenever a session changes.
CREATE TABLE IF NOT EXISTS personal_records (
    user_id     uuid NOT NULL REFERENCES users(id),
    exercise_id uuid NOT NULL REFERENCES exercises(id),
    kind        text NOT NULL CHECK (kind IN ('e1rm', 'max_weight', 'max_reps', 'max_volume')),
    value       real NOT NULL,
    set_id      uuid NOT NULL,
    session_id  uuid NOT NULL,
    achieved_at timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, exercise_id, kind)
);
CREATE INDEX IF NOT EXISTS personal_records_achieved ON personal_records (user_id, achieved_at);

-- The muscular load of a session lands on one workout (a matching HealthKit
-- workout, or one scoring creates with source 'ryft-session') and on the day.
ALTER TABLE workouts
    ADD COLUMN IF NOT EXISTS muscular_load double precision,
    ADD COLUMN IF NOT EXISTS session_id    uuid;

ALTER TABLE daily_scores
    ADD COLUMN IF NOT EXISTS muscular_trimp double precision,
    ADD COLUMN IF NOT EXISTS strength_min   int,
    ADD COLUMN IF NOT EXISTS volume_kg      double precision;

-- api: the app's read and write side for everything the user enters. It keeps
-- built-ins read-only in code; on Supabase RLS says the same (below).
GRANT SELECT, INSERT, UPDATE, DELETE ON exercises, plans, plan_days, plan_exercises, plan_enrollments,
    sessions, session_exercises, session_sets, personal_records TO ryft_api;

-- scoring: reads sessions and their exercises, writes the load it computes on the
-- session, and attaches it to a workout. It may create and delete only the
-- workouts it owns (source 'ryft-session'; enforced by RLS on Supabase), and on
-- the phone's workouts set only the attachment.
GRANT SELECT ON exercises, sessions, session_exercises, session_sets TO ryft_scoring;
GRANT UPDATE (muscular_trimp, strain, volume_kg, workout_id) ON sessions TO ryft_scoring;
GRANT INSERT (user_id, id, activity, start_at, end_at, source, session_id, muscular_load) ON workouts TO ryft_scoring;
GRANT UPDATE (muscular_load, session_id) ON workouts TO ryft_scoring;
GRANT DELETE ON workouts TO ryft_scoring;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['exercises', 'plans', 'plan_days', 'plan_exercises', 'plan_enrollments',
                             'sessions', 'session_exercises', 'session_sets', 'personal_records'] LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_services', t);
        EXECUTE format('CREATE POLICY %I ON %I TO ryft_scoring, ryft_api USING (true) WITH CHECK (true)',
                       t || '_services', t);
    END LOOP;
END $$;

-- Restrictive: whatever else allows it, scoring deletes and inserts only its own workouts.
DROP POLICY IF EXISTS workouts_scoring_own_delete ON workouts;
CREATE POLICY workouts_scoring_own_delete ON workouts AS RESTRICTIVE FOR DELETE TO ryft_scoring
    USING (source = 'ryft-session');
DROP POLICY IF EXISTS workouts_scoring_own_insert ON workouts;
CREATE POLICY workouts_scoring_own_insert ON workouts AS RESTRICTIVE FOR INSERT TO ryft_scoring
    WITH CHECK (source = 'ryft-session');

-- Supabase only, as in 0007: RLS on, built-ins readable by every signed-in user,
-- own rows writable, and no privileges for the Data API roles at all.
DO $$
DECLARE t text;
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    FOREACH t IN ARRAY ARRAY['exercises', 'plans', 'plan_days', 'plan_exercises'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY %I ON %I FOR SELECT TO authenticated
                          USING (user_id IS NULL OR user_id = (SELECT auth.uid()))', t || '_read_builtin_or_own_rows', t);
        EXECUTE format('CREATE POLICY %I ON %I FOR ALL TO authenticated
                          USING (user_id = (SELECT auth.uid())) WITH CHECK (user_id = (SELECT auth.uid()))',
                       t || '_write_own_rows', t);
    END LOOP;
    FOREACH t IN ARRAY ARRAY['plan_enrollments', 'sessions', 'session_exercises', 'session_sets', 'personal_records'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY %I ON %I FOR ALL TO authenticated
                          USING (user_id = (SELECT auth.uid())) WITH CHECK (user_id = (SELECT auth.uid()))',
                       t || '_own_rows', t);
    END LOOP;
    REVOKE ALL ON exercises, plans, plan_days, plan_exercises, plan_enrollments,
        sessions, session_exercises, session_sets, personal_records FROM anon, authenticated, service_role;
END $$;
