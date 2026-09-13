/**
 * Hermes Canopy — image viewer body (SPEC-PL-02 §9.2, phase 3).
 *
 * The string exported here is the SECOND <script> injected into the §8.2
 * sandboxed srcDoc by ViewerHost.buildViewerDoc (after the §8.3 shim). It
 * renders a zero-dependency fullscreen image viewer using ONLY the shim
 * surface:
 *   - canopy.__bootstrap (sync: fileMeta, streamUrl, config) for instant
 *     first paint
 *   - canopy.viewer.getStreamUrl() as the authoritative stream URL
 *     (reconciled over the bootstrap value; failure keeps the bootstrap URL)
 *   - canopy.viewer.on(...) for the spec §9.2 event names — frame-local this
 *     phase (the shim's `on` registry has no host delivery yet; host-side
 *     event handling lands in a later phase — documented in the brief)
 *
 * All pure math (zoom steps, clamps, fit, rotation) lives in
 * imageViewerLogic.ts and is unit-tested there. The helpers are injected
 * into the body script as an object literal built from Function.toString so
 * the EXACT tested source runs inside the iframe (property names survive
 * bundler minification; free identifiers would not).
 */

import {
  clampPan,
  clampSensitivity,
  clampZoom,
  computeFitZoom,
  keyboardZoomMultiplier,
  normalizeRotation,
  wheelZoomMultiplier,
} from './imageViewerLogic';

/** Helper bundle handed to the body script (property names are load-bearing). */
export const imageHelpers = {
  clampPan,
  clampSensitivity,
  clampZoom,
  computeFitZoom,
  keyboardZoomMultiplier,
  normalizeRotation,
  wheelZoomMultiplier,
};

type ImageHelpers = typeof imageHelpers;

