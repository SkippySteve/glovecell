import {describe, expect, it} from 'vitest';
import {isRunning, safeLink} from './ProviderLogins';

describe('provider sign-in', () => {
  it('shows only links on the provider\'s own sign-in hosts', () => {
    expect(safeLink('grok', 'https://accounts.x.ai/oauth2/device?user_code=ABCD-1234')).toBe('https://accounts.x.ai/oauth2/device?user_code=ABCD-1234');
    expect(safeLink('cursor', 'https://cursor.com/loginDeepControl?challenge=c')).toBe('https://cursor.com/loginDeepControl?challenge=c');
    expect(safeLink('grok', 'https://cursor.com/x')).toBeUndefined();
    expect(safeLink('cursor', 'https://cursor.com.evil.example/x')).toBeUndefined();
    expect(safeLink('grok', 'http://accounts.x.ai/x')).toBeUndefined();
    expect(safeLink('grok', 'javascript:alert(1)')).toBeUndefined();
    expect(safeLink('grok', 'https://user@accounts.x.ai/x')).toBeUndefined();
  });
  it('knows which states are in progress', () => {
    expect(isRunning({provider: 'grok', state: 'waiting'})).toBe(true);
    expect(isRunning({provider: 'grok', state: 'done'})).toBe(false);
    expect(isRunning(undefined)).toBe(false);
  });
});
