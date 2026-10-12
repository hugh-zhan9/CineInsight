import Foundation
@preconcurrency import Speech
import AVFoundation

/// Grants a single permission to finish where a callback, an error and a timeout race.
final class Once: @unchecked Sendable {
    private let lock=NSLock()
    private var done=false
    func claim() -> Bool { lock.lock(); defer { lock.unlock() }; if done { return false }; done=true; return true }
}

/// macOS 15: on-device SFSpeechRecognizer. Its languages are the dictation languages the user downloads in
/// System Settings › Keyboard › Dictation; this helper cannot download them programmatically.
struct LegacySpeech: SpeechEngine {
    /// Bounded wait for the user to answer the speech recognition permission prompt.
    static let authorizationTimeout=60.0
    /// One recognition request per chunk of at most 50 s, cut at the quietest 0.1 s of its last 5 s.
    static let chunkSeconds=50.0, cutSearchSeconds=5.0
    /// A chunk normally finishes much faster than real time; a stalled recognizer fails instead of hanging.
    static let chunkTimeout=300.0
    /// Languages written without spaces between words: no space is inserted where chunks join.
    static let unspaced: Set<String>=["zh", "ja", "yue", "wuu", "th", "lo", "km", "my"]

    func status() async -> Response {
        let authorization=SFSpeechRecognizer.authorizationStatus()
        let locales=SFSpeechRecognizer.supportedLocales().sorted { $0.identifier < $1.identifier }
        let langs=locales.map { Language(code:$0.identifier,name:displayLanguage($0.identifier),installed:SFSpeechRecognizer(locale:$0)?.supportsOnDeviceRecognition ?? false) }
        return Response(supported:!locales.isEmpty && authorization != .denied && authorization != .restricted,languages:langs)
    }
    func locale(for code: String) async throws -> Locale {
        let wanted=Locale(identifier:code)
        let normalized={ (locale: Locale) in locale.identifier.replacingOccurrences(of:"_",with:"-").lowercased() }
        let supported=Array(SFSpeechRecognizer.supportedLocales())
        if code != "auto", let exact=supported.first(where: { normalized($0)==normalized(wanted) }) { return exact }
        guard code != "auto", let locale=Self.preferred(supported.filter { languageKey($0)==languageKey(wanted) },region:wanted.region?.identifier) else { try fail("apple_language_unsupported") }
        return locale
    }
    func prepare(_ code: String) async throws {
        let locale=try await locale(for:code)
        try await Self.authorize()
        guard SFSpeechRecognizer(locale:locale)?.supportsOnDeviceRecognition == true else { try fail("apple_speech_dictation_missing") }
    }
    func detectionLocales() async throws -> [Locale] {
        let installed=SFSpeechRecognizer.supportedLocales().filter { SFSpeechRecognizer(locale:$0)?.supportsOnDeviceRecognition == true }
        let locales=Dictionary(grouping:installed,by:languageKey).values.compactMap { Self.preferred($0,region:nil) }.sorted { $0.identifier < $1.identifier }
        guard !locales.isEmpty else { try fail("apple_speech_dictation_missing") }
        return locales
    }

    /// Picks the requested region, then the language's most likely region (en → US, zh → CN).
    static func preferred(_ locales: [Locale], region: String?) -> Locale? {
        guard let first=locales.first else { return nil }
        let likely=Locale.Language(identifier:Locale.Language(identifier:languageKey(first)).maximalIdentifier).region?.identifier
        func rank(_ locale: Locale) -> Int {
            guard let own=locale.region?.identifier else { return 2 }
            return own==region ? 0 : own==likely ? 1 : 2
        }
        return locales.min { (rank($0),$0.identifier) < (rank($1),$1.identifier) }
    }
    static func authorize() async throws {
        var status=SFSpeechRecognizer.authorizationStatus()
        if status == .notDetermined {
            let once=Once()
            status=await withCheckedContinuation { continuation in
                SFSpeechRecognizer.requestAuthorization { result in if once.claim() { continuation.resume(returning:result) } }
                DispatchQueue.global().asyncAfter(deadline:.now()+authorizationTimeout) { if once.claim() { continuation.resume(returning:.notDetermined) } }
            }
        }
        // An unanswered prompt is reported like a denial: access can be allowed later in System Settings.
        guard status == .authorized else { try fail("apple_speech_permission_denied") }
    }
}

extension LegacySpeech {
    func transcribe(_ path: String, locale: Locale) async throws -> Transcript {
        guard let recognizer=SFSpeechRecognizer(locale:locale) else { try fail("apple_language_unsupported") }
        guard recognizer.supportsOnDeviceRecognition else { try fail("apple_speech_dictation_missing") }
        try await Self.authorize()
        recognizer.queue=OperationQueue()
        let file=try AVAudioFile(forReading:URL(fileURLWithPath:path))
        let rate=file.processingFormat.sampleRate
        var collector=WordCollector(spaced:!Self.unspaced.contains(languageKey(locale)))
        var position: AVAudioFramePosition=0
        while position < file.length {
            try Task.checkCancellation()
            let count=try Self.chunkLength(file,from:position)
            let transcription=try await Self.recognize(file,from:position,count:count,recognizer:recognizer)
            collector.add(chunk:Self.pieces(transcription,offset:Double(position)/rate))
            position += AVAudioFramePosition(count)
        }
        return collector.transcript
    }

