import Foundation

// Logic tests for speech_words.swift. Run: bash scripts/test_apple_ai_logic.sh
nonisolated(unsafe) var failures=0
func check(_ ok: Bool, _ label: String) { print((ok ? "PASS " : "FAIL ")+label); if !ok { failures += 1 } }
/// A word at file time; `c` 0 means no confidence (what partial results report).
func w(_ text: String, _ start: Double, _ end: Double, _ c: Double=0.9) -> Piece { (Segment(start:start,end:end,text:text),c) }
/// An untimed partial result: every word at the chunk origin with zero length.
func partial(_ origin: Double, _ words: String...) -> [Piece] { words.map { w($0,origin,origin,0) } }
func joined(_ utterances: [[Piece]]) -> String { utterances.map { $0.map(\.segment.text).joined(separator:" ") }.joined(separator:" | ") }
func run(_ origin: Double, _ snapshots: [[Piece]]) -> [[Piece]] {
    var accumulator=SnapshotAccumulator(origin:origin)
    for snapshot in snapshots { accumulator.add(snapshot) }
    return accumulator.snapshots
}

func accumulationTests() {
    // Shape measured on macOS 27 with the legacy path forced: untimed partials grow (the last word may be revised),
    // a timed snapshot closes the utterance, the next utterance restarts from one untimed word.
    let measured=run(46.75,[partial(46.75,"Our"),partial(46.75,"Our","neighbor","play"),partial(46.75,"Our","neighbor","plays","the"),
        [w("Our",48.13,48.31),w("neighbor",48.31,48.67),w("plays",48.67,48.97)],
        partial(46.75,"The"),partial(46.75,"The","coffee"),[w("The",54.0,54.2),w("coffee",54.2,54.6)],
        [w("The",54.0,54.2),w("coffee",54.2,54.6)]])
    check(joined(measured) == "Our neighbor plays | The coffee", "measured restart pattern keeps every utterance once: \(joined(measured))")
    let told=run(0,[[w("I",0,0.2),w("told",0.2,0.5),w("him.",0.5,0.9)],partial(0,"I")])
    check(joined(told) == "I told him. | I", "untimed \"I\" after \"I told him.\" commits the first utterance: \(joined(told))")
    let toldTimed=run(0,[[w("I",0,0.2),w("told",0.2,0.5),w("him.",0.5,0.9)],[w("I",3.0,3.1)]])
    check(joined(toldTimed) == "I told him. | I", "timed \"I\" after a pause commits the first utterance: \(joined(toldTimed))")
    check(joined(run(0,[partial(0,"It's"),partial(0,"I")])) == "I", "an untimed partial is revised, never committed (\"It's\" then \"I\")")
    check(joined(run(0,[[w("I",3.0,3.03)],[w("It's",3.0,3.2)]])) == "It's", "timed \"It's\" starting inside \"I\" is a revision")
    check(joined(run(0,[[w("I",3.0,3.03)],[w("I",3.0,3.2),w("went",3.2,3.5)]])) == "I went", "a <50 ms single-word partial is revised, not repeated")
    let thenNext=run(0,[[w("I",3.0,3.03)],[w("I",3.0,3.2),w("went",3.2,3.5)],[w("Next",6.0,6.3)]])
    check(joined(thenNext) == "I went | Next", "the revised <50 ms partial stays one utterance when another follows: \(joined(thenNext))")
    check(joined(run(0,[[w("So",1.0,1.2),w("I",1.2,1.3),w("went",1.3,1.6)],[w("I",1.2,1.3),w("went",1.3,1.6),w("to",1.6,1.7),w("the",1.7,1.8),w("store",1.8,2.2)]])) == "I went to the store",
          "a revision dropping its first word inside the previous span is not a restart")
    let washing=run(0,[partial(0,"We","are","watching","a"),[w("We",0,0.2),w("are",0.2,0.4),w("washing",0.4,0.8),w("a",0.8,0.9),w("car.",0.9,1.3)]])
    check(joined(washing) == "We are washing a car.", "a timed closure revising an earlier word replaces the partial: \(joined(washing))")
    let dollars=run(0,[partial(0,"I","have","twenty","five"),partial(0,"I","have","25","dollars"),[w("I",0,0.2),w("have",0.2,0.5),w("25",0.5,1.0),w("dollars.",1.0,1.5)]])
    check(joined(dollars) == "I have 25 dollars.", "routine partial revisions are not duplicated: \(joined(dollars))")
    let no=run(0,[[w("No.",1.0,1.3)],partial(0,"No"),[w("No,",3.0,3.2),w("I",3.2,3.3),w("don't.",3.3,3.6)]])
    check(joined(no) == "No. | No, I don't.", "a short closure followed by an utterance with the same first word is kept: \(joined(no))")
    let come=run(0,[partial(0,"Come"),[w("Come",0.5,0.7),w("on.",0.7,1.0)],partial(0,"Come"),partial(0,"Come","here."),[w("Come",4.0,4.2),w("here.",4.2,4.6)]])
    check(joined(come) == "Come on. | Come here.", "\"Come on.\" then \"Come here.\" keeps both utterances: \(joined(come))")
    check(joined(run(0,[partial(0,"We"),partial(0,"We","are"),partial(0,"We","are","watch"),partial(0,"We","are","watching","a")])) == "We are watching a",
          "untimed growth with a revised last word stays one utterance")
    let overlap=run(0,[[w("We",0,0.2),w("are",0.2,0.4),w("here",0.4,0.8)],[w("here",0.76,0.85),w("now",0.9,1.2)]])
    check(joined(overlap) == "We are here | now", "words repeating the previous tail at overlapping times are dropped: \(joined(overlap))")
    let apart=run(0,[[w("We",0,0.2),w("are",0.2,0.4),w("here",0.4,0.8)],[w("here",3.0,3.2),w("now",3.2,3.5)]])
    check(joined(apart) == "We are here | here now", "a real repetition after a pause is kept: \(joined(apart))")
    let u1=[w("We",0,0.2),w("are",0.2,0.4),w("here.",0.4,0.8)], u2=[w("Next",3.0,3.3),w("one",3.3,3.6)]
    check(joined(run(0,[u1,u2,u2])) == "We are here. | Next one", "a duplicate final is not repeated")
    check(joined(run(0,[u1,u2,u1+u2])) == "We are here. Next one", "a final repeating the whole request is not duplicated")
    check(run(0,[[w(".",0,0)]]).isEmpty && joined(run(0,[partial(0,"We"),[w(".",0,0)]])) == "We", "punctuation-only snapshots are ignored")
}

