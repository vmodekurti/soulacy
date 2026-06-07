// Plugin install & management helpers (Story E13).

// sourceKind classifies an install source the way the backend will.
export function sourceKind(source) {
  const s = (source || '').trim();
  if (!s) return '';
  if (/\.(tar\.gz|tgz|zip)$/i.test(s)) return 'archive';
  if (/^(https?:\/\/|git@)/.test(s) || s.endsWith('.git')) return 'git';
  return 'dir';
}

// needsChecksum — archives require a sha256; everything else doesn't.
export function needsChecksum(source) {
  return sourceKind(source) === 'archive';
}

// permissionLines renders manifest permissions for the approval dialog.
// Empty scopes mean "unscoped" — flag that loudly.
export function permissionLines(permissions) {
  return (permissions || []).map((p) => {
    const scopes = [];
    if (p.agents?.length) scopes.push(`agents: ${p.agents.join(', ')}`);
    if (p.channels?.length) scopes.push(`channels: ${p.channels.join(', ')}`);
    if (p.types?.length) scopes.push(`types: ${p.types.join(', ')}`);
    return scopes.length ? `${p.cap} (${scopes.join(' · ')})` : `${p.cap} (UNSCOPED — applies everywhere)`;
  });
}

// credentialLines renders requested vault credentials.
export function credentialLines(credentials) {
  return (credentials || []).map((c) => `${c.key} ← vault: ${c.from}`);
}

// statusInfo maps an installed-plugin entry to a badge.
export function statusInfo(p) {
  if (p?.needs_reapproval) return { label: 'needs re-approval', cls: 'warn' };
  if (!p?.enabled) return { label: 'disabled', cls: 'muted' };
  return { label: 'enabled', cls: 'ok' };
}

// riskSummary gives the approval dialog a one-line risk statement.
export function riskSummary(preview) {
  const nPerm = preview?.permissions?.length || 0;
  const nCred = preview?.credentials?.length || 0;
  const bits = [];
  if (nPerm) bits.push(`${nPerm} capabilit${nPerm === 1 ? 'y' : 'ies'}`);
  if (nCred) bits.push(`${nCred} credential${nCred === 1 ? '' : 's'}`);
  if (preview?.channels?.length) bits.push(`${preview.channels.length} sidecar channel(s)`);
  if (preview?.providers?.length) bits.push(`${preview.providers.length} provider(s)`);
  if (preview?.has_gui) bits.push('a GUI panel');
  if (!bits.length) return 'Requests no capabilities or credentials.';
  return `Requests ${bits.join(', ')}.`;
}
