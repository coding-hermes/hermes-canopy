/**
 * Hermes Canopy — native audio/video viewer body (SPEC-PL-02 §9.7).
 *
 * Injected as ViewerHost's second sandbox script. The viewer paints from
 * canopy.__bootstrap immediately, reconciles the signed stream URL through
 * canopy.viewer, and emits lifecycle events only through the established
 * frame-local canopy.__handlers registry. Pure helpers are serialized from
 * mediaViewerLogic.ts so the tested functions are the functions run in-frame.
 */

import {
  clampMediaProgress,
  clampSeekTarget,
  clampVolume,
  finiteMediaDuration,
  formatMediaTime,
  keyboardJumpPercentage,
  mediaKindForMime,
  normalizePlaybackRate,
} from './mediaViewerLogic';

/** Helper bundle handed to the body script (property names are load-bearing). */
export const mediaHelpers = {
  clampMediaProgress,
  clampSeekTarget,
  clampVolume,
  finiteMediaDuration,
  formatMediaTime,
  keyboardJumpPercentage,
  mediaKindForMime,
  normalizePlaybackRate,
};

type MediaHelpers = typeof mediaHelpers;

function mediaViewerBodyScript(H: MediaHelpers): void {
  'use strict';
  var canopy = (window as unknown as { canopy?: Record<string, unknown> }).canopy;
  var boot = ((canopy && canopy.__bootstrap) || {}) as {
    fileMeta?: { id?: string; filename?: string; mimeType?: string };
    streamUrl?: string;
    config?: Record<string, unknown>;
  };
  var config = boot.config || {};
  var viewerApi = ((canopy && canopy.viewer) || {}) as Record<string, (...args: unknown[]) => unknown>;
  var fileMeta = boot.fileMeta || {};
  var mediaKind = H.mediaKindForMime(fileMeta.mimeType);

  function emit(event: string, payload: Record<string, unknown>): void {
    // The phase-2 shim exposes only its frame-local handler map. Host event
    // transport needs the closure-captured mount nonce and is intentionally
    // not invented here.
    var handlerMap =
      (canopy && (canopy as { __handlers?: Record<string, Array<(value: unknown) => void>> }).__handlers) || {};
    var handlers = handlerMap[event];
    if (!Array.isArray(handlers)) return;
    for (var index = 0; index < handlers.length; index += 1) {
      try {
        handlers[index](payload);
      } catch {
        /* one consumer cannot break media playback */
      }
    }
  }

  function safeLog(action: string, metadata: Record<string, unknown>): void {
    if (!viewerApi || typeof viewerApi.logAccess !== 'function') return;
    try {
      Promise.resolve(viewerApi.logAccess(action, metadata)).catch(function () {
        /* access logging is observational and must never break playback */
      });
    } catch {
      /* same guarantee for synchronous host stubs */
    }
  }

  function notifyReady(): void {
    if (!viewerApi || typeof viewerApi.ready !== 'function') return;
    try {
      Promise.resolve(viewerApi.ready()).catch(function () {
        /* rendering never depends on the acknowledgement */
      });
    } catch {
      /* rendering never depends on the acknowledgement */
    }
  }

  // ── DOM scaffold ──────────────────────────────────────────
  var rootEl = document.getElementById('root') || document.body;
  rootEl.innerHTML = '';
  rootEl.setAttribute('data-media-viewer', mediaKind || 'unsupported');
  rootEl.setAttribute(
    'style',
    'box-sizing:border-box;margin:0;width:100vw;height:100vh;overflow:auto;' +
      'display:flex;flex-direction:column;align-items:stretch;justify-content:center;gap:12px;' +
      'padding:16px;background:#111827;color:#f3f4f6;font:14px/1.4 system-ui,sans-serif;',
  );

  var errorBox = document.createElement('div');
  errorBox.setAttribute('data-media-error', '');
  errorBox.setAttribute('data-visible', 'false');
  errorBox.setAttribute('role', 'alert');
  errorBox.setAttribute(
    'style',
    'display:none;padding:12px 16px;background:#3f1d24;color:#fecdd3;border:1px solid #9f1239;' +
      'border-radius:8px;white-space:pre-wrap;font:13px/1.5 ui-monospace,monospace;',
  );
  rootEl.appendChild(errorBox);

  var media: HTMLMediaElement | null = null;

  function errorMessage(error: unknown, fallback: string): string {
    if (error instanceof Error && error.message) return error.message;
    if (typeof error === 'string' && error) return error;
    return fallback;
  }

  function showError(code: string, message: string): void {
    errorBox.style.display = 'block';
    errorBox.setAttribute('data-visible', 'true');
    errorBox.textContent = 'media_error\n' + code + '\n' + message;
    var payload = { code: code, message: message };
    emit('media_error', payload);
    safeLog('error', {
      code: code,
      errorCode: code,
      message: message,
      fileId: fileMeta.id || (canopy && canopy.fileId) || '',
      mediaKind: mediaKind || 'unsupported',
    });
  }

  if (mediaKind === null) {
    showError(
      'UNSUPPORTED_MEDIA_MIME',
      'The audio/video viewer cannot render MIME type "' + String(fileMeta.mimeType || '(missing)') + '".',
    );
    notifyReady();
    return;
  }

  media = document.createElement(mediaKind) as HTMLMediaElement;
  media.setAttribute('data-media-element', mediaKind);
  media.setAttribute(
    'aria-label',
    (fileMeta.filename || 'file') + ' ' + (mediaKind === 'audio' ? 'audio player' : 'video player'),
  );
  media.controls = true; // native fallback remains available alongside the custom controls
  media.autoplay = config.autoPlayMedia === true;
  media.loop = config.loopMedia === true;
  media.preload =
    config.preloadMedia === 'none' || config.preloadMedia === 'auto' || config.preloadMedia === 'metadata'
      ? config.preloadMedia
      : 'metadata';
  media.volume = H.clampVolume(config.audioVolume, 1);
  media.playbackRate = H.normalizePlaybackRate(config.playbackRate, 1);
  media.setAttribute(
    'style',
    mediaKind === 'video'
      ? 'display:block;width:100%;max-width:1200px;max-height:calc(100vh - 120px);margin:auto;background:#000;'
      : 'display:block;width:100%;max-width:900px;margin:auto;',
  );

  if (mediaKind === 'video' && typeof config.videoPoster === 'string' && config.videoPoster) {
    (media as HTMLVideoElement).poster = config.videoPoster;
  }
  if (typeof config.subtitleUrl === 'string' && config.subtitleUrl && config.showSubtitles !== false) {
    var track = document.createElement('track');
    track.kind = 'subtitles';
    track.label = 'Subtitles';
    track.srclang = 'en';
    track.src = config.subtitleUrl;
    track.default = true;
    media.appendChild(track);
  }
  rootEl.insertBefore(media, errorBox);

  var controls = document.createElement('div');
  controls.setAttribute('data-media-controls', '');
  controls.setAttribute('role', 'group');
  controls.setAttribute('aria-label', 'Media controls');
  controls.setAttribute(
    'style',
    'display:flex;align-items:center;gap:8px;flex-wrap:wrap;width:100%;max-width:1200px;' +
      'box-sizing:border-box;margin:0 auto;padding:10px;background:#1f2937;border:1px solid #374151;border-radius:8px;',
  );
  rootEl.appendChild(controls);

  function makeButton(marker: string, label: string): HTMLButtonElement {
    var button = document.createElement('button');
    button.type = 'button';
    button.setAttribute(marker, '');
    button.setAttribute('aria-label', label);
    button.textContent = label;
    button.setAttribute(
      'style',
      'padding:6px 10px;color:#f9fafb;background:#374151;border:1px solid #4b5563;border-radius:6px;cursor:pointer;',
    );
    controls.appendChild(button);
    return button;
  }

  var playButton = makeButton('data-media-play', 'Play');

  var seekInput = document.createElement('input');
  seekInput.type = 'range';
  seekInput.min = '0';
  seekInput.max = '0';
  seekInput.step = '0.1';
  seekInput.value = '0';
  seekInput.disabled = true;
  seekInput.setAttribute('data-media-seek', '');
  seekInput.setAttribute('aria-label', 'Seek');
  seekInput.setAttribute('aria-valuetext', '0:00 of 0:00');
  seekInput.setAttribute('style', 'flex:1 1 180px;min-width:120px;');
  controls.appendChild(seekInput);

  var timeText = document.createElement('output');
  timeText.setAttribute('data-media-time', '');
  timeText.setAttribute('aria-live', 'polite');
  timeText.textContent = '0:00 / 0:00';
  timeText.setAttribute('style', 'min-width:92px;font-variant-numeric:tabular-nums;');
  controls.appendChild(timeText);

  var muteButton = makeButton('data-media-mute', 'Mute');

  var volumeInput = document.createElement('input');
  volumeInput.type = 'range';
  volumeInput.min = '0';
  volumeInput.max = '1';
  volumeInput.step = '0.05';
  volumeInput.value = String(media.volume);
  volumeInput.setAttribute('data-media-volume', '');
  volumeInput.setAttribute('aria-label', 'Volume');
  volumeInput.setAttribute('style', 'width:90px;');
  controls.appendChild(volumeInput);

  var rateLabel = document.createElement('label');
  rateLabel.textContent = 'Speed ';
  rateLabel.setAttribute('style', 'white-space:nowrap;');
  var rateSelect = document.createElement('select');
  rateSelect.setAttribute('data-media-rate', '');
  rateSelect.setAttribute('aria-label', 'Playback rate');
  var rateChoices = [0.5, 1, 1.5, 2];
  var initialRate = H.normalizePlaybackRate(media.playbackRate, 1);
  if (rateChoices.indexOf(initialRate) === -1) rateChoices.push(initialRate);
  rateChoices.sort(function (left, right) {
    return left - right;
  });
  for (var rateIndex = 0; rateIndex < rateChoices.length; rateIndex += 1) {
    var option = document.createElement('option');
    option.value = String(rateChoices[rateIndex]);
    option.textContent = String(rateChoices[rateIndex]) + '×';
    rateSelect.appendChild(option);
  }
  rateSelect.value = String(initialRate);
  rateLabel.appendChild(rateSelect);
  controls.appendChild(rateLabel);

  var fullscreenButton: HTMLButtonElement | null = null;
  var pipButton: HTMLButtonElement | null = null;
  if (mediaKind === 'video') {
    fullscreenButton = makeButton('data-media-fullscreen', 'Fullscreen');
    if (config.pipEnabled !== false) {
      pipButton = makeButton('data-media-pip', 'Picture in picture');
      var pipDocument = document as Document & {
        pictureInPictureEnabled?: boolean;
        pictureInPictureElement?: Element | null;
        exitPictureInPicture?: () => Promise<unknown>;
      };
      var pipMedia = media as HTMLVideoElement & { requestPictureInPicture?: () => Promise<unknown> };
      pipButton.disabled = !(pipDocument.pictureInPictureEnabled && typeof pipMedia.requestPictureInPicture === 'function');
      pipButton.setAttribute('aria-disabled', pipButton.disabled ? 'true' : 'false');
    }
  }

  function currentDuration(): number {
    return H.finiteMediaDuration(media ? media.duration : 0);
  }

  function currentTime(): number {
    return H.clampMediaProgress(media ? media.currentTime : 0, currentDuration());
  }

  function updateTimeState(): void {
    if (!media) return;
    var duration = currentDuration();
    var progress = H.clampMediaProgress(media.currentTime, duration);
    seekInput.max = String(duration);
    seekInput.value = String(progress);
    seekInput.disabled = duration === 0;
    timeText.textContent = H.formatMediaTime(progress) + ' / ' + H.formatMediaTime(duration);
    seekInput.setAttribute(
      'aria-valuetext',
      H.formatMediaTime(progress) + ' of ' + H.formatMediaTime(duration),
    );
  }

  function seekTo(rawTarget: unknown): void {
    if (!media) return;
    var duration = currentDuration();
    if (duration === 0) return;
    var from = H.clampMediaProgress(media.currentTime, duration);
    var to = H.clampSeekTarget(rawTarget, duration);
    try {
      media.currentTime = to;
      updateTimeState();
      emit('media_seek', { from: from, to: to });
    } catch (error) {
      showError('MEDIA_SEEK_FAILED', errorMessage(error, 'The browser rejected the requested seek.'));
    }
  }

  function requestPlay(): void {
    if (!media) return;
    try {
      var result = media.play();
      if (result && typeof result.then === 'function') {
        Promise.resolve(result).catch(function (error) {
          showError('MEDIA_PLAY_REJECTED', errorMessage(error, 'The browser rejected playback.'));
        });
      }
    } catch (error) {
      showError('MEDIA_PLAY_REJECTED', errorMessage(error, 'The browser rejected playback.'));
    }
  }

  function togglePlayback(): void {
    if (!media) return;
    if (media.paused || media.ended) requestPlay();
    else media.pause();
  }

  function requestFullscreen(): void {
    if (!media || mediaKind !== 'video') return;
    var candidate = media as HTMLVideoElement & { requestFullscreen?: () => Promise<unknown> | void };
    if (typeof candidate.requestFullscreen !== 'function') return;
    try {
      Promise.resolve(candidate.requestFullscreen()).catch(function (error) {
        showError('MEDIA_FULLSCREEN_ERROR', errorMessage(error, 'Fullscreen is unavailable.'));
      });
    } catch (error) {
      showError('MEDIA_FULLSCREEN_ERROR', errorMessage(error, 'Fullscreen is unavailable.'));
    }
  }

  playButton.addEventListener('click', togglePlayback);
  muteButton.addEventListener('click', function () {
    if (!media) return;
    media.muted = !media.muted;
    muteButton.textContent = media.muted ? 'Unmute' : 'Mute';
    muteButton.setAttribute('aria-label', media.muted ? 'Unmute' : 'Mute');
  });
  volumeInput.addEventListener('input', function () {
    if (!media) return;
    media.volume = H.clampVolume(Number(volumeInput.value), media.volume);
    if (media.volume > 0) media.muted = false;
    muteButton.textContent = media.muted ? 'Unmute' : 'Mute';
    muteButton.setAttribute('aria-label', media.muted ? 'Unmute' : 'Mute');
  });
  seekInput.addEventListener('input', function () {
    var duration = currentDuration();
    var preview = H.clampSeekTarget(Number(seekInput.value), duration);
    timeText.textContent = H.formatMediaTime(preview) + ' / ' + H.formatMediaTime(duration);
  });
  seekInput.addEventListener('change', function () {
    seekTo(Number(seekInput.value));
  });

  var lastEmittedRate = initialRate;
  function emitRateChangeIfNeeded(): void {
    if (!media) return;
    var rate = H.normalizePlaybackRate(media.playbackRate, 1);
    if (rate === lastEmittedRate) return;
    lastEmittedRate = rate;
    emit('media_rate_change', { rate: rate });
  }
  rateSelect.addEventListener('change', function () {
    if (!media) return;
    media.playbackRate = H.normalizePlaybackRate(Number(rateSelect.value), 1);
    emitRateChangeIfNeeded();
  });
  media.addEventListener('ratechange', emitRateChangeIfNeeded);

  if (fullscreenButton) fullscreenButton.addEventListener('click', requestFullscreen);
  if (pipButton) {
    pipButton.addEventListener('click', function () {
      if (!media) return;
      var pipDocument = document as Document & {
        pictureInPictureElement?: Element | null;
        exitPictureInPicture?: () => Promise<unknown>;
      };
      var pipMedia = media as HTMLVideoElement & { requestPictureInPicture?: () => Promise<unknown> };
      try {
        var operation =
          pipDocument.pictureInPictureElement && typeof pipDocument.exitPictureInPicture === 'function'
            ? pipDocument.exitPictureInPicture()
            : typeof pipMedia.requestPictureInPicture === 'function'
              ? pipMedia.requestPictureInPicture()
              : null;
        if (operation) {
          Promise.resolve(operation).catch(function (error) {
            showError('MEDIA_PIP_ERROR', errorMessage(error, 'Picture in picture is unavailable.'));
          });
        }
      } catch (error) {
        showError('MEDIA_PIP_ERROR', errorMessage(error, 'Picture in picture is unavailable.'));
      }
    });
  }

  media.addEventListener('loadedmetadata', function () {
    if (!media) return;
    updateTimeState();
    var payload: Record<string, unknown> = {
      duration: currentDuration(),
      hasAudio:
        mediaKind === 'audio' ||
        Boolean((media as HTMLMediaElement & { audioTracks?: { length: number } }).audioTracks?.length),
      hasVideo: mediaKind === 'video',
    };
    if (mediaKind === 'video') {
      var video = media as HTMLVideoElement;
      if (Number.isFinite(video.videoWidth) && video.videoWidth > 0) payload.width = video.videoWidth;
      if (Number.isFinite(video.videoHeight) && video.videoHeight > 0) payload.height = video.videoHeight;
    }
    emit('media_loaded', payload);
  });
  media.addEventListener('durationchange', updateTimeState);
  media.addEventListener('timeupdate', updateTimeState);
  media.addEventListener('play', function () {
    playButton.textContent = 'Pause';
    playButton.setAttribute('aria-label', 'Pause');
    var at = currentTime();
    emit('media_play', { currentTime: at });
    safeLog('stream_start', { currentTime: at, mediaKind: mediaKind, fileId: fileMeta.id || '' });
  });
  media.addEventListener('pause', function () {
    playButton.textContent = 'Play';
    playButton.setAttribute('aria-label', 'Play');
    emit('media_pause', { currentTime: currentTime() });
  });
  media.addEventListener('ended', function () {
    playButton.textContent = 'Play';
    playButton.setAttribute('aria-label', 'Play');
    emit('media_ended', {});
  });
  media.addEventListener('volumechange', function () {
    if (!media) return;
    volumeInput.value = String(H.clampVolume(media.volume, 1));
    muteButton.textContent = media.muted ? 'Unmute' : 'Mute';
    muteButton.setAttribute('aria-label', media.muted ? 'Unmute' : 'Mute');
  });
  media.addEventListener('error', function () {
    if (!media) return;
    var nativeError = media.error as (MediaError & { message?: string }) | null;
    var codes: Record<number, string> = {
      1: 'MEDIA_ERR_ABORTED',
      2: 'MEDIA_ERR_NETWORK',
      3: 'MEDIA_ERR_DECODE',
      4: 'MEDIA_ERR_SRC_NOT_SUPPORTED',
    };
    var code = nativeError && codes[nativeError.code] ? codes[nativeError.code] : 'MEDIA_LOAD_ERROR';
    var message =
      nativeError && nativeError.message
        ? nativeError.message
        : 'The browser could not load or decode this media stream.';
    showError(code, message);
  });

  function isEditableTarget(target: EventTarget | null): boolean {
    var element = target as HTMLElement | null;
    while (element && element !== document.body) {
      var tagName = typeof element.tagName === 'string' ? element.tagName.toLowerCase() : '';
      if (tagName === 'input' || tagName === 'select' || tagName === 'textarea' || tagName === 'button') return true;
      var editable = typeof element.getAttribute === 'function' ? element.getAttribute('contenteditable') : null;
      if (editable !== null && editable !== 'false') return true;
      element = element.parentElement;
    }
    return false;
  }

  window.addEventListener('keydown', function (event: KeyboardEvent) {
    if (isEditableTarget(event.target)) return;
    var key = event.key;
    var lower = key.toLowerCase();
    var jump = H.keyboardJumpPercentage(key);
    if (key === ' ' || key === 'Spacebar' || event.code === 'Space') {
      event.preventDefault();
      togglePlayback();
    } else if (key === 'ArrowLeft') {
      event.preventDefault();
      seekTo(currentTime() - 5);
    } else if (key === 'ArrowRight') {
      event.preventDefault();
      seekTo(currentTime() + 5);
    } else if (lower === 'f' && mediaKind === 'video') {
      event.preventDefault();
      requestFullscreen();
    } else if (lower === 'm') {
      event.preventDefault();
      if (media) {
        media.muted = !media.muted;
        muteButton.textContent = media.muted ? 'Unmute' : 'Mute';
        muteButton.setAttribute('aria-label', media.muted ? 'Unmute' : 'Mute');
      }
    } else if (jump !== null) {
      event.preventDefault();
      seekTo(currentDuration() * jump);
    }
  });

  // ── Stream URL: bootstrap immediately, host URL authoritative ──
  var currentUrl = typeof boot.streamUrl === 'string' ? boot.streamUrl : '';
  function adoptStreamUrl(rawUrl: unknown): boolean {
    if (!media || typeof rawUrl !== 'string' || rawUrl.length === 0) return false;
    if (rawUrl !== currentUrl) {
      currentUrl = rawUrl;
      media.src = rawUrl;
    }
    return true;
  }
  if (currentUrl) media.src = currentUrl;

  safeLog('open', {
    fileId: fileMeta.id || (canopy && canopy.fileId) || '',
    filename: fileMeta.filename || '',
    mimeType: fileMeta.mimeType || '',
    mediaKind: mediaKind,
  });

  if (viewerApi && typeof viewerApi.getStreamUrl === 'function') {
    try {
      Promise.resolve(viewerApi.getStreamUrl(null))
        .then(function (result) {
          var response = result as { url?: unknown } | string | null;
          var url = typeof response === 'string' ? response : response && response.url;
          if (!adoptStreamUrl(url) && !currentUrl) {
            showError('STREAM_URL_MISSING', 'No playable stream URL is available for this file.');
          }
        })
        .catch(function () {
          if (!currentUrl) showError('STREAM_URL_MISSING', 'No playable stream URL is available for this file.');
          /* otherwise preserve the already-working bootstrap URL */
        });
    } catch {
      if (!currentUrl) showError('STREAM_URL_MISSING', 'No playable stream URL is available for this file.');
    }
  } else if (!currentUrl) {
    showError('STREAM_URL_MISSING', 'No stream URL was bootstrapped for this file.');
  }

  updateTimeState();
  notifyReady();
}

/** The full in-iframe script source (IIFE) injected after the shim. */
export const mediaViewerBody: string =
  '(' +
  mediaViewerBodyScript.toString() +
  ')(' +
  '{' +
  Object.entries(mediaHelpers)
    .map(function ([name, helper]) {
      return name + ': ' + helper.toString();
    })
    .join(', ') +
  '});';
