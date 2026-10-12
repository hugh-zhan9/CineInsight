import Foundation
import Speech
import Translation
import NaturalLanguage
import AVFoundation
import CoreMedia
import AppKit
import SwiftUI

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
struct Transcript { var segments: [Segment]; var confidence: Double }

func languageKey(_ locale: Locale) -> String { locale.language.languageCode?.identifier ?? locale.identifier }
func displayLanguage(_ code: String) -> String { Locale(identifier:"zh_CN").localizedString(forIdentifier:code) ?? code }
func fail(_ code: String) throws -> Never { throw Failure(code:code) }
func emit(_ response: Response) {
    if let data=try? JSONEncoder().encode(response) { FileHandle.standardOutput.write(data); FileHandle.standardOutput.write(Data([10])) }
}
func sourceLocale(_ code: String) async throws -> Locale {
    guard code != "auto", let locale=await SpeechTranscriber.supportedLocale(equivalentTo:Locale(identifier:code)) else { try fail("apple_language_unsupported") }
    return locale
}
func installedSpeechLocales() async -> [Locale] {
    var seen=Set<String>()
    return await SpeechTranscriber.installedLocales.sorted { $0.identifier < $1.identifier }.filter { seen.insert(languageKey($0)).inserted }
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

func detect(_ path: String) async throws -> [Candidate] {
    let locales=await installedSpeechLocales()
    guard !locales.isEmpty else { try fail("apple_speech_language_missing") }
    var candidates=[Candidate]()
    for locale in locales {
        try Task.checkCancellation()
        let transcript=try await transcribe(path,locale:locale)
        let text=transcript.segments.map(\.text).joined()
        guard text.filter({ $0.isLetter }).count >= 8 else { continue }
        let recognizer=NLLanguageRecognizer(); recognizer.processString(text)
        let hypotheses=recognizer.languageHypotheses(withMaximum:3)
        let key=languageKey(locale)
        let consistency=hypotheses.filter { languageKey(Locale(identifier:$0.key.rawValue))==key }.map(\.value).max() ?? 0
        // Confidence alone is not calibrated across languages. Reject text inconsistent with its recognizer.
        if transcript.confidence >= 0.65 && consistency >= 0.6 {
            candidates.append(Candidate(language:locale.identifier,confidence:transcript.confidence*consistency))
        }
    }
    return candidates.sorted { $0.confidence > $1.confidence }
}

func translationSource(_ request: Request) throws -> Locale.Language {
    if let source=request.source, !source.isEmpty && source != "auto" { return Locale.Language(identifier:source) }
    let recognizer=NLLanguageRecognizer(); recognizer.processString((request.texts ?? []).joined(separator:"\n"))
    guard let best=recognizer.languageHypotheses(withMaximum:2).max(by: { $0.value < $1.value }), best.value >= 0.6 else { try fail("apple_language_uncertain") }
    return Locale.Language(identifier:best.key.rawValue)
}

@MainActor
struct TranslationPreparation: View {
    let source: Locale.Language
    let target: Locale.Language
    @State var configuration: TranslationSession.Configuration?
    var body: some View {
        VStack(spacing:16) {
            Text("准备系统翻译语言").font(.title2)
            Text("由 macOS 下载和管理语言资源。准备完成后可在本机翻译字幕。").multilineTextAlignment(.center)
            ProgressView()
            Button("取消") { emit(Response(error:"cancelled")); exit(0) }
        }.padding(28).frame(width:420,height:180)
        .task { configuration=TranslationSession.Configuration(source:source,target:target) }
        .translationTask(configuration) { session in
            do { try await session.prepareTranslation(); emit(Response(supported:true)) }
            catch { emit(Response(error:errorCode(error))) }
            exit(0)
        }
    }
}
@MainActor
func prepareTranslation(_ request: Request) throws {
    guard let source=request.source, source != "auto", let target=request.target else { try fail("apple_source_language_required") }
    let app=NSApplication.shared
    app.setActivationPolicy(.regular)
    let window=NSWindow(contentRect:NSRect(x:0,y:0,width:476,height:236),styleMask:[.titled],backing:.buffered,defer:false)
    window.title="析微影策 · 系统语言资源"
    window.contentView=NSHostingView(rootView:TranslationPreparation(source:Locale.Language(identifier:source),target:Locale.Language(identifier:target)))
    window.center(); window.makeKeyAndOrderFront(nil); app.activate(ignoringOtherApps:true)
    app.run()
}
func errorCode(_ error: Error) -> String {
    if let failure=error as? Failure { return failure.code }
    if error is CancellationError { return "cancelled" }
    if TranslationError.notInstalled ~= error { return "apple_translation_language_missing" }
    if TranslationError.unsupportedSourceLanguage ~= error || TranslationError.unsupportedTargetLanguage ~= error || TranslationError.unsupportedLanguagePairing ~= error { return "apple_language_unsupported" }
    if TranslationError.unableToIdentifyLanguage ~= error { return "apple_language_uncertain" }
    return "apple_system_failed"
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
            switch request.operation {
            case "status":
                let installed=await SpeechTranscriber.installedLocales
                let supported=await SpeechTranscriber.supportedLocales
                let langs=supported.map { locale in Language(code:locale.identifier,name:displayLanguage(locale.identifier),installed:installed.contains(where: { $0.identifier==locale.identifier })) }
                let translation=await LanguageAvailability().supportedLanguages.map { Language(code:$0.minimalIdentifier,name:displayLanguage($0.minimalIdentifier),installed:false) }
                emit(Response(supported:SpeechTranscriber.isAvailable,languages:langs,translationLanguages:translation))
            case "prepare_speech":
                let locale=try await sourceLocale(request.source ?? "")
                let module=SpeechTranscriber(locale:locale,preset:.transcription)
                if let install=try await AssetInventory.assetInstallationRequest(supporting:[module]) { try await install.downloadAndInstall() }
                emit(Response(supported:true))
            case "detect":
                guard let path=request.path else { try fail("apple_invalid_request") }
                let candidates=try await detect(path)
                guard let first=candidates.first, first.confidence >= 0.7, candidates.count==1 || first.confidence-candidates[1].confidence >= 0.1 else {
                    emit(Response(error:"apple_language_uncertain",candidates:candidates)); return
                }
                emit(Response(language:first.language,candidates:candidates))
            case "transcribe":
                guard let path=request.path else { try fail("apple_invalid_request") }
                let locale=try await sourceLocale(request.source ?? "")
                let result=try await transcribe(path,locale:locale)
                emit(Response(language:locale.identifier,segments:result.segments))
            case "translate":
                let texts=request.texts ?? []
                if texts.isEmpty { emit(Response(translations:[])); return }
                guard texts.count <= 50, let target=request.target, !target.isEmpty else { try fail("apple_invalid_request") }
                let source=try translationSource(request), destination=Locale.Language(identifier:target)
                if source.languageCode == destination.languageCode { emit(Response(language:source.minimalIdentifier,translations:texts)); return }
                let session=TranslationSession(installedSource:source,target:destination)
                let batch=texts.enumerated().map { TranslationSession.Request(sourceText:$0.element,clientIdentifier:String($0.offset)) }
                let results=try await session.translations(from:batch)
                var translated=Array<String?>(repeating:nil,count:texts.count)
                for result in results {
                    guard let raw=result.clientIdentifier, let index=Int(raw), translated.indices.contains(index), translated[index]==nil else { try fail("apple_translation_invalid_result") }
                    translated[index]=result.targetText
                }
                guard translated.allSatisfy({ $0 != nil }) else { try fail("apple_translation_invalid_result") }
                emit(Response(language:source.minimalIdentifier,translations:translated.map { $0! }))
            case "prepare_translation": try prepareTranslation(request)
            default: try fail("apple_invalid_request")
            }
        } catch { emit(Response(error:errorCode(error))) }
    }
}
