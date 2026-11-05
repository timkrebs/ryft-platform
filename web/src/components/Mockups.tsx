// Monochrome mock screens of the app, drawn in HTML/SVG inside the phone frame: no raster
// captures, so the marketing site stays black and white while the app's own redesign lands.
// Every value is sample data, the same numbers the app's previews use. Sizes are in
// container units of the frame (cqw), so a screen scales with the width it is given.
import { Gauge } from './charts';
import { Mark } from './Mark';
import { DeviceFrame } from './Screenshot';
import styles from './Mockups.module.css';

export const SAMPLE = {
  readiness: 84,
  recovery: 82,
  strain: 12.4,
  sleepHours: '7:48',
  sleepNeed: 94,
  hrv: 64,
  restingHr: 48,
  week: [71, 76, 68, 79, 74, 81, 84],
  sleepWeek: [6.9, 7.4, 6.2, 7.8, 7.1, 8.0, 7.8],
  strainWeek: [8.1, 14.2, 5.6, 11.9, 9.4, 15.8, 12.4],
  stages: [
    { id: 'deep', minutes: 92 },
    { id: 'light', minutes: 246 },
    { id: 'rem', minutes: 110 },
    { id: 'awake', minutes: 20 },
  ] as const,
  zones: [
    { zone: 1, minutes: 18 },
    { zone: 2, minutes: 24 },
    { zone: 3, minutes: 31 },
    { zone: 4, minutes: 12 },
    { zone: 5, minutes: 3 },
  ],
};

type Strings = {
  statusTime: string;
  tabs: string[];
};

const CHROME: Strings = { statusTime: '09:41', tabs: ['Today', 'Sleep', 'Strain', 'Train', 'Profile'] };

function Screen({ title, active, children }: { title: string; active: number; children: React.ReactNode }) {
  return (
    <div className={styles.screen} aria-hidden="true">
      <div className={styles.status}>
        <span>{CHROME.statusTime}</span>
        <Mark size={14} className={styles.statusMark} />
      </div>
      <h3 className={styles.screenTitle}>{title}</h3>
      <div className={styles.screenBody}>{children}</div>
      <ul className={styles.tabs}>
        {CHROME.tabs.map((tab, i) => (
          <li key={tab} className={i === active ? styles.tabActive : undefined}>
            {tab}
          </li>
        ))}
      </ul>
    </div>
  );
}

function Stat({ value, label }: { value: string; label: string }) {
  return (
    <div className={styles.stat}>
      <span className={styles.statValue}>{value}</span>
      <span className={styles.statLabel}>{label}</span>
    </div>
  );
}

