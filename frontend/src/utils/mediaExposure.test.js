import { describe, expect, it } from 'vitest';
import { createMediaExposureTracker } from './mediaExposure';

describe('actual presentation', () => {
  const visible = { id: 7, loaded: true, fraction: .5 };
  it('requires a continuous loaded half-visible 300ms and emits once', () => {
    const t = createMediaExposureTracker();
    expect(t.update([visible], { now: 0 })).toEqual([]);
    expect(t.update([visible], { now: 299 })).toEqual([]);
    expect(t.update([visible], { now: 300 })).toEqual([7]);
    expect(t.update([visible], { now: 600 })).toEqual([]);
  });
  it('does not count preloading, errors, hidden time, occlusion or discontinuous exposure', () => {
    const t = createMediaExposureTracker();
    t.update([visible], { now: 0 });
    t.update([visible], { now: 200, active: false });
    expect(t.update([visible], { now: 600 })).toEqual([]);
    t.update([{ ...visible, fraction: .49 }], { now: 800 });
    expect(t.update([visible], { now: 900 })).toEqual([]);
    t.update([{ ...visible, loaded: false }], { now: 1000 });
    expect(t.update([visible], { now: 1500 })).toEqual([]);
    expect(t.update([visible], { now: 1800 })).toEqual([7]);
  });
  it('resets on a new scene and never emits already persisted items', () => {
    const t = createMediaExposureTracker();
    t.update([visible], { now: 0 }); t.update([visible], { now: 300 }); t.reset();
    expect(t.update([{ ...visible, presented: true }], { now: 5000 })).toEqual([]);
    expect(t.update([visible], { now: 6000 })).toEqual([]);
    expect(t.update([visible], { now: 6300 })).toEqual([7]);
  });
});
