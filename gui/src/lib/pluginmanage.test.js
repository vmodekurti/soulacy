import { describe, it, expect } from 'vitest';
import { sourceKind, needsChecksum, permissionLines, credentialLines, statusInfo, riskSummary } from './pluginmanage.js';

describe('sourceKind', () => {
  it('classifies sources', () => {
    expect(sourceKind('https://github.com/acme/plug.git')).toBe('git');
    expect(sourceKind('git@github.com:acme/plug.git')).toBe('git');
    expect(sourceKind('https://example.com/plug-1.0.tar.gz')).toBe('archive');
    expect(sourceKind('/home/me/plug.zip')).toBe('archive');
    expect(sourceKind('/home/me/my-plugin')).toBe('dir');
    expect(sourceKind('')).toBe('');
  });
  it('requires checksums only for archives', () => {
    expect(needsChecksum('/x/p.tar.gz')).toBe(true);
    expect(needsChecksum('https://github.com/a/b.git')).toBe(false);
  });
});

describe('permissionLines', () => {
  it('renders scoped grants', () => {
    const lines = permissionLines([{ cap: 'vector.search', agents: ['assistant'] }]);
    expect(lines[0]).toContain('vector.search');
    expect(lines[0]).toContain('agents: assistant');
  });
  it('flags unscoped grants loudly', () => {
    expect(permissionLines([{ cap: 'channel.send' }])[0]).toContain('UNSCOPED');
  });
  it('handles empty', () => {
    expect(permissionLines(undefined)).toEqual([]);
  });
});

describe('credentialLines', () => {
  it('renders vault refs', () => {
    expect(credentialLines([{ key: 'API_TOKEN', from: 'p/api_token' }])[0])
      .toBe('API_TOKEN ← vault: p/api_token');
  });
});

describe('statusInfo', () => {
  it('prioritises re-approval over enabled state', () => {
    expect(statusInfo({ enabled: true, needs_reapproval: true }).cls).toBe('warn');
    expect(statusInfo({ enabled: false }).cls).toBe('muted');
    expect(statusInfo({ enabled: true }).cls).toBe('ok');
  });
});

describe('riskSummary', () => {
  it('summarises requests', () => {
    const s = riskSummary({ permissions: [{ cap: 'a.b' }], credentials: [{ key: 'K' }], has_gui: true });
    expect(s).toContain('1 capability');
    expect(s).toContain('1 credential');
    expect(s).toContain('GUI panel');
  });
  it('benign when nothing requested', () => {
    expect(riskSummary({})).toContain('no capabilities');
  });
});