func durationTests() {
    let filled=filledDurations([w("a",1.0,1.0),w("b",1.4,1.4),w("c",5.0,5.0),w("d",9.8,9.8),w("e",9.9,9.95)],limit:10)
    check(filled.map(\.segment.end) == [1.4,2.9,6.5,9.9,9.95], "zero lengths end at the next start, at most 1.5 s; real lengths kept: \(filled.map(\.segment.end))")
    let last=filledDurations([w("x",9.8,9.8)],limit:10).map(\.segment.end), atLimit=filledDurations([w("y",10,10)],limit:10).map(\.segment.end)
    check(abs(last[0]-10) < 1e-9 && atLimit == [10], "the last word gets 0.3 s, never past the limit: \(last) \(atLimit)")
    var accumulator=SnapshotAccumulator(origin:0)
    accumulator.add([w("a",1.0,1.2),w("b",1.2,1.2)]); accumulator.add([w("c",1.35,1.6)])
    let padded=accumulator.utterances(chunkEnd:50).map { $0.map(\.segment.end) }
    check(padded == [[1.2,1.35],[1.6]], "a last word's padding stops at the next utterance's first word: \(padded)")
    let untimedNext=SnapshotAccumulator(origin:0)
    check(untimedNext.utterances(chunkEnd:50).isEmpty, "no snapshots, no utterances")
}
func errorTests() {
    check(chunkFailure(domain:"kAFAssistantErrorDomain",code:1110,authorized:true) == nil, "\"no speech detected\" skips a silent chunk")
    for (domain,code) in [("kAFAssistantErrorDomain",1101),("kAFAssistantErrorDomain",203),("kLSRErrorDomain",301),(NSCocoaErrorDomain,1110)] {
        check(chunkFailure(domain:domain,code:code,authorized:true) == "apple_system_failed", "\(domain) \(code) fails the transcription")
    }
    check(chunkFailure(domain:"kAFAssistantErrorDomain",code:1110,authorized:false) == "apple_speech_permission_denied", "a revoked permission fails with the permission code")
}
func detectionTests() {
    check(candidateScore(confidence:nil,consistency:0.9) == 0.9 && candidateScore(confidence:nil,consistency:0.59) == nil, "without confidence candidates rank by consistency")
    check(candidateScore(confidence:0.5,consistency:0.9) == nil && abs((candidateScore(confidence:0.8,consistency:0.9) ?? 0)-0.72) < 1e-9 && candidateScore(confidence:0,consistency:0.95) == nil,
          "with confidence the original rule is unchanged")
    let clear=[Candidate(language:"en-US",confidence:0.95)]
    check(detectedLanguage(clear,calibrated:true) == "en-US", "a calibrated clear winner is accepted")
    check(detectedLanguage(clear,calibrated:false) == nil, "an uncalibrated winner is never accepted automatically")
    check(detectedLanguage([Candidate(language:"en-US",confidence:0.69)],calibrated:true) == nil, "below 0.7 the user picks")
    check(detectedLanguage([Candidate(language:"en-US",confidence:0.9),Candidate(language:"de-DE",confidence:0.85)],calibrated:true) == nil, "a lead under 0.1 makes the user pick")
    check(detectedLanguage([],calibrated:true) == nil, "no candidates, no language")
}
func collectorTests() {
    var en=WordCollector(spaced:true)
    en.add([w("We ",0,0.2),w("are ",0.2,0.4),w("here.",0.4,0.8)]); en.add([w("Next ",47.2,47.5),w("one",47.5,47.9)])
    check(en.words.map(\.text).joined() == "We are here. Next one", "English utterances join with one space")
    var zh=WordCollector(spaced:false)
    zh.add([w("今天",0,0.5),w("好。",0.5,0.9)]); zh.add([w("明天",47.2,47.6)])
    check(zh.words.map(\.text).joined() == "今天好。明天", "Chinese utterances join without a space")
    var zero=WordCollector(spaced:true)
    zero.add(filledDurations([w("We ",3,3,0),w("are ",3.2,3.2,0),w("here.",3.5,3.5,0)],limit:50))
    check(zero.words.map(\.text) == ["We ","are ","here."] && zero.transcript.confidence == nil, "zero-length words survive with derived ends; no confidence is nil")
    var rated=WordCollector(spaced:true)
    rated.add([w("a ",0,1,1.0),w("b ",1,4,0.6),w("c",4,9,0)])
    check(abs((rated.transcript.confidence ?? -1)-0.7) < 1e-9, "confidence is weighted over rated words only")
}