    /// Frames in the chunk starting at `position`: the rest of the file, or ≤50 s ending at a quiet point.
    static func chunkLength(_ file: AVAudioFile, from position: AVAudioFramePosition) throws -> AVAudioFrameCount {
        let rate=file.processingFormat.sampleRate
        let limit=AVAudioFrameCount(chunkSeconds*rate), search=AVAudioFrameCount(cutSearchSeconds*rate), window=max(AVAudioFrameCount(rate/10),1)
        if file.length-position <= AVAudioFramePosition(limit) { return AVAudioFrameCount(file.length-position) }
        guard let buffer=AVAudioPCMBuffer(pcmFormat:file.processingFormat,frameCapacity:search) else { try fail("apple_system_failed") }
        file.framePosition=position+AVAudioFramePosition(limit-search)
        try file.read(into:buffer,frameCount:search)
        guard let samples=buffer.floatChannelData?[0] else { return limit }
        var best=limit, quietest=Float.infinity, start: AVAudioFrameCount=0
        while start+window <= buffer.frameLength {
            var energy: Float=0
            for i in Int(start)..<Int(start+window) { energy += samples[i]*samples[i] }
            if energy < quietest { quietest=energy; best=limit-search+start+window/2 }
            start += window
        }
        return best
    }

    /// Recognizes one chunk on device. Returns nil when the chunk holds no speech.
    static func recognize(_ file: AVAudioFile, from position: AVAudioFramePosition, count: AVAudioFrameCount, recognizer: SFSpeechRecognizer) async throws -> SFTranscription? {
        let request=SFSpeechAudioBufferRecognitionRequest()
        request.requiresOnDeviceRecognition=true
        request.addsPunctuation=true
        request.shouldReportPartialResults=false
        request.taskHint = .dictation
        let step=max(AVAudioFrameCount(file.processingFormat.sampleRate),1)
        var remaining=count
        file.framePosition=position
        while remaining > 0 {
            guard let buffer=AVAudioPCMBuffer(pcmFormat:file.processingFormat,frameCapacity:min(step,remaining)) else { try fail("apple_system_failed") }
            try file.read(into:buffer,frameCount:min(step,remaining))
            if buffer.frameLength == 0 { break }
            request.append(buffer)
            remaining -= buffer.frameLength
        }
        request.endAudio()
        return try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<SFTranscription?, Error>) in
            let once=Once()
            let task=recognizer.recognitionTask(with:request) { result, error in
                if let result, result.isFinal {
                    if once.claim() { continuation.resume(returning:result.bestTranscription) }
                } else if let error, once.claim() {
                    if isNoSpeech(error) { continuation.resume(returning:nil) } else { continuation.resume(throwing:Failure(code:"apple_system_failed")) }
                }
            }
            DispatchQueue.global().asyncAfter(deadline:.now()+chunkTimeout) {
                if once.claim() { task.cancel(); continuation.resume(throwing:Failure(code:"apple_system_failed")) }
            }
        }
    }
    /// The recognizer reports a silent chunk as an error rather than an empty result.
    static func isNoSpeech(_ error: Error) -> Bool {
        let error=error as NSError
        return error.domain == "kAFAssistantErrorDomain" && error.code == 1110
    }
    /// Word timings shifted to file time. Each word's text runs to the next word's start, so punctuation and
    /// spacing from formattedString stay with the preceding word, matching the line breaking of the new path.
    static func pieces(_ transcription: SFTranscription?, offset: Double) -> [(Segment, Double)] {
        guard let transcription else { return [] }
        let text=transcription.formattedString as NSString, segments=transcription.segments
        return segments.indices.map { i in
            let lower=i == 0 ? 0 : segments[i].substringRange.location
            let upper=i+1 < segments.count ? segments[i+1].substringRange.location : text.length
            let piece=lower < upper && upper <= text.length ? text.substring(with:NSRange(location:lower,length:upper-lower)) : segments[i].substring
            let start=offset+segments[i].timestamp
            return (Segment(start:start,end:start+segments[i].duration,text:piece),Double(segments[i].confidence))
        }
    }
}

/// Word timings across chunks for the Go side, which groups them into subtitle lines. Words stay ordered and
/// non-overlapping with a usable duration; text with unusable timing joins a neighbouring word.
struct WordCollector {
    let spaced: Bool
    var words=[Segment](), weighted=0.0, weight=0.0, carried=""
    mutating func add(chunk pieces: [(Segment, Double)]) {
        for (index,(piece,confidence)) in pieces.enumerated() {
            var word=piece
            if index == 0, spaced, let last=words.last?.text.last, !last.isWhitespace { word.text=" "+word.text }
            if word.start.isFinite, word.end.isFinite { word.start=max(word.start,words.last?.end ?? 0) }
            guard word.start.isFinite, word.end.isFinite, word.end-word.start >= 0.01 else {
                if words.isEmpty { carried += word.text } else { words[words.count-1].text += word.text }
                continue
            }
            word.text=carried+word.text; carried=""
            words.append(word)
            weighted += confidence*(word.end-word.start); weight += word.end-word.start
        }
    }
    var transcript: Transcript { Transcript(segments:words,confidence:weight > 0 ? weighted/weight : 0) }
}
