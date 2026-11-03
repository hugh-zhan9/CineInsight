import { describe, it, expect } from 'vitest';
import { capabilityGoalSteps, setupState } from './capabilitySetup.js';
describe('capability preparation facts', () => {
  it('distinguishes unsupported, preparing, failed and not yet checked', () => {
    expect(setupState({ state: 'unavailable', reason_code: 'unsupported_platform' })).toBe('unsupported');
    expect(setupState({ state: 'warning', reason_code: 'preparing' })).toBe('preparing');
    expect(setupState({ state: 'unknown', reason_code: 'check_failed' })).toBe('failed');
    expect(setupState(undefined)).toBe('unknown');
  });
  it('does not claim subtitle resources are ready when no speech language is installed', () => {
    const state = { runtimes: { items: [{ key: 'apple', state: 'ok', metrics: [{ key: 'speech_languages_ready', value: 0 }] }] } };
    const steps = capabilityGoalSteps('subtitles', state);
    expect(steps.find(x => x.key === 'apple').state).toBe('needs_action');
    expect(steps.find(x => x.key === 'ffmpeg').state).toBe('unknown');
  });
  it('shows the empty library without treating a disabled watcher as missing directories', () => {
    const steps = capabilityGoalSteps('watch', { storage: { items: [{ key: 'root_1', state: 'disabled', reason_code: 'disabled' }] } }, { count: 0 });
    expect(steps.find(x => x.key === 'roots').state).toBe('ready');
    expect(steps.find(x => x.key === 'library').state).toBe('needs_action');
    expect(capabilityGoalSteps('watch', {}, {}).find(x => x.key === 'library').state).toBe('unknown');
  });
});
