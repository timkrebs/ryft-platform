// Decodes every endpoint with the Swift mirror from docs/API.md.
import Foundation

// Unchanged from the app's Models.swift; the mirror references it.
struct Workout: Codable, Identifiable {
    let id: UUID
    let activity: String
    let start: Date
    let end: Date
    let avgHr: Double?
    let maxHr: Double?
    let energyKcal: Double?
    let distanceM: Double?
    let source: String
    var strain: Double?
}

await decode([DailyScore].self, "/v1/days?to=\(today)")
let detail = await decode(DayDetail.self, "/v1/days/\(today)")
precondition(detail.sleep != nil && detail.strain != nil, "today must have sleep and strain")
await decode(SleepRange.self, "/v1/sleep?range=week&to=\(today)")
await decode(SleepRange.self, "/v1/sleep?range=month&to=\(today)")
let workouts = await decode([Workout].self, "/v1/workouts?to=\(today)")
if let w = workouts.last {
    await decode(WorkoutDetail.self, "/v1/workouts/\(w.id.uuidString.lowercased())")
}
for metric in ["readiness", "recovery", "strain", "sleep", "sleep_min", "hrv", "rhr"] {
    await decode(Trend.self, "/v1/trends?metric=\(metric)&range=30d&to=\(today)")
}
await decode([Insight].self, "/v1/insights")
await decode(User.self, "/v1/me")
await decode(APIErrorBody.self, "/v1/trends?metric=nope", status: 422)
let notFound = await decode(APIErrorBody.self, "/v1/nope", status: 404)
precondition(notFound.requestId != nil, "error bodies must carry request_id")
print("Swift mirror decodes every endpoint")