function imageViewerBodyScript(H: ImageHelpers): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as {
    fileMeta?: { filename?: string; mimeType?: string };
    streamUrl?: string;
    config?: Record<string, unknown>;
  };
  var config = boot.config || {};
  var viewerApi = ((canopy && canopy.viewer) || {}) as Record<string, (...args: unknown[]) => unknown> & {
    on?: (event: string, handler: (payload: unknown) => void) => void;
  };

  function emit(event: string, payload: Record<string, unknown>): void {
    // Frame-local event dispatch through the shim's handler registry (the
    // map `canopy.viewer.on` maintains). Host-side delivery is a later
    // phase; inventing a postMessage envelope here is NOT done because the
    // mount nonce is closure-captured inside the shim and unreachable from
    // this script.
    var handlersMap = (canopy && (canopy as { __handlers?: Record<string, Array<(p: unknown) => void>> }).__handlers) || {};
    var list = handlersMap[event];
    if (Array.isArray(list)) {
      for (var i = 0; i < list.length; i += 1) {
        try {
          list[i](payload);
        } catch (_handlerError) {
          /* a broken handler must not kill the viewer */
        }
      }
    }
  }

  // ── DOM scaffold ──────────────────────────────────────────
  var rootEl = document.getElementById('root') || document.body;
  rootEl.innerHTML = '';
  rootEl.setAttribute(
    'style',
    'margin:0;width:100vw;height:100vh;overflow:hidden;position:relative;' +
      'background-color:#232323;' +
      'background-image:conic-gradient(#2e2e2e 25%, transparent 0 50%, #2e2e2e 0 75%, transparent 0);' +
      'background-size:24px 24px;',
  );

  var img = document.createElement('img');
  img.setAttribute('alt', (boot.fileMeta && boot.fileMeta.filename) || 'file preview');
  img.setAttribute(
    'style',
    'position:absolute;top:50%;left:50%;transform-origin:0 0;' +
      'user-select:none;-webkit-user-drag:none;image-rendering:auto;',
  );
  rootEl.appendChild(img);

  var errorBox = document.createElement('div');
  errorBox.setAttribute('data-image-error', '');
  errorBox.setAttribute(
    'style',
    'display:none;position:absolute;inset:0;margin:auto;width:min(480px,80vw);height:max-content;' +
      'padding:16px 20px;background:#3a1d1d;color:#ffb4b4;border:1px solid #7a2e2e;border-radius:8px;' +
      "font:13px/1.5 ui-monospace, monospace;white-space:pre-wrap;z-index:10;",
  );
  rootEl.appendChild(errorBox);

  var zoomHud = document.createElement('div');
  zoomHud.setAttribute('data-zoom-hud', '');
  zoomHud.setAttribute(
    'style',
    'position:absolute;right:12px;bottom:12px;padding:4px 10px;background:rgba(0,0,0,0.55);' +
      'color:#eee;border-radius:6px;font:12px/1.4 ui-monospace, monospace;z-index:10;pointer-events:none;',
  );
  rootEl.appendChild(zoomHud);

  // ── State ─────────────────────────────────────────────────
  var sensitivity = H.clampSensitivity(config.imageZoomSensitivity, 1.0);
  var zoom = 1;
  var rotation = 0;
  var panX = 0;
  var panY = 0;
  var fitZoom = 1;
  var dragging = false;
  var dragStartX = 0;
  var dragStartY = 0;
  var panStartX = 0;
  var panStartY = 0;

  function applyTransform(): void {
    img.style.transform =
      'translate(' + panX + 'px, ' + panY + 'px) translate(-50%, -50%) scale(' + zoom + ') rotate(' + rotation + 'deg)';
    zoomHud.textContent = Math.round(zoom * 100) + '%';
  }

  function setZoom(next: number): void {
    zoom = H.clampZoom(next);
    applyTransform();
    emit('image_zoom_changed', { zoom: zoom });
  }

  function setRotation(next: number): void {
    rotation = H.normalizeRotation(next);
    applyTransform();
    emit('image_rotated', { degrees: rotation });
  }

  function resetToFit(): void {
    setZoom(fitZoom);
    panX = 0;
    panY = 0;
    applyTransform();
  }

  function recomputeFit(): void {
    var rect = rootEl.getBoundingClientRect();
    fitZoom = H.computeFitZoom(rect.width, rect.height, img.naturalWidth || 0, img.naturalHeight || 0);
  }

  function showError(code: string, message: string): void {
    errorBox.style.display = 'block';
    errorBox.textContent = 'image_error\n' + code + '\n' + message;
    img.style.display = 'none';
    emit('image_error', { code: code, message: message });
  }

  img.addEventListener('load', function () {
    recomputeFit();
    resetToFit();
    emit('image_loaded', {
      width: img.naturalWidth,
      height: img.naturalHeight,
      mimeType: (boot.fileMeta && boot.fileMeta.mimeType) || '',
    });
  });
  img.addEventListener('error', function () {
    showError(
      'IMAGE_DECODE_ERROR',
      'The image could not be decoded or loaded. The file may be corrupt or in an unsupported format.',
    );
  });

  // ── Stream URL: bootstrap first paint, getStreamUrl authoritative ──
  var currentUrl = typeof boot.streamUrl === 'string' ? boot.streamUrl : '';
  function adoptStreamUrl(url: string): void {
    if (!url || url === currentUrl) return;
    currentUrl = url;
    img.src = url;
  }
  if (currentUrl) img.src = currentUrl;
  if (viewerApi && typeof viewerApi.getStreamUrl === 'function') {
    try {
      Promise.resolve(viewerApi.getStreamUrl(null))
        .then(function (result) {
          var url = (result as { url?: string } | null)?.url;
          if (typeof url === 'string') adoptStreamUrl(url);
        })
        .catch(function () {
          /* bootstrap URL already in place; an API failure must not blank the frame */
        });
    } catch (_streamError) {
      /* keep the bootstrap URL */
    }
  } else if (!currentUrl) {
    showError('STREAM_URL_MISSING', 'No stream URL was bootstrapped for this file.');
  }

  // ── Wheel zoom (viewport-centered) ────────────────────────
  rootEl.addEventListener(
    'wheel',
    function (event: WheelEvent) {
      event.preventDefault();
      setZoom(zoom * H.wheelZoomMultiplier(event.deltaY, sensitivity));
    },
    { passive: false },
  );

  // ── Click-and-drag pan ────────────────────────────────────
  rootEl.addEventListener('mousedown', function (event: MouseEvent) {
    dragging = true;
    dragStartX = event.clientX;
    dragStartY = event.clientY;
    panStartX = panX;
    panStartY = panY;
    event.preventDefault();
  });
  window.addEventListener('mousemove', function (event: MouseEvent) {
    if (!dragging) return;
    panX = clampPan(panStartX + (event.clientX - dragStartX));
    panY = clampPan(panStartY + (event.clientY - dragStartY));
    applyTransform();
  });
  window.addEventListener('mouseup', function () {
    dragging = false;
  });
  img.addEventListener('dragstart', function (event: Event) {
    event.preventDefault();
  });

  // ── Double-click reset ────────────────────────────────────
  rootEl.addEventListener('dblclick', function () {
    resetToFit();
  });

  // ── Keyboard: + − 0 1 [ ] ────────────────────────────────
  window.addEventListener('keydown', function (event: KeyboardEvent) {
    var key = event.key;
    if (key === '+' || key === '=') {
      setZoom(zoom * H.keyboardZoomMultiplier(1, sensitivity));
    } else if (key === '-' || key === '_') {
      setZoom(zoom * H.keyboardZoomMultiplier(-1, sensitivity));
    } else if (key === '0') {
      resetToFit();
    } else if (key === '1') {
      setZoom(1);
      panX = 0;
      panY = 0;
      applyTransform();
    } else if (key === '[') {
      setRotation(rotation - 90);
    } else if (key === ']') {
      setRotation(rotation + 90);
    }
  });

  window.addEventListener('resize', function () {
    recomputeFit();
    if (zoom === fitZoom) resetToFit();
  });
}

/** The full in-iframe script source (IIFE) injected after the shim. */
export const imageViewerBody: string =
  '(' +
  imageViewerBodyScript.toString() +
  ')(' +
  '{' +
  Object.entries(imageHelpers)
    .map(function ([name, fn]) {
      return name + ': ' + fn.toString();
    })
    .join(', ') +
  '});';
