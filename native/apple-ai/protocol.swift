import Foundation

/// JSON request/response types of the one-request-per-process helper protocol, plus internal results.
struct Request: Decodable {
    var operation: String
    var path: String?
    var source: String?
    var target: String?
    var texts: [String]?
}
struct Segment: Codable { var start: Double; var end: Double; var text: String }
struct Language: Encodable { var code: String; var name: String; var installed: Bool }
struct Candidate: Encodable { var language: String; var confidence: Double }
struct Response: Encodable {
    var error: String?
    var supported: Bool?
    var languages: [Language]?
    var translationLanguages: [Language]?
    var language: String?
    var segments: [Segment]?
    var translations: [String]?
    var candidates: [Candidate]?
}
struct Failure: Error { let code: String }
/// `confidence` is nil when the recognizer reported none (the macOS 15 recognizer may not).
struct Transcript { var segments: [Segment]; var confidence: Double? }