/// Replays tests/fixtures: recognizer snapshots logged on macOS 27 with the macOS 15 path forced, from synthetic `say`
/// speech. One line per result callback: `SNAP final=<isFinal> offset=<chunk start>` then one
/// `[substring|timestamp+duration|c<confidence>]` per segment, timestamps relative to the chunk.
func replay(_ path: String) -> [String] {
    guard let log=try? String(contentsOfFile:path,encoding:.utf8) else { check(false,"fixture \(path) readable"); return [] }
    var chunks=[(Double,[[Piece]])]()
    for line in log.split(separator:"\n") where line.hasPrefix("SNAP") {
        let offset=Double(line.split(separator:" ")[2].dropFirst(7)) ?? 0
        let pieces=line.split(separator:"[").dropFirst().map { token -> Piece in
            let parts=token.split(separator:"|"), time=parts[1].split(separator:"+"), start=offset+(Double(time[0]) ?? 0)
            return w(String(parts[0]),start,start+(Double(time[1]) ?? 0),Double(parts[2].dropFirst().prefix { $0 != "]" }) ?? 0)
        }
        if chunks.last?.0 != offset { chunks.append((offset,[])) }
        chunks[chunks.count-1].1.append(pieces)
    }
    return chunks.flatMap { offset,snapshots in
        var accumulator=SnapshotAccumulator(origin:offset)
        snapshots.forEach { accumulator.add($0) }
        return accumulator.snapshots.map { $0.map(\.segment.text).joined(separator:" ") }
    }
}
func replayTests(_ fixtures: String) {
    let long=replay(fixtures+"/legacy-snapshots-90s.log")
    let sentences=["The first train leaves at seven","My sister bought a red bicycle","We should paint the kitchen tomorrow","The library closes early on Sunday",
        "Please bring an umbrella with you","The museum has a new dinosaur exhibit","Our neighbor plays the violin every night","The coffee shop sells fresh bread",
        "I forgot my keys at the office","The children built a Sandcastle on the beach","Turn left after the bridge","The concert starts at 9: 30"]
    check(long == sentences, "logged 90 s sample (98 snapshots, two chunks) gives its 12 sentences once each: \(long.count)")
    let english=replay(fixtures+"/legacy-snapshots-english.log")
    check(english == ["We are watching a movie tonight please wait for me here. The weather is nice and the popcorn is ready."], "logged English sample gives one utterance: \(english)")
}

@main struct SpeechWordsTests {
    static func main() {
        accumulationTests(); durationTests(); errorTests(); detectionTests(); collectorTests()
        replayTests(CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "native/apple-ai/tests/fixtures")
        print(failures == 0 ? "speech_words tests passed" : "\(failures) speech_words test(s) failed")
        exit(failures == 0 ? 0 : 1)
    }
}
