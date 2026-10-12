#if APPLE_AI_SDK26
import Foundation
import Speech
import AVFoundation
import CoreMedia

/// macOS 26+: SpeechAnalyzer/SpeechTranscriber with system-managed speech assets (AssetInventory).
/// Only compiled with a macOS 26 SDK (APPLE_AI_SDK26); selected at runtime by `speechEngine()`.
@available(macOS 26.0, *)
struct ModernSpeech: SpeechEngine {
    func status() async -> Response {
        let installed=await SpeechTranscriber.installedLocales
        let supported=await SpeechTranscriber.supportedLocales
        let langs=supported.map { locale in Language(code:locale.identifier,name:displayLanguage(locale.identifier),installed:installed.contains(where: { $0.identifier==locale.identifier })) }
        return Response(supported:SpeechTranscriber.isAvailable,languages:langs)
    }
    func locale(for code: String) async throws -> Locale {
        guard code != "auto", let locale=await SpeechTranscriber.supportedLocale(equivalentTo:Locale(identifier:code)) else { try fail("apple_language_unsupported") }
        return locale
    }
    func prepare(_ code: String) async throws {
        let module=SpeechTranscriber(locale:try await locale(for:code),preset:.transcription)
        if let install=try await AssetInventory.assetInstallationRequest(supporting:[module]) { try await install.downloadAndInstall() }
    }
    func detectionLocales() async throws -> [Locale] {
        var seen=Set<String>()
        let locales=await SpeechTranscriber.installedLocales.sorted { $0.identifier < $1.identifier }.filter { seen.insert(languageKey($0)).inserted }
        guard !locales.isEmpty else { try fail("apple_speech_language_missing") }
        return locales
    }
    func transcribe(_ path: String, locale: Locale) async throws -> Transcript {
        guard await SpeechTranscriber.installedLocales.contains(where: { languageKey($0)==languageKey(locale) }) else { try fail("apple_speech_language_missing") }
        let module=SpeechTranscriber(locale:locale,transcriptionOptions:[],reportingOptions:[],attributeOptions:[.audioTimeRange,.transcriptionConfidence])
        let analyzer=SpeechAnalyzer(modules:[module])
        let collector=Task { () throws -> Transcript in
            var segments=[Segment](), weighted=0.0, weight=0.0
            for try await result in module.results {
                guard result.isFinal else { continue }
                var words=[Segment]()
                for run in result.text.runs {
                    if let range=run.audioTimeRange {
                        let text=String(result.text[run.range].characters)
                        let start=range.start.seconds, end=range.end.seconds
                        if start.isFinite && end.isFinite && end > start && !text.trimmingCharacters(in:.whitespacesAndNewlines).isEmpty {
                            words.append(Segment(start:start,end:end,text:text))
                            if let confidence=run.transcriptionConfidence {
                                let duration=end-start; weighted += Double(confidence)*duration; weight += duration
                            }
                        }
                    }
                }
                // A final result may have no word attributes. Its own range is still authoritative.
                if words.isEmpty {
                    let start=result.range.start.seconds, end=result.range.end.seconds
                    let text=String(result.text.characters).trimmingCharacters(in:.whitespacesAndNewlines)
                    if start.isFinite && end.isFinite && end > start && !text.isEmpty { words=[Segment(start:start,end:end,text:text)] }
                }
                segments.append(contentsOf:words)
            }
            return Transcript(segments:segments,confidence:weight > 0 ? weighted/weight : 0)
        }
        do {
            let file=try AVAudioFile(forReading:URL(fileURLWithPath:path))
            if let end=try await analyzer.analyzeSequence(from:file) { try await analyzer.finalizeAndFinish(through:end) }
            else { await analyzer.cancelAndFinishNow() }
            return try await collector.value
        } catch {
            await analyzer.cancelAndFinishNow(); collector.cancel(); _=try? await collector.value
            throw error
        }
    }
}
#endif
