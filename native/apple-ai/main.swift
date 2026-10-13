import Foundation
import NaturalLanguage

/// Test hook: CINEINSIGHT_APPLE_AI_LEGACY=1 runs the macOS 15 paths (on-device SFSpeechRecognizer and the
/// hidden-window translation session) on macOS 26+, so they can be exercised on a newer Mac.
let legacyForced = ProcessInfo.processInfo.environment["CINEINSIGHT_APPLE_AI_LEGACY"] == "1"

func languageKey(_ locale: Locale) -> String { locale.language.languageCode?.identifier ?? locale.identifier }
func displayLanguage(_ code: String) -> String { Locale(identifier:"zh_CN").localizedString(forIdentifier:code) ?? code }
func fail(_ code: String) throws -> Never { throw Failure(code:code) }
func emit(_ response: Response) {
    if let data=try? JSONEncoder().encode(response) { FileHandle.standardOutput.write(data); FileHandle.standardOutput.write(Data([10])) }
}

/// System speech recognition: SpeechAnalyzer on macOS 26+, on-device SFSpeechRecognizer on macOS 15.
protocol SpeechEngine {
    /// Fills `supported` and `languages`; the caller adds translation languages.
    func status() async throws -> Response
    func prepare(_ code: String) async throws
    func locale(for code: String) async throws -> Locale
    /// Installed recognition locales, one per language: the candidates for language detection.
    func detectionLocales() async throws -> [Locale]
    func transcribe(_ path: String, locale: Locale) async throws -> Transcript
}
func speechEngine() -> any SpeechEngine {
#if APPLE_AI_SDK26
    if #available(macOS 26.0, *), !legacyForced { return ModernSpeech() }
#endif
    return LegacySpeech()
}

/// Candidates best first. `calibrated` is false when any was ranked without a transcription confidence.
func detect(_ path: String, engine: any SpeechEngine) async throws -> (candidates: [Candidate], calibrated: Bool) {
    var candidates=[Candidate](), calibrated=true
    for locale in try await engine.detectionLocales() {
        try Task.checkCancellation()
        let transcript=try await engine.transcribe(path,locale:locale)
        let text=transcript.segments.map(\.text).joined()
        guard text.filter({ $0.isLetter }).count >= 8 else { continue }
        let recognizer=NLLanguageRecognizer(); recognizer.processString(text)
        let hypotheses=recognizer.languageHypotheses(withMaximum:3)
        let key=languageKey(locale)
        let consistency=hypotheses.filter { languageKey(Locale(identifier:$0.key.rawValue))==key }.map(\.value).max() ?? 0
        if let score=candidateScore(confidence:transcript.confidence,consistency:consistency) {
            candidates.append(Candidate(language:locale.identifier,confidence:score))
            if transcript.confidence == nil { calibrated=false }
        }
    }
    return (candidates.sorted { $0.confidence > $1.confidence },calibrated)
}

@main struct AppleAI {
    static func main() async {
        if CommandLine.arguments.contains("--face-worker") { exit(Int32(CineFaceWorker())) }
        do {
            let data=FileHandle.standardInput.readDataToEndOfFile()
            guard data.count <= 8*1024*1024 else { try fail("apple_request_too_large") }
            let request=try JSONDecoder().decode(Request.self,from:data)
            if ["face_status", "image_status", "image_features"].contains(request.operation) {
                let result=String(decoding:data,as:UTF8.self).withCString { CineVisionRequest($0) }!
                defer { free(result) }
                FileHandle.standardOutput.write(Data(String(cString:result).utf8))
                return
            }
            let speech=speechEngine()
            switch request.operation {
            case "status":
                var response=try await speech.status()
                response.translationLanguages=await translationLanguages()
                emit(response)
            case "prepare_speech":
                try await speech.prepare(request.source ?? "")
                emit(Response(supported:true))
            case "detect":
                guard let path=request.path else { try fail("apple_invalid_request") }
                let detection=try await detect(path,engine:speech)
                guard let language=detectedLanguage(detection.candidates,calibrated:detection.calibrated) else {
                    emit(Response(error:"apple_language_uncertain",candidates:detection.candidates)); return
                }
                emit(Response(language:language,candidates:detection.candidates))
            case "transcribe":
                guard let path=request.path else { try fail("apple_invalid_request") }
                let locale=try await speech.locale(for:request.source ?? "")
                let result=try await speech.transcribe(path,locale:locale)
                emit(Response(language:locale.identifier,segments:result.segments))
            case "translate":
                let job=try TranslationJob(request)
                if let response=job.immediate { emit(response); return }
#if APPLE_AI_SDK26
                if #available(macOS 26.0, *), !legacyForced { emit(try await translateInstalled(job)); return }
#endif
                // macOS 15 runs NSApplication; reached without any earlier suspension of main (see the function).
                hiddenSessionTranslate(job)
            case "prepare_translation": try prepareTranslation(request)
            default: try fail("apple_invalid_request")
            }
        } catch { emit(Response(error:errorCode(error))) }
    }
}
