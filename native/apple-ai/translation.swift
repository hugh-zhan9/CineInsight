import Foundation
import Translation
import NaturalLanguage
import AppKit
import SwiftUI

func translationSource(_ request: Request) throws -> Locale.Language {
    if let source=request.source, !source.isEmpty && source != "auto" { return Locale.Language(identifier:source) }
    let recognizer=NLLanguageRecognizer(); recognizer.processString((request.texts ?? []).joined(separator:"\n"))
    guard let best=recognizer.languageHypotheses(withMaximum:2).max(by: { $0.value < $1.value }), best.value >= 0.6 else { try fail("apple_language_uncertain") }
    return Locale.Language(identifier:best.key.rawValue)
}
func translationLanguages() async -> [Language] {
    await LanguageAvailability().supportedLanguages.map { Language(code:$0.minimalIdentifier,name:displayLanguage($0.minimalIdentifier),installed:false) }
}

/// A validated batch of at most 50 lines. `immediate` answers empty and same-language batches without a session.
struct TranslationJob {
    var batch=[TranslationSession.Request]()
    var source=Locale.Language(identifier:"und"), target=Locale.Language(identifier:"und")
    var immediate: Response?
    init(_ request: Request) throws {
        let texts=request.texts ?? []
        if texts.isEmpty { immediate=Response(translations:[]); return }
        guard texts.count <= 50, let target=request.target, !target.isEmpty else { try fail("apple_invalid_request") }
        source=try translationSource(request); self.target=Locale.Language(identifier:target)
        if source.languageCode == self.target.languageCode { immediate=Response(language:source.minimalIdentifier,translations:texts); return }
        batch=texts.enumerated().map { TranslationSession.Request(sourceText:$0.element,clientIdentifier:String($0.offset)) }
    }
    /// Restores input order by client identifier; missing, duplicate or out-of-range results are rejected.
    func response(_ results: [TranslationSession.Response]) throws -> Response {
        var translated=Array<String?>(repeating:nil,count:batch.count)
        for result in results {
            guard let raw=result.clientIdentifier, let index=Int(raw), translated.indices.contains(index), translated[index]==nil else { try fail("apple_translation_invalid_result") }
            translated[index]=result.targetText
        }
        guard translated.allSatisfy({ $0 != nil }) else { try fail("apple_translation_invalid_result") }
        return Response(language:source.minimalIdentifier,translations:translated.map { $0! })
    }
}
#if APPLE_AI_SDK26
/// macOS 26+: a background session limited to installed languages; it never downloads or shows UI.
@available(macOS 26.0, *)
func translateInstalled(_ job: TranslationJob) async throws -> Response {
    try job.response(try await TranslationSession(installedSource:job.source,target:job.target).translations(from:job.batch))
}
#endif
func errorCode(_ error: Error) -> String {
    if let failure=error as? Failure { return failure.code }
    if error is CancellationError { return "cancelled" }
#if APPLE_AI_SDK26
    if #available(macOS 26.0, *), TranslationError.notInstalled ~= error { return "apple_translation_language_missing" }
#endif
    if TranslationError.unsupportedSourceLanguage ~= error || TranslationError.unsupportedTargetLanguage ~= error || TranslationError.unsupportedLanguagePairing ~= error { return "apple_language_unsupported" }
    if TranslationError.unableToIdentifyLanguage ~= error { return "apple_language_uncertain" }
    return "apple_system_failed"
}

@MainActor
struct TranslationPreparation: View {
    // One fixed language pair per helper invocation; no mutable SwiftUI state is needed.
    let configuration: TranslationSession.Configuration
    var body: some View {
        VStack(spacing:16) {
            Text("准备系统翻译语言").font(.title2)
            Text("由 macOS 下载和管理语言资源。准备完成后可在本机翻译字幕。").multilineTextAlignment(.center)
            ProgressView()
            Button("取消") { emit(Response(error:"cancelled")); exit(0) }
        }.padding(28).frame(width:420,height:180)
        .translationTask(configuration) { session in
            do { try await session.prepareTranslation(); emit(Response(supported:true)) }
            catch { emit(Response(error:errorCode(error))) }
            exit(0)
        }
    }
}
/// Runs NSApplication: like hiddenSessionTranslate it must be called before `main` first suspends.
@MainActor
func prepareTranslation(_ request: Request) throws {
    guard let source=request.source, source != "auto", let target=request.target else { try fail("apple_source_language_required") }
    let app=NSApplication.shared
    app.setActivationPolicy(.regular)
    let window=NSWindow(contentRect:NSRect(x:0,y:0,width:476,height:236),styleMask:[.titled],backing:.buffered,defer:false)
    window.title="析微影策 · 系统语言资源"
    window.contentView=NSHostingView(rootView:TranslationPreparation(configuration:TranslationSession.Configuration(source:Locale.Language(identifier:source),target:Locale.Language(identifier:target))))
    window.center(); window.makeKeyAndOrderFront(nil); app.activate(ignoringOtherApps:true)
    app.run()
}

final class Started { var value=false }
/// macOS 15 has no public TranslationSession initializer, only SwiftUI's `.translationTask`, so an invisible,
/// off-screen window of an accessory app hosts it. The language pair is checked before any translation call:
/// a missing language is reported instead of letting the session offer a download.
@MainActor
struct HiddenTranslation: View {
    let job: TranslationJob
    let configuration: TranslationSession.Configuration
    let started: Started
    var body: some View {
        Color.clear.frame(width:1,height:1).translationTask(configuration) { session in
            started.value=true
            do {
                switch await LanguageAvailability().status(from:job.source,to:job.target) {
                case .installed: emit(try job.response(try await session.translations(from:job.batch)))
                case .supported: emit(Response(error:"apple_translation_language_missing"))
                case .unsupported: emit(Response(error:"apple_language_unsupported"))
                @unknown default: emit(Response(error:"apple_system_failed"))
                }
            } catch { emit(Response(error:errorCode(error))) }
            exit(0)
        }
    }
}
/// Runs NSApplication and never returns. It must be called before `main` first suspends: afterwards `main`
/// resumes as a main-queue job, and a nested run loop there blocks every main-actor task, this one included.
@MainActor
func hiddenSessionTranslate(_ job: TranslationJob) -> Never {
    let started=Started()
    let app=NSApplication.shared
    app.setActivationPolicy(.accessory)
    let window=NSWindow(contentRect:NSRect(x:-10000,y:-10000,width:1,height:1),styleMask:[.borderless],backing:.buffered,defer:false)
    window.alphaValue=0; window.ignoresMouseEvents=true
    window.contentView=NSHostingView(rootView:HiddenTranslation(job:job,configuration:TranslationSession.Configuration(source:job.source,target:job.target),started:started))
    window.orderFrontRegardless()
    // The session normally starts at once and a 50-line batch takes seconds; never wait forever.
    Task { @MainActor in
        try? await Task.sleep(for:.seconds(30))
        if !started.value { emit(Response(error:"apple_system_failed")); exit(0) }
        try? await Task.sleep(for:.seconds(210))
        emit(Response(error:"apple_system_failed")); exit(0)
    }
    app.run()
    exit(0)
}
