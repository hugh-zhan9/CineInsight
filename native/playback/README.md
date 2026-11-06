# Native playback composition probe

This package owns the low-level libmpv renderer only. It does not authorize a
video, expose filesystem paths to a browser, or record viewing history. Production
owner/session integration is tracked separately in P-006.

Assemble the pinned arm64 runtime with `python3 scripts/build_native_playback_runtime.py --collect-sources`.
The builder checks the archive, dependency closure and minimum macOS version,
rewrites relative install names, signs the libraries, and collects the fixed
recipe, patches, source URLs and license material. The source manifest documents
the corresponding FFmpeg Git tree separately from the upstream release archive;
it does not claim a legal distribution determination.

Run `bash scripts/test_native_playback_probe.sh`. The script builds an isolated
Wails app bundle, copies the assembled runtime, ad-hoc signs and verifies the
bundle, and removes it on exit. It never installs or reads a user library. Default
media is synthetic MPEG-4/MP3 AVI. `CINE_NATIVE_PROBE_MEDIA` may select a dedicated
fixture. Provenance/hashes for downloaded upstream fixtures are in
`probe/fixtures.json`; media bytes are not included in the repository.

`CINE_NATIVE_PROBE_MODE=warm` measures 20 opens/seeks in one process.
`CINE_NATIVE_PROBE_MODE=cold CINE_NATIVE_PROBE_REPEAT=20` measures fresh processes;
OS file caches are not cleared. `CINE_NATIVE_PROBE_REPORT` saves raw JSON. The
measured scope and results are in the design's `native-probe-performance.json`.

The application registers its unique initial WKWebView rather than guessing the
current key window. Loading/closing advances its epoch. Production must revoke
owners on that change and drain all players in OnBeforeClose, while AppKit is
still running; OnShutdown is not an appropriate first opportunity to do this.
Render work runs on a bounded serial queue with one GL context. Subtitle lookup
uses a separate clock queue and interval index; only the latest display update is
pending on the main queue. Close drains both queues before destroying the core.

Known failure fixture: `eiqu-56k.rm` has a broken seek index. At 37.93425 seconds,
libavformat reports read_timestamp/seek failure even though load and initial
playback succeed. Keep this separate from normal performance samples, and ensure
production reports seek failure rather than treating an incorrect position as a
successful jump. Truncated FATE clips are also failure fixtures, not complete
movie-duration acceptance evidence.

The production media element and real service composition can be exercised with
`CINE_NATIVE_PROBE_MODE=owner bash scripts/test_native_playback_probe.sh`.
This creates a disposable SQLite library and copies generated media into it; the
fixture uses the actual owner service, source FD checks, subtitle reader and Vue
player. It covers natural EOF recommendations, native-to-native navigation and
source rename cleanup. Development executables must explicitly set
`CINEINSIGHT_NATIVE_RUNTIME_DIR`; a release .app always loads its own Resources.

Occluded video consumes frames through a 16×16 offscreen framebuffer, keeping
playback and seek facts moving while HTML popovers cover the native surface.
Presented-frame and decoded-frame counters are separate. The source mutation
port holds a matching-path admission barrier across the actual OS operation;
its short FD-registration gate is acquired after the existing library path
fence. Existing descriptors do not wait for unrelated directory-copy writers.
