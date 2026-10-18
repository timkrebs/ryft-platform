-- Ryft initial schema. Single-tenant by default (one seeded user), but every
-- table carries user_id so multi-user is an auth change, not a schema change.

CREATE TABLE IF NOT EXISTS users (
    id          uuid PRIMARY KEY,
    name        text NOT NULL,
    tz          text NOT NULL DEFAULT 'Europe/Berlin',
    max_hr      smallint NOT NULL DEFAULT 190,   -- used for heart-rate-reserve in strain
    sleep_base_min int NOT NULL DEFAULT 460,     -- baseline sleep need (7h40)
    created_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO users (id, name) VALUES ('00000000-0000-0000-0000-000000000001', 'owner')
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS devices (
    id           uuid PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id),
    name         text NOT NULL DEFAULT '',
    last_sync_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Raw time series from HealthKit. `type` is a closed vocabulary shared with the
-- iOS app: heart_rate, hrv_sdnn, resting_hr, respiratory_rate, spo2, vo2max,
-- active_energy, steps.
CREATE TABLE IF NOT EXISTS samples (
    id         bigserial PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id),
    type       text NOT NULL,
    start_at   timestamptz NOT NULL,
    end_at     timestamptz NOT NULL,
    value      double precision NOT NULL,
    unit       text NOT NULL DEFAULT '',
    source     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, type, start_at, end_at, source)
);
CREATE INDEX IF NOT EXISTS samples_user_type_start ON samples (user_id, type, start_at);

-- HealthKit sleep analysis segments. stage: inbed, asleep, core, deep, rem, awake.
CREATE TABLE IF NOT EXISTS sleep_segments (
    id         bigserial PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id),
    start_at   timestamptz NOT NULL,
    end_at     timestamptz NOT NULL,
    stage      text NOT NULL,
    source     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, start_at, end_at, stage, source)
);
CREATE INDEX IF NOT EXISTS sleep_user_end ON sleep_segments (user_id, end_at);

CREATE TABLE IF NOT EXISTS workouts (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users(id),
    activity    text NOT NULL,
    start_at    timestamptz NOT NULL,
    end_at      timestamptz NOT NULL,
    avg_hr      double precision,
    max_hr      double precision,
    energy_kcal double precision,
    distance_m  double precision,
    source      text NOT NULL DEFAULT '',
    strain      double precision,                 -- filled by scoring
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS workouts_user_start ON workouts (user_id, start_at);

-- One row per local calendar day, written by the scoring service.
CREATE TABLE IF NOT EXISTS daily_scores (
    user_id           uuid NOT NULL REFERENCES users(id),
    day               date NOT NULL,
    recovery          smallint,        -- 1..100
    strain            double precision,            -- 0..21
    sleep_performance smallint,        -- 0..100
    sleep_need_min    int,
    sleep_actual_min  int,
    sleep_debt_min    int,
    hrv_ms            double precision,
    rhr               double precision,
    resp_rate         double precision,
    spo2              double precision,
    calibrating       boolean NOT NULL DEFAULT true,
    details           jsonb NOT NULL DEFAULT '{}'::jsonb,
    computed_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, day)
);
