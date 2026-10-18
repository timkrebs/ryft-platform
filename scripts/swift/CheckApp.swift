// Backwards compatibility: the app's *current* models must decode every endpoint
// it calls. The app decodes leniently (`try?`), so a mismatch would silently
// become nil or [] — hence the checks on the decoded values, not just success.
import Foundation

func expect(_ cond: Bool, _ msg: String) {
    if !cond {
        print("✗ \(msg)")
        exit(1)
    }
}

await decode([DailyScore].self, "/v1/days?to=\(today)")
let day = await decode(DayDetail.self, "/v1/days/\(today)")
expect(day.score.readiness != nil && day.score.state != nil, "app DailyScore lost readiness/state")
expect(day.sleep?.stages.count == 4 && day.sleep?.totalMin != nil, "app SleepDetail did not decode")
let week = await decode(SleepRange.self, "/v1/sleep?range=week&to=\(today)")
expect(!week.days.isEmpty && week.days.allSatisfy { $0.stages.count == 4 }, "app SleepRange days did not decode")
let workouts = await decode([Workout].self, "/v1/workouts?to=\(today)")
if let w = workouts.last {
    let d = await decode(WorkoutDetail.self, "/v1/workouts/\(w.id.uuidString.lowercased())")
    expect(!d.hr.isEmpty && d.zoneMin.count == 5 && d.trimp != nil, "app WorkoutDetail extras did not decode")
}
for m in TrendMetric.allCases {
    let t = await decode(TrendSeries.self, "/v1/trends?metric=\(m.rawValue)&range=30d&to=\(today)")
    expect(!t.points.isEmpty, "app TrendSeries \(m.rawValue) has no points")
}
await decode([Insight].self, "/v1/insights")
await decode(User.self, "/v1/me")
print("the app's current models decode every endpoint it uses")
