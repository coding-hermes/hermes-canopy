import { useState } from 'react';
import type { PluginManifest } from '../lib/pluginTypes';

export interface UpdateBannerPlugin { id: string; name: string; manifest: PluginManifest }
export interface PluginUpdateBannerProps { installed: UpdateBannerPlugin; available: UpdateBannerPlugin; onUpdated?: () => void }

export default function PluginUpdateBanner({ installed, available, onUpdated }: PluginUpdateBannerProps) {
  const storageKey = `canopy.plugin-update.dismissed.${installed.id}.${available.manifest.version}`;
  const [dismissed, setDismissed] = useState(() => localStorage.getItem(storageKey) === '1');
  const [showChanges, setShowChanges] = useState(false);
  if (dismissed) return null;
  const oldPermissions = new Set(installed.manifest.permissions);
  const newPermissions = new Set(available.manifest.permissions);
  const added = available.manifest.permissions.filter((permission) => !oldPermissions.has(permission));
  const removed = installed.manifest.permissions.filter((permission) => !newPermissions.has(permission));
  const update = async () => {
    const response = await fetch(`/api/v1/plugins/${available.id}/install`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    if (!response.ok) throw new Error(`Plugin update failed (${response.status})`);
    onUpdated?.();
  };
  return <section role="status" aria-label={`Update available for ${installed.name}`}>
    <span>Update available: {installed.name} v{installed.manifest.version} → v{available.manifest.version}</span>
    <button type="button" onClick={() => { void update(); }}>Update now</button>
    <button type="button" onClick={() => setShowChanges(true)}>Changelog</button>
    <button type="button" onClick={() => { localStorage.setItem(storageKey, '1'); setDismissed(true); }}>Dismiss</button>
    {showChanges && <div role="dialog" aria-label="Plugin changelog">
      <p>Permissions added: {added.length ? added.join(', ') : 'none'}</p>
      <p>Permissions removed: {removed.length ? removed.join(', ') : 'none'}</p>
      <p>Render type: {installed.manifest.render_type} → {available.manifest.render_type}</p>
      <p>Entry point: {installed.manifest.entry_point} → {available.manifest.entry_point}</p>
      <button type="button" onClick={() => setShowChanges(false)}>Close</button>
    </div>}
  </section>;
}
