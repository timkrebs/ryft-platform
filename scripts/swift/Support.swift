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

func fetch(_ path: String, status want: Int = 200) async -> Data {
    var req = URLRequest(url: URL(string: apiURL + path)!)
    req.setValue("Bearer \(apiKey)", forHTTPHeaderField: "Authorization")
    do {
        let (data, resp) = try await URLSession.shared.data(for: req)
        let got = (resp as? HTTPURLResponse)?.statusCode ?? 0
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
