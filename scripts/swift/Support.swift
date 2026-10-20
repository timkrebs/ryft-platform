// Shared by both checks: the iOS app's decoder settings (APIClient.jsonDecoder)
// and a tiny fetch helper.
import Foundation

let env = ProcessInfo.processInfo.environment
let apiURL = env["API_URL"] ?? "http://localhost:8082"
let apiKey = env["API_KEY"] ?? "dev-api-key-change-me"
let today = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : ""

let decoder: JSONDecoder = {
    let d = JSONDecoder()
    d.keyDecodingStrategy = .convertFromSnakeCase
    let frac = ISO8601DateFormatter()
    frac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let plain = ISO8601DateFormatter()
    plain.formatOptions = [.withInternetDateTime]
    d.dateDecodingStrategy = .custom { decoder in
        let s = try decoder.singleValueContainer().decode(String.self)
        if let date = frac.date(from: s) ?? plain.date(from: s) { return date }
        throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "bad date \(s)"))
    }
    return d
}()

// fetch waits out a 429 (Retry-After, up to three times) as the app does: run
// right after `make smoke`, the checks start with the user's burst used up.
func fetch(_ path: String, status want: Int = 200) async -> Data {
    var req = URLRequest(url: URL(string: apiURL + path)!)
    req.setValue("Bearer \(apiKey)", forHTTPHeaderField: "Authorization")
    do {
        var (data, resp) = try await URLSession.shared.data(for: req)
        var got = (resp as? HTTPURLResponse)?.statusCode ?? 0
        for _ in 0..<3 where got == 429 && want != 429 {
            let after = Double((resp as? HTTPURLResponse)?.value(forHTTPHeaderField: "Retry-After") ?? "") ?? 1
            try await Task.sleep(nanoseconds: UInt64(min(max(after, 1), 5) * 1_000_000_000))
            (data, resp) = try await URLSession.shared.data(for: req)
            got = (resp as? HTTPURLResponse)?.statusCode ?? 0
        }
        if got != want {
            print("✗ GET \(path): status \(got), want \(want)")
            exit(1)
        }
        return data
    } catch {
        print("✗ GET \(path): \(error)")
        exit(1)
    }
}

@discardableResult
func decode<T: Decodable>(_ type: T.Type, _ path: String, status: Int = 200) async -> T {
    let data = await fetch(path, status: status)
    do {
        let v = try decoder.decode(T.self, from: data)
        print("✓ \(T.self) ← \(path)")
        return v
    } catch {
        print("✗ \(T.self) ← \(path): \(error)")
        exit(1)
    }
}
