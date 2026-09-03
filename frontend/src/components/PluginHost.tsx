import { useEffect, useMemo, useRef, useState } from 'react';
import { buildSandboxDoc, parseEmbeddedManifest, PluginSandboxHost, sha256Hex } from '../lib/pluginSandbox';
import type { PluginDataApi } from '../lib/pluginApi';
import { PluginApiHost } from '../lib/pluginApi';
import type { PluginEventPayload, PluginManifest, PluginPermission } from '../lib/pluginTypes';

export interface PluginHostProps {
  plugin: { id: string; name: string; manifest: PluginManifest; sourceJS: string };
  instanceId: string;
  grantedPermissions?: readonly PluginPermission[];
  dataApi?: PluginDataApi;
  theme?: string;
  className?: string;
  onEvent?: (event: PluginEventPayload) => void;
  onError?: (error: { code: string; message: string }) => void;
  eventSource?: EventSource;
}

export default function PluginHost({ plugin, instanceId, grantedPermissions, dataApi, theme, className, onEvent, onError, eventSource }: PluginHostProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null);
	const [currentPlugin, setCurrentPlugin] = useState(plugin);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
	useEffect(() => setCurrentPlugin(plugin), [plugin]);
  const permissions = useMemo(() => [...(grantedPermissions ?? currentPlugin.manifest.permissions)], [grantedPermissions, currentPlugin.manifest.permissions]);
  const host = useMemo(() => new PluginSandboxHost({
	pluginId: currentPlugin.id, instanceId, manifest: currentPlugin.manifest, grantedPermissions: permissions,
    api: new PluginApiHost(permissions, dataApi), theme, onEvent,
    onError: (value) => { setError(value.message); onError?.(value); },
	}), [currentPlugin.id, currentPlugin.manifest, instanceId, permissions, dataApi, theme, onEvent, onError]);
  const doc = useMemo(() => buildSandboxDoc({
	id: currentPlugin.id, instanceId, name: currentPlugin.name, manifest: currentPlugin.manifest,
	sourceJS: currentPlugin.sourceJS, nonce: host.nonce, parentOrigin: window.location.origin,
	}), [currentPlugin, instanceId, host]);

	useEffect(() => {
		const stream = eventSource ?? (typeof EventSource === 'undefined' ? null : new EventSource(`/api/v1/plugins/${currentPlugin.id}/events`));
		if (!stream) return;
		let cancelled = false;
		const reload = async (raw: MessageEvent<string>) => {
			const metadata = JSON.parse(raw.data) as { plugin_id: string; version: string; source_sha256: string };
			if (metadata.plugin_id !== currentPlugin.id || metadata.version === currentPlugin.manifest.version) return;
			let source = '';
			for (let attempt = 0; attempt < 2; attempt++) {
				const response = await fetch(`/api/v1/plugins/${metadata.plugin_id}/source`, { cache: 'no-store' });
				if (!response.ok) throw new Error(`Plugin source fetch failed (${response.status})`);
				source = await response.text();
				if (await sha256Hex(source) === metadata.source_sha256) break;
				console.warn('Plugin source SHA-256 mismatch; retrying hot reload'); source = '';
			}
			if (!source || cancelled) return;
			host.teardown();
			await new Promise((resolve) => setTimeout(resolve, 100));
			if (!cancelled) setCurrentPlugin({ ...currentPlugin, manifest: parseEmbeddedManifest(source), sourceJS: source });
		};
		const listener: EventListener = (event) => { void reload(event as MessageEvent<string>); };
		stream.addEventListener('plugin_updated', listener);
		stream.addEventListener('plugin_rolled_back', listener);
		return () => { cancelled = true; stream.removeEventListener('plugin_updated', listener); stream.removeEventListener('plugin_rolled_back', listener); if (!eventSource) stream.close(); };
	}, [currentPlugin, eventSource, host]);

  useEffect(() => {
    const iframe = iframeRef.current;
    if (iframe) host.attach(iframe);
    return () => host.teardown();
  }, [host]);

  if (error) return <div role="alert" className={className}>Plugin failed to load: {error}</div>;
  return (
    <div className={className} style={{ position: 'relative', width: '100%', height: '100%' }}>
      {!loaded && <div role="status">Loading plugin…</div>}
      <iframe
        ref={iframeRef}
		key={`${currentPlugin.id}-${currentPlugin.manifest.version}`}
		name={`canopy-plugin-${currentPlugin.id}-${instanceId}`}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        srcDoc={doc}
        onLoad={() => { setLoaded(true); host.initialize(); }}
		title={`${currentPlugin.name} plugin sandbox`}
        style={{ width: '100%', height: '100%', border: 0, display: 'block' }}
      />
    </div>
  );
}
