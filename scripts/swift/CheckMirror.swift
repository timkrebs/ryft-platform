// Decodes every endpoint with the Swift mirror from docs/API.md.
import Foundation

let days = await decode([DailyScore].self, "/v1/days?to=\(today)")
let detail = await decode(DayDetail.self, "/v1/days/\(today)")
precondition(detail.sleep != nil && detail.strain != nil, "today must have sleep and strain: run make smoke first (it syncs synthetic days up to today)")
// Today may have no steps yet (the synthetic day starts moving at ~06:30 UTC).
precondition(days.contains { $0.steps != nil && $0.activeKcal != nil }, "a day must have steps and active kcal")
for d in days.suffix(4) {
    await decode(DayDetail.self, "/v1/days/\(d.day)")   // DayStrain.sessions on the day check_api trained
}
await decode(SleepRange.self, "/v1/sleep?range=week&to=\(today)")
await decode(SleepRange.self, "/v1/sleep?range=month&to=\(today)")
if let need = detail.sleep?.need {
    precondition(need.total == need.base + need.strain + need.debt - need.napCredit, "the need terms must add up")
}
let plan = await decode(SleepPlan.self, "/v1/sleep/plan?wake=07:00")
precondition(plan.bedtime100 < plan.bedtime85 && plan.bedtime85 < plan.bedtime70 && plan.bedtime70 < plan.wake,
             "plan bedtimes must ascend towards the wake time")
await decode(APIErrorBody.self, "/v1/sleep/plan?wake=7am", status: 422)
let workouts = await decode([Workout].self, "/v1/workouts?to=\(today)")
if let w = workouts.last {
    await decode(WorkoutDetail.self, "/v1/workouts/\(w.id.uuidString.lowercased())")
}
for metric in ["readiness", "recovery", "strain", "sleep", "sleep_min", "hrv", "rhr", "steps", "active_kcal",
               "strength_min", "volume_kg", "sleep_quality", "sleep_consistency", "nap_min"] {
    await decode(Trend.self, "/v1/trends?metric=\(metric)&range=30d&to=\(today)")
}
await decode([Insight].self, "/v1/insights")

// Train
let library = await decode([Exercise].self, "/v1/exercises")
precondition(library.count >= 150, "the built-in library must be there")
if let squat = library.first(where: { $0.slug == "back-squat" }) {
    await decode(ExerciseHistory.self, "/v1/exercises/\(squat.id.uuidString.lowercased())/history")
}
let overview = await decode(PlansOverview.self, "/v1/plans")
for p in overview.builtin + overview.mine {
    await decode(Plan.self, "/v1/plans/\(p.id.uuidString.lowercased())")
}
if overview.active != nil {
    await decode(PlanToday.self, "/v1/plans/today")
}
let sessions = await decode([SessionSummary].self, "/v1/sessions")
for s in sessions.prefix(3) {
    await decode(Session.self, "/v1/sessions/\(s.id.uuidString.lowercased())")
}
let behaviors = await decode([Behavior].self, "/v1/behaviors")
precondition(behaviors.filter { !$0.isCustom }.count == 25, "the built-in behaviors")
await decode(JournalDay.self, "/v1/journal/\(today)")
let impact = await decode(BehaviorImpacts.self, "/v1/behaviors/impact")
precondition(impact.windowDays == 90, "impact window")
let me = await decode(User.self, "/v1/me")
precondition(Set(me.notifications.categories.keys) == Set(NotificationCategory.allCases.map(\.rawValue)), "every category")
let config = await decode(RemoteConfig.self, "/v1/config")
precondition(config.ttlSec == 21600 && config.flags["train.enabled"] != nil, "remote config")
let syncStatus = await decode(SyncStatus.self, "/v1/me/sync")
precondition(syncStatus.today.day == today, "today in the user's time zone")
let modelInfo = await decode(ModelInfo.self, "/v1/model")
precondition(modelInfo.changelog.first?.version == modelInfo.current, "the newest changelog entry is the current model")
let patch = try! JSONEncoder().encode(NotificationPatch(quietHours: .some(nil)))
precondition(String(data: patch, encoding: .utf8) == #"{"quiet_hours":null}"#, "a cleared quiet_hours encodes null")
await decode(APIErrorBody.self, "/v1/trends?metric=nope", status: 422)
let notFound = await decode(APIErrorBody.self, "/v1/nope", status: 404)
precondition(notFound.requestId != nil, "error bodies must carry request_id")
print("Swift mirror decodes every endpoint")
