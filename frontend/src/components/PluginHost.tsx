import { useEffect, useMemo, useRef, useState } from 'react';
import { buildSandboxDoc, PluginSandboxHost } from '../lib/pluginSandbox';
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
}

export default function PluginHost({ plugin, instanceId, grantedPermissions, dataApi, theme, className, onEvent, onError }: PluginHostProps) {
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const permissions = useMemo(() => [...(grantedPermissions ?? plugin.manifest.permissions)], [grantedPermissions, plugin.manifest.permissions]);
  const host = useMemo(() => new PluginSandboxHost({
    pluginId: plugin.id, instanceId, manifest: plugin.manifest, grantedPermissions: permissions,
    api: new PluginApiHost(permissions, dataApi), theme, onEvent,
    onError: (value) => { setError(value.message); onError?.(value); },
  }), [plugin.id, plugin.manifest, instanceId, permissions, dataApi, theme, onEvent, onError]);
  const doc = useMemo(() => buildSandboxDoc({
    id: plugin.id, instanceId, name: plugin.name, manifest: plugin.manifest,
    sourceJS: plugin.sourceJS, nonce: host.nonce, parentOrigin: window.location.origin,
  }), [plugin, instanceId, host]);

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
        name={`canopy-plugin-${plugin.id}-${instanceId}`}
        sandbox="allow-scripts"
        referrerPolicy="no-referrer"
        srcDoc={doc}
        onLoad={() => { setLoaded(true); host.initialize(); }}
        title={`${plugin.name} plugin sandbox`}
        style={{ width: '100%', height: '100%', border: 0, display: 'block' }}
      />
    </div>
  );
}
