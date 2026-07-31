package plugin

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// canopyShimJS is the host-injected `canopy` API shim from SPEC-PL-01 §7.3,
// verbatim. Placeholders are substituted by BuildSrcDoc:
//
//	__PLUGIN_ID__      plugin registry id
//	__INSTANCE_ID__    plugin instance id
//	__NONCE__          per-session random hex nonce
//	__PARENT_ORIGIN__  host page origin
//	__ENTRY_POINT__    manifest entry_point
const canopyShimJS = `(function() {
  const PLUGIN_ID = '__PLUGIN_ID__';
  const INSTANCE_ID = '__INSTANCE_ID__';
  const NONCE = '__NONCE__';
  const PARENT_ORIGIN = '__PARENT_ORIGIN__';

  let nextId = 1;
  const pendingCalls = new Map();

  window.addEventListener('message', (event) => {
    if (event.origin !== PARENT_ORIGIN) return;
    const msg = event.data;
    if (msg.target !== 'plugin:' + PLUGIN_ID + ':' + INSTANCE_ID) return;
    if (msg.nonce !== NONCE) return;

    if (msg.type === 'api_response' && pendingCalls.has(msg.id)) {
      const { resolve, reject } = pendingCalls.get(msg.id);
      pendingCalls.delete(msg.id);
      if (msg.error) reject(new CanopyAPIError(msg.error.code, msg.error.message));
      else resolve(msg.result);
    }
  });

  class CanopyAPIError extends Error {
    constructor(code, message) {
      super(message);
      this.code = code;
    }
  }

  function callAPI(method, params) {
    return new Promise((resolve, reject) => {
      const id = 'call-' + nextId++;
      pendingCalls.set(id, { resolve, reject });
      window.parent.postMessage({
        type: 'api_call',
        id,
        target: 'host',
        payload: { method, params },
        nonce: NONCE,
        timestamp: Date.now(),
      }, PARENT_ORIGIN);

      // Timeout after 30s
      setTimeout(() => {
        if (pendingCalls.has(id)) {
          pendingCalls.delete(id);
          reject(new CanopyAPIError('TIMEOUT', 'API call ' + method + ' timed out'));
        }
      }, 30000);
    });
  }

  // Public API
  window.canopy = {
    version: '1.0.0',
    pluginId: PLUGIN_ID,
    instanceId: INSTANCE_ID,

    data: {
      query: (params) => callAPI('data.query', params),
      mutate: (params) => callAPI('data.mutate', params),
    },
    notify: (params) => callAPI('notify', params),
    calendar: {
      query: (params) => callAPI('calendar.query', params),
      create: (params) => callAPI('calendar.create', params),
    },
    network: {
      fetch: (params) => callAPI('network.fetch', params),
    },

    on: (event, handler) => { /* register host event listener */ },
    emit: (event, data) => { /* postMessage('event', {event, data}) to host */ },

    error: CanopyAPIError,
  };

  // Tell host we're ready
  window.parent.postMessage({
    type: 'ready',
    id: 'ready-' + Date.now(),
    target: 'host',
    payload: { entryPoint: '__ENTRY_POINT__' },
    nonce: NONCE,
    timestamp: Date.now(),
  }, PARENT_ORIGIN);
})();`

// BuildSrcDoc renders the sandboxed iframe document for a plugin
// (SPEC-PL-01 §7.1/§7.3). nonce is a per-session random hex string;
// parentOrigin is the host page origin. The returned document carries the
// fixed CSP meta, the #root mount div, the canopy shim, and the plugin
// source evaluated in an IIFE after the shim.
func BuildSrcDoc(p *Plugin, instanceID uuid.UUID, nonce, parentOrigin string) string {
	entryPoint := "main"
	if p != nil {
		if m, err := p.Manifest(); err == nil && m.EntryPoint != "" {
			entryPoint = m.EntryPoint
		}
	}
	pluginID := ""
	if p != nil {
		pluginID = p.ID.String()
	}

	shim := strings.NewReplacer(
		"__PLUGIN_ID__", pluginID,
		"__INSTANCE_ID__", instanceID.String(),
		"__NONCE__", nonce,
		"__PARENT_ORIGIN__", parentOrigin,
		"__ENTRY_POINT__", entryPoint,
	).Replace(canopyShimJS)

	source := ""
	if p != nil {
		source = p.SourceJS
	}

	return fmt.Sprintf(`<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <meta http-equiv="Content-Security-Policy"
    content="default-src 'none';
             script-src 'unsafe-inline';
             style-src 'unsafe-inline';
             connect-src none;
             img-src data: https:;
             font-src data:;" />
  <title>%s</title>
</head>
<body>
  <div id="root"></div>
  <script>
%s
    (function() {
      // Plugin source is evaluated in this scope
%s
    })();
  </script>
</body>
</html>`, pluginTitle(p), shim, indentSource(source, 6))
}

// pluginTitle returns a safe document title for the iframe.
func pluginTitle(p *Plugin) string {
	if p == nil || p.Name == "" {
		return "canopy-plugin"
	}
	return p.Name
}

// indentSource indents every line of the plugin source so it nests inside
// the IIFE in the generated document.
func indentSource(source string, spaces int) string {
	if source == "" {
		return ""
	}
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lines[i] = pad + line
	}
	return strings.Join(lines, "\n")
}
