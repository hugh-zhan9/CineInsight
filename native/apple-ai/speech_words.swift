import Foundation

/// A recognized word in file time with the recognizer's confidence (0 when it reports none).
typealias Piece = (segment: Segment, confidence: Double)

/// Lowercased letters and digits of a word; punctuation-only pieces give "".
func wordKey(_ text: String) -> String { String(text.lowercased().filter { $0.isLetter || $0.isNumber }) }
func wordKeys(_ pieces: [Piece]) -> [String] { pieces.map { wordKey($0.segment.text) }.filter { !$0.isEmpty } }

/// Joins the snapshots one macOS 15 recognition request reports. On-device recognition restarts its transcription
/// after a pause (measured: untimed partials grow, a timed snapshot closes the utterance, the next utterance starts
/// from one word), so the final result may hold only the last utterance. A snapshot that restarts commits the
/// previous one; anything else replaces it. The rules prefer keeping text over guessing.
struct SnapshotAccumulator {
    /// Chunk start in file time. Partial results carry no timing: every word sits here with zero length.
    let origin: Double
    private(set) var committed=[[Piece]]()
    private(set) var current=[Piece]()
    init(origin: Double) { self.origin=origin }
    mutating func add(_ snapshot: [Piece]) {
        guard !wordKeys(snapshot).isEmpty else { return }
        if restarts(from:current,to:snapshot) { committed.append(current) }
        current=snapshot
    }
    func timed(_ pieces: [Piece]) -> Bool { pieces.contains { $0.segment.start > origin || $0.segment.end > $0.segment.start } }
    /// With timings on both sides a restart begins after the previous snapshot ended and later than it began; a
    /// revision begins inside its span, even with a different first word. Without timings only a strict revision
    /// keeps the snapshot: the same first word, and the previous words except possibly the last repeated in order.
    func restarts(from previous: [Piece], to next: [Piece]) -> Bool {
        guard let p0=previous.first, let s0=next.first else { return false }
        if timed(previous), timed(next) {
            let previousEnd=previous.map(\.segment.end).max() ?? p0.segment.end
            return s0.segment.start >= previousEnd-0.05 && s0.segment.start > p0.segment.start+0.05
        }
        let old=wordKeys(previous), new=wordKeys(next)
        return old.first != new.first || !new.starts(with:old.dropLast())
    }
    /// Utterances in order. Committed words the latest snapshot covers again (a final result repeating the whole
    /// request) are dropped, and so are leading words that repeat the previous utterance's tail at overlapping times.
    var snapshots: [[Piece]] {
        var earlier=committed
        if let first=current.first, timed(current) {
            earlier=committed.map { $0.filter { $0.segment.start < first.segment.start-0.05 } }
        }
        var result=[[Piece]]()
        for utterance in (earlier+[current]).filter({ !$0.isEmpty }) {
            guard let previous=result.last, timed(previous), timed(utterance),
                  utterance[0].segment.start < previous.map(\.segment.end).max() ?? 0 else { result.append(utterance); continue }
            let tail=previous.map { wordKey($0.segment.text) }, head=utterance.map { wordKey($0.segment.text) }
            let repeated=(0...min(tail.count,head.count)).last { head.prefix($0).elementsEqual(tail.suffix($0)) } ?? 0
            let rest=Array(utterance.dropFirst(repeated))
            if !rest.isEmpty { result.append(rest) }
        }
        return result
    }
    /// Utterances with zero-length words given an end; a last word's padding stops at the next utterance's start.
    func utterances(chunkEnd: Double) -> [[Piece]] {
        let list=snapshots
        return list.indices.map { i in
            let next=i+1 < list.count && timed(list[i+1]) ? list[i+1][0].segment.start : chunkEnd
            return filledDurations(list[i],limit:min(next,chunkEnd))
        }
    }
}

/// Gives zero-length words an end: the next word's start (at most 1.5 s later, so pauses still separate lines),
/// or 0.3 s for the last word, never past `limit`. Words that still have no length are left to WordCollector.
func filledDurations(_ pieces: [Piece], limit: Double) -> [Piece] {
    pieces.indices.map { index in
        var piece=pieces[index]
        let start=piece.segment.start
        guard start.isFinite, !(piece.segment.end > start) else { return piece }
        let end=index+1 < pieces.count ? min(pieces[index+1].segment.start,start+1.5) : min(start+0.3,limit)
        if end > start { piece.segment.end=end }
        return piece
    }
}

/// How a recognition error ends one chunk: nil only for "no speech detected" (kAFAssistantErrorDomain 1110, what a
/// silent chunk reports), which skips the chunk. Any other error, or a revoked permission, fails the transcription.
func chunkFailure(domain: String, code: Int, authorized: Bool) -> String? {
    if !authorized { return "apple_speech_permission_denied" }
    return domain == "kAFAssistantErrorDomain" && code == 1110 ? nil : "apple_system_failed"
}

/// Confidence alone is not calibrated across languages, so text inconsistent with its recognizer is rejected.
/// Without a transcription confidence the candidate is ranked by language consistency alone.
func candidateScore(confidence: Double?, consistency: Double) -> Double? {
    guard consistency >= 0.6 else { return nil }
    guard let confidence else { return consistency }
    return confidence >= 0.65 ? confidence*consistency : nil
}
/// The language to use, or nil when the user must choose. Candidates ranked without a transcription confidence
/// (the macOS 15 recognizer may report none) are never accepted automatically.
func detectedLanguage(_ candidates: [Candidate], calibrated: Bool) -> String? {
    guard calibrated, let first=candidates.first, first.confidence >= 0.7,
          candidates.count == 1 || first.confidence-candidates[1].confidence >= 0.1 else { return nil }
    return first.language
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
