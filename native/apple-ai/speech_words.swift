import Foundation

/// A recognized word in file time with the recognizer's confidence (0 when it reports none).
typealias Piece = (segment: Segment, confidence: Double)

/// Joins the snapshots one macOS 15 recognition request reports. On-device recognition can restart its
/// transcription after a pause, so the final result may hold only the last utterance: a snapshot that restarts
/// commits the previous one; a snapshot that revises or extends it replaces it.
struct SnapshotAccumulator {
    private(set) var committed=[[Piece]]()
    private(set) var current=[Piece]()
    mutating func add(_ snapshot: [Piece]) {
        guard !snapshot.isEmpty else { return }
        if Self.restarts(from:current,to:snapshot) { committed.append(current) }
        current=snapshot
    }
    /// Committed utterances, then the latest snapshot. Committed words the latest snapshot covers again (a final
    /// result that repeats the whole request) are dropped.
    var snapshots: [[Piece]] {
        var earlier=committed
        if let first=current.first, Self.timed(current) {
            earlier=committed.map { $0.filter { $0.segment.start < first.segment.start-0.05 } }
        }
        return (earlier+[current]).filter { !$0.isEmpty }
    }
    static func timed(_ pieces: [Piece]) -> Bool { pieces.contains { $0.segment.end > $0.segment.start } }
    /// Timestamps count from the start of the audio: a restarted transcription begins after the previous snapshot
    /// ended, or with different words clearly later than it began. Without timings only the words can tell.
    static func restarts(from previous: [Piece], to next: [Piece]) -> Bool {
        guard let p0=previous.first, let s0=next.first else { return false }
        let key={ (text: String) in String(text.lowercased().filter { $0.isLetter || $0.isNumber }) }
        let a=key(p0.segment.text), b=key(s0.segment.text)
        let sameStart=a.isEmpty || b.isEmpty || a.hasPrefix(b) || b.hasPrefix(a)
        guard timed(previous), timed(next) else { return !sameStart }
        let previousEnd=previous.map(\.segment.end).max() ?? 0
        return s0.segment.start >= previousEnd-0.05 || (!sameStart && s0.segment.start > p0.segment.start+0.3)
    }
}

/// Gives zero-length words an end: the next word's start (at most 1.5 s later, so pauses still separate lines),
/// or 0.3 s for the last word, capped at the chunk end. Words that still have no length are left to WordCollector.
func filledDurations(_ pieces: [Piece], chunkEnd: Double) -> [Piece] {
    pieces.indices.map { index in
        var piece=pieces[index]
        let start=piece.segment.start
        guard start.isFinite, !(piece.segment.end > start) else { return piece }
        let end=index+1 < pieces.count ? min(pieces[index+1].segment.start,start+1.5) : min(start+0.3,chunkEnd)
        if end > start { piece.segment.end=end }
        return piece
    }
}

/// How a recognition error ends one chunk: nil skips the chunk (assistant-domain errors are what silence,
/// music and noise produce); a revoked permission or any other error fails the transcription with a code.
func chunkFailure(domain: String, authorized: Bool) -> String? {
    if !authorized { return "apple_speech_permission_denied" }
    return domain == "kAFAssistantErrorDomain" ? nil : "apple_system_failed"
}

/// Word timings across chunks for the Go side, which groups them into subtitle lines. Words stay ordered and
/// non-overlapping with a usable duration; text with unusable timing joins a neighbouring word. Confidence is
/// the duration-weighted mean over words that report one, nil when none does.
struct WordCollector {
    let spaced: Bool
    var words=[Segment](), weighted=0.0, weight=0.0, carried=""
    mutating func add(_ pieces: [Piece]) {
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
            if confidence > 0 { weighted += confidence*(word.end-word.start); weight += word.end-word.start }
        }
    }
    var transcript: Transcript { Transcript(segments:words,confidence:weight > 0 ? weighted/weight : nil) }
}