/** Seven bars, today in ink, the rest in the raised surface. */
function WeekBars({ values, caption }: { values: number[]; caption?: string }) {
  const max = Math.max(...values);
  return (
    <div className={styles.week}>
      {caption && <p className={styles.weekCaption}>{caption}</p>}
      <svg viewBox="0 0 70 30" className={styles.bars} aria-hidden="true" focusable="false">
        {values.map((v, i) => {
          const h = (v / max) * 24;
          return (
            <g key={i}>
              <rect
                x={i * 10}
                y={24 - h}
                width="8"
                height={h.toFixed(1)}
                className={i === values.length - 1 ? styles.barToday : styles.barFill}
              />
              <text x={i * 10 + 4} y="29.5" textAnchor="middle" className={styles.barTick}>
                {'MTWTFSS'[i]}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

/** Today: the readiness gauge, the week, the three tiles. */
export function TodayScreen({
  labels,
}: {
  labels: { readiness: string; recovery: string; sleep: string; strain: string; hrv: string };
}) {
  return (
    <DeviceFrame>
      <Screen title="Today" active={0}>
        <div className={styles.gaugeWrap}>
          <Gauge value={SAMPLE.readiness} label={labels.readiness} size={150} />
        </div>
        <WeekBars values={SAMPLE.week} />
        <div className={styles.stats}>
          <Stat value={`${SAMPLE.recovery} %`} label={labels.recovery} />
          <Stat value={`${SAMPLE.sleepHours} h`} label={labels.sleep} />
          <Stat value={String(SAMPLE.strain)} label={labels.strain} />
          <Stat value={`${SAMPLE.hrv} ms`} label={labels.hrv} />
        </div>
      </Screen>
    </DeviceFrame>
  );
}

/** Sleep: hours against need, the stage bar, one row per stage. */
export function SleepScreen({
  labels,
}: {
  labels: {
    asleep: string;
    ofNeed: string;
    stages: Record<'deep' | 'light' | 'rem' | 'awake', string>;
    bedtime: string;
    week: string;
  };
}) {
  const total = SAMPLE.stages.reduce((s, x) => s + x.minutes, 0);
  const time = (m: number) => `${Math.floor(m / 60)}:${String(m % 60).padStart(2, '0')}`;
  return (
    <DeviceFrame>
      <Screen title="Sleep" active={1}>
        <p className={styles.big}>{SAMPLE.sleepHours}</p>
        <p className={styles.bigLabel}>
          {labels.asleep} · {SAMPLE.sleepNeed} % {labels.ofNeed}
        </p>
        <svg
          viewBox="0 0 100 6"
          preserveAspectRatio="none"
          className={styles.stageBar}
          aria-hidden="true"
          focusable="false"
        >
          {
            SAMPLE.stages.reduce<{ x: number; nodes: React.ReactNode[] }>(
              (acc, s) => {
                const w = (s.minutes / total) * 100;
                acc.nodes.push(
                  <rect
                    key={s.id}
                    x={acc.x.toFixed(2)}
                    y="0"
                    width={w.toFixed(2)}
                    height="6"
                    className={styles[`stage-${s.id}`]}
                  />,
                );
                acc.x += w;
                return acc;
              },
              { x: 0, nodes: [] },
            ).nodes
          }
        </svg>
        <ul className={styles.rows}>
          {SAMPLE.stages.map((s) => (
            <li key={s.id}>
              <span className={`${styles.swatch} ${styles[`stage-${s.id}`]}`} />
              <span>{labels.stages[s.id]}</span>
              <span className={styles.rowValue}>{time(s.minutes)}</span>
            </li>
          ))}
        </ul>
        <p className={styles.meta}>{labels.bedtime}</p>
        <WeekBars values={SAMPLE.sleepWeek} caption={labels.week} />
      </Screen>
    </DeviceFrame>
  );
}

/** Strain: today's number, five zones as bars, the session that made it. */
export function StrainScreen({
  labels,
}: {
  labels: { ofMax: string; band: string; zone: string; session: string; sessionValue: string; week: string };
}) {
  const max = Math.max(...SAMPLE.zones.map((z) => z.minutes));
  return (
    <DeviceFrame>
      <Screen title="Strain" active={2}>
        <p className={styles.big}>{SAMPLE.strain}</p>
        <p className={styles.bigLabel}>
          {labels.ofMax} · {labels.band}
        </p>
        <ul className={styles.zones}>
          {SAMPLE.zones.map((z) => (
            <li key={z.zone}>
              <span className={styles.zoneLabel}>
                {labels.zone} {z.zone}
              </span>
              <svg
                viewBox="0 0 100 4"
                preserveAspectRatio="none"
                className={styles.zoneTrack}
                aria-hidden="true"
                focusable="false"
              >
                <rect
                  x="0"
                  y="0"
                  width={Math.round((z.minutes / max) * 100)}
                  height="4"
                  className={styles.zoneFill}
                  data-zone={z.zone}
                />
              </svg>
              <span className={styles.zoneValue}>{z.minutes}′</span>
            </li>
          ))}
        </ul>
        <div className={styles.session}>
          <span>{labels.session}</span>
          <span className={styles.rowValue}>{labels.sessionValue}</span>
        </div>
        <WeekBars values={SAMPLE.strainWeek} caption={labels.week} />
      </Screen>
    </DeviceFrame>
  );
}
