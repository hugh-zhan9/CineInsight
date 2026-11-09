import { afterEach, expect, it, vi } from 'vitest';
import { readPlaybackAudio, restorePlaybackAudio, rememberPlaybackAudio } from './playbackAudio.js';
afterEach(() => vi.unstubAllGlobals());
it.each([null, 'bad', '{}', '{"volume":2,"muted":false}', '{"volume":null,"muted":true}'])('rejects invalid stored audio %s', saved => {
 vi.stubGlobal('localStorage', { getItem: () => saved });
 expect(readPlaybackAudio()).toEqual({ volume: 1, muted: true });
});
it('does not persist a media element before restoration, and restores only once', () => {
 const setItem=vi.fn(); vi.stubGlobal('localStorage', {getItem:()=>'{"volume":0.4,"muted":false}',setItem});
 const video={volume:1,muted:true};rememberPlaybackAudio(video);expect(setItem).not.toHaveBeenCalled();
 restorePlaybackAudio(video);video.volume=.7;restorePlaybackAudio(video);expect(video.volume).toBe(.7);
 rememberPlaybackAudio(video);expect(setItem).toHaveBeenCalledWith('cineinsight-playback-audio','{"volume":0.7,"muted":false}');
});
