/* ============================================================
   SMB Web Application – Client-side JavaScript
   ============================================================ */

'use strict';

/* ── Format Helpers ─────────────────────────────────────────
   Utility functions for human-readable sizes and durations.
   ─────────────────────────────────────────────────────────── */

/**
 * Format a byte count into a human-readable string.
 * @param {number} bytes
 * @returns {string}
 */
function formatSize(bytes) {
  if (bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const exp = Math.min(Math.floor(Math.log2(bytes) / 10), units.length - 1);
  const value = bytes / Math.pow(1024, exp);
  return value % 1 === 0
    ? `${value} ${units[exp]}`
    : `${value.toFixed(exp === 0 ? 0 : 1)} ${units[exp]}`;
}

/**
 * Format a duration in seconds into a human-readable string.
 * @param {number} seconds
 * @returns {string}
 */
function formatDuration(seconds) {
  if (!isFinite(seconds) || seconds < 0) return '--';
  seconds = Math.ceil(seconds);
  if (seconds < 60) return `${seconds} 秒`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  if (m < 60) return s > 0 ? `${m} 分 ${s} 秒` : `${m} 分钟`;
  const h = Math.floor(m / 60);
  const rm = m % 60;
  return rm > 0 ? `${h} 小时 ${rm} 分钟` : `${h} 小时`;
}


/* ── Toast Notifications ────────────────────────────────────
   Temporary notification messages shown at the top-right.
   ─────────────────────────────────────────────────────────── */

let _toastContainer = null;

function _getToastContainer() {
  if (!_toastContainer) {
    _toastContainer = document.getElementById('toast-container');
    if (!_toastContainer) {
      _toastContainer = document.createElement('div');
      _toastContainer.id = 'toast-container';
      document.body.appendChild(_toastContainer);
    }
  }
  return _toastContainer;
}

/**
 * Show a transient toast notification.
 * @param {string} message
 * @param {'success'|'error'|'info'} [type='info']
 * @param {number} [duration=3000]  Auto-dismiss delay in ms.
 */
function showToast(message, type = 'info', duration = 3000) {
  const icons = { success: '✓', error: '✕', info: 'ℹ' };
  const container = _getToastContainer();

  const el = document.createElement('div');
  el.className = `toast toast-${type}`;
  el.innerHTML = `<span class="toast-icon">${icons[type] || icons.info}</span>
                  <span class="toast-msg">${_escapeHTML(message)}</span>`;

  container.appendChild(el);

  const dismiss = () => {
    el.classList.add('removing');
    el.addEventListener('animationend', () => el.remove(), { once: true });
    // Fallback removal in case animation doesn't fire
    setTimeout(() => el.remove(), 400);
  };

  const timer = setTimeout(dismiss, duration);
  el.addEventListener('click', () => { clearTimeout(timer); dismiss(); });
}


/* ── API Helper ─────────────────────────────────────────────
   Thin fetch wrapper with JSON handling and error surface.
   ─────────────────────────────────────────────────────────── */

/**
 * Make an API call and return the parsed JSON response.
 *
 * @param {'GET'|'POST'|'PUT'|'PATCH'|'DELETE'} method
 * @param {string} url
 * @param {object|null} [body]        Will be JSON-serialised unless options.rawBody is true.
 * @param {object} [options]
 * @param {boolean}  [options.rawBody]   Send body as-is (e.g. Blob/ArrayBuffer).
 * @param {AbortSignal} [options.signal]
 * @param {Record<string,string>} [options.headers]
 * @returns {Promise<any>}
 * @throws {Error} with a .status property on HTTP error
 */
async function apiCall(method, url, body = null, options = {}) {
  const headers = Object.assign({}, options.headers);
  let fetchBody = undefined;

  if (body !== null && body !== undefined) {
    if (options.rawBody) {
      fetchBody = body;
    } else {
      headers['Content-Type'] = 'application/json';
      fetchBody = JSON.stringify(body);
    }
  }

  let response;
  try {
    response = await fetch(url, {
      method,
      headers,
      body: fetchBody,
      signal: options.signal,
    });
  } catch (err) {
    if (err.name === 'AbortError') throw err;
    const networkErr = new Error('网络请求失败，请检查网络连接');
    networkErr.name = 'NetworkError';
    throw networkErr;
  }

  let data;
  const contentType = response.headers.get('content-type') || '';
  if (contentType.includes('application/json')) {
    try { data = await response.json(); } catch (_) { data = null; }
  } else {
    data = await response.text();
  }

  if (!response.ok) {
    const message =
      (data && typeof data === 'object' && (data.message || data.error || data.detail)) ||
      (typeof data === 'string' && data) ||
      `请求失败 (${response.status})`;
    const err = new Error(message);
    err.status = response.status;
    err.data = data;
    throw err;
  }

  return data;
}


/* ── Clipboard Helper ───────────────────────────────────────
   Copy text to clipboard and give visual feedback on button.
   ─────────────────────────────────────────────────────────── */

/**
 * Copy text to the clipboard; optionally update a button's label.
 * @param {string} text
 * @param {HTMLElement|null} [buttonEl]
 * @returns {Promise<void>}
 */
async function copyToClipboard(text, buttonEl = null) {
  try {
    await navigator.clipboard.writeText(text);
    if (buttonEl) {
      const original = buttonEl.textContent;
      buttonEl.textContent = '已复制';
      buttonEl.disabled = true;
      setTimeout(() => {
        buttonEl.textContent = original;
        buttonEl.disabled = false;
      }, 1800);
    }
    showToast('已复制到剪贴板', 'success');
  } catch (_) {
    // Fallback for browsers without clipboard API
    try {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.cssText = 'position:fixed;left:-9999px;top:-9999px;opacity:0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
      if (buttonEl) {
        const original = buttonEl.textContent;
        buttonEl.textContent = '已复制';
        buttonEl.disabled = true;
        setTimeout(() => {
          buttonEl.textContent = original;
          buttonEl.disabled = false;
        }, 1800);
      }
      showToast('已复制到剪贴板', 'success');
    } catch (e) {
      showToast('复制失败，请手动复制', 'error');
    }
  }
}


/* ── Confirmation Dialog ────────────────────────────────────
   Lightweight confirm wrapper (uses native dialog or fallback).
   ─────────────────────────────────────────────────────────── */

/**
 * Show a confirmation dialog and invoke onConfirm if the user accepts.
 * @param {string} message
 * @param {function} onConfirm
 * @param {object} [opts]
 * @param {string} [opts.title]
 * @param {string} [opts.confirmText]
 * @param {string} [opts.cancelText]
 * @param {'danger'|'primary'} [opts.confirmStyle]
 */
function confirmAction(message, onConfirm, opts = {}) {
  const title       = opts.title       || '请确认';
  const confirmText = opts.confirmText || '确定';
  const cancelText  = opts.cancelText  || '取消';
  const style       = opts.confirmStyle || 'danger';

  // Remove any existing confirmation modal
  const existing = document.getElementById('_confirmModal');
  if (existing) existing.remove();

  const overlay = document.createElement('div');
  overlay.id = '_confirmModal';
  overlay.className = 'modal-overlay';
  overlay.innerHTML = `
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="_confirmTitle">
      <div class="modal-title" id="_confirmTitle">${_escapeHTML(title)}</div>
      <div class="modal-body">${_escapeHTML(message)}</div>
      <div class="modal-actions">
        <button class="btn btn-outline" id="_confirmCancel">${_escapeHTML(cancelText)}</button>
        <button class="btn btn-${style}" id="_confirmOk">${_escapeHTML(confirmText)}</button>
      </div>
    </div>`;

  document.body.appendChild(overlay);

  const close = () => {
    overlay.classList.add('hidden');
    overlay.remove();
  };

  overlay.querySelector('#_confirmCancel').addEventListener('click', close);
  overlay.querySelector('#_confirmOk').addEventListener('click', () => {
    close();
    onConfirm();
  });

  // Close on backdrop click
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) close();
  });

  // Close on Escape
  const keyHandler = (e) => {
    if (e.key === 'Escape') { close(); document.removeEventListener('keydown', keyHandler); }
  };
  document.addEventListener('keydown', keyHandler);

  // Focus the cancel button by default
  requestAnimationFrame(() => overlay.querySelector('#_confirmCancel').focus());
}


/* ── URL Verification ───────────────────────────────────────
   Trigger a server-side URL verification for a request.
   ─────────────────────────────────────────────────────────── */

/**
 * Verify the target URL of a sign/release request.
 * @param {string} requestId
 * @param {HTMLElement} buttonEl
 * @param {HTMLElement} resultEl   Element to display the result in.
 */
async function verifyURL(requestId, buttonEl, resultEl) {
  const originalText = buttonEl.textContent;
  buttonEl.disabled = true;
  buttonEl.innerHTML = '<span class="spinner"></span> 验证中…';

  if (resultEl) {
    resultEl.textContent = '';
    resultEl.className = '';
  }

  try {
    const data = await apiCall('POST', `/api/requests/${encodeURIComponent(requestId)}/verify-url`);
    const ok = data && data.reachable;

    if (resultEl) {
      resultEl.className = `alert alert-${ok ? 'success' : 'error'} mt-2`;
      resultEl.innerHTML = ok
        ? `<span class="alert-icon">✓</span> URL 可访问 (HTTP ${data.status_code || 200})`
        : `<span class="alert-icon">✕</span> URL 不可访问：${_escapeHTML(data.message || '未知错误')}`;
    }

    showToast(ok ? 'URL 验证通过' : 'URL 无法访问', ok ? 'success' : 'error');
  } catch (err) {
    if (resultEl) {
      resultEl.className = 'alert alert-error mt-2';
      resultEl.innerHTML = `<span class="alert-icon">✕</span> 验证请求失败：${_escapeHTML(err.message)}`;
    }
    showToast(`验证失败：${err.message}`, 'error');
  } finally {
    buttonEl.disabled = false;
    buttonEl.textContent = originalText;
  }
}


/* ── ChunkedUploader ────────────────────────────────────────
   Uploads a single File to the server using multi-part upload.

   Flow:
     POST initURL           → { draft_id, chunk_size }
     POST partURL?part_number=N  (raw body, repeat per chunk)
     POST completeURL

   Progress callback receives:
     { uploaded, total, speed, eta, percent }
   ─────────────────────────────────────────────────────────── */

class ChunkedUploader {
  /**
   * Create a ChunkedUploader.
   *
   * Supports two calling conventions:
   *
   * 1. Auto-init mode (the uploader calls initURL itself):
   *    new ChunkedUploader(file, initURL, partURLTemplate, completeURL, options?)
   *
   * 2. Pre-initialized mode (caller already has draftId/chunkSize from a prior init call):
   *    new ChunkedUploader(file, { draftId, chunkSize, partURL, completeURL, ...options })
   *
   * @param {File} file
   * @param {string|object} initURLOrConfig
   * @param {string} [partURLTemplate]   String with "{draft_id}" placeholder (mode 1 only).
   * @param {string} [completeURL]       String with "{draft_id}" placeholder (mode 1 only).
   * @param {object} [options]
   */
  constructor(file, initURLOrConfig, partURLTemplate, completeURL, options = {}) {
    this.file = file;

    if (typeof initURLOrConfig === 'object' && initURLOrConfig !== null) {
      // Pre-initialized mode: caller passes a config object as 2nd arg.
      const cfg = initURLOrConfig;
      this.initURL         = null; // no init call needed
      this.partURLTemplate = null;
      this.completeURL     = null;
      // Store pre-resolved values.
      this._preInitDraftId   = cfg.draftId;
      this._preInitChunkSize = cfg.chunkSize;
      this._preInitPartURL   = cfg.partURL;
      this._preInitCompleteURL = cfg.completeURL;
      this.chunkSizeOverride = cfg.chunkSize || null;
      this.maxConcurrency    = cfg.maxConcurrency ?? 3;
      this.maxRetries        = cfg.maxRetries    ?? 3;
    } else {
      // Auto-init mode: positional arguments.
      this.initURL         = initURLOrConfig;
      this.partURLTemplate = partURLTemplate;
      this.completeURL     = completeURL;
      this._preInitDraftId   = null;
      this._preInitChunkSize = null;
      this._preInitPartURL   = null;
      this._preInitCompleteURL = null;
      this.chunkSizeOverride = options.chunkSize    || null;
      this.maxConcurrency    = options.maxConcurrency ?? 3;
      this.maxRetries        = options.maxRetries    ?? 3;
    }

    this._onProgress = null;
    this._onComplete = null;
    this._onError    = null;

    this._abortController = new AbortController();
    this._aborted         = false;

    // State
    this._draftId    = null;
    this._chunkSize  = null;
    this._chunks     = [];       // Array of { index, start, end, uploaded: bool }
    this._uploaded   = 0;        // bytes confirmed uploaded
    this._speedWindow = [];      // [{ ts, bytes }, …] last 5 s
  }

  /** Register a progress callback. */
  onProgress(cb) { this._onProgress = cb; return this; }
  /** Register a completion callback. Receives server response from completeURL. */
  onComplete(cb) { this._onComplete = cb; return this; }
  /** Register an error callback. */
  onError(cb)    { this._onError    = cb; return this; }

  /** Cancel any in-flight requests. */
  abort() {
    this._aborted = true;
    this._abortController.abort();
  }

  /** Begin the upload. Returns a Promise that resolves with the complete response. */
  async start() {
    try {
      return await this._run();
    } catch (err) {
      if (this._onError) this._onError(err);
      throw err;
    }
  }

  // ── Internal ──────────────────────────────────────────────

  async _run() {
    // 1. Initialise upload (or use pre-initialised values)
    if (this._preInitDraftId) {
      // Pre-initialized mode: skip init API call.
      this._draftId  = this._preInitDraftId;
      this._chunkSize = this._preInitChunkSize || (5 * 1024 * 1024);
    } else {
      // Auto-init mode: call initURL to get draft_id and chunk_size.
      const init = await apiCall('POST', this.initURL, { filename: this.file.name, size: this.file.size });
      this._draftId  = init.draft_id;
      this._chunkSize = this.chunkSizeOverride || init.chunk_size || (5 * 1024 * 1024);
    }

    // 2. Slice into chunks
    this._chunks = [];
    let offset = 0;
    let index  = 0;
    while (offset < this.file.size) {
      const end = Math.min(offset + this._chunkSize, this.file.size);
      this._chunks.push({ index, start: offset, end, uploaded: false });
      offset = end;
      index++;
    }
    // Edge case: empty file
    if (this._chunks.length === 0) {
      this._chunks.push({ index: 0, start: 0, end: 0, uploaded: false });
    }

    this._uploaded = 0;
    this._speedWindow = [];

    // 3. Upload parts with concurrency limit
    const queue = [...this._chunks];
    const inFlight = new Set();

    const runNext = async () => {
      if (this._aborted) return;
      if (queue.length === 0) return;

      const chunk = queue.shift();
      const promise = this._uploadChunk(chunk).then(() => {
        inFlight.delete(promise);
        return runNext();
      });
      inFlight.add(promise);

      // Fill up to maxConcurrency
      if (inFlight.size < this.maxConcurrency && queue.length > 0) {
        await runNext();
      }

      await promise;
    };

    // Kick off up to maxConcurrency initial workers
    const workers = [];
    for (let i = 0; i < Math.min(this.maxConcurrency, this._chunks.length); i++) {
      workers.push(runNext());
    }
    await Promise.all(workers);

    if (this._aborted) throw new Error('上传已取消');

    // 4. Complete
    const completeURL = this._preInitCompleteURL || this._buildURL(this.completeURL);

    const result = await apiCall('POST', completeURL, {
      draft_id:   this._draftId,
      part_count: this._chunks.length,
    });

    if (this._onComplete) this._onComplete(result);
    return result;
  }

  async _uploadChunk(chunk) {
    const basePartURL = this._preInitPartURL || this._buildURL(this.partURLTemplate);
    const partURL = basePartURL + (basePartURL.includes('?') ? '&' : '?') +
                    `part_number=${chunk.index + 1}`;

    const blob  = this.file.slice(chunk.start, chunk.end);
    let attempt = 0;

    while (attempt <= this.maxRetries) {
      if (this._aborted) throw new Error('上传已取消');

      try {
        await apiCall('POST', partURL, blob, {
          rawBody: true,
          signal:  this._abortController.signal,
          headers: { 'Content-Type': 'application/octet-stream' },
        });

        chunk.uploaded = true;
        const bytes = chunk.end - chunk.start;
        this._recordProgress(bytes);
        return;
      } catch (err) {
        if (this._aborted || err.name === 'AbortError') throw err;

        attempt++;
        if (attempt > this.maxRetries) throw err;

        // Exponential back-off: 500 ms, 1 s, 2 s
        const delay = 500 * Math.pow(2, attempt - 1);
        await _sleep(delay);
      }
    }
  }

  _recordProgress(bytes) {
    this._uploaded += bytes;
    const now = Date.now();
    this._speedWindow.push({ ts: now, bytes });

    // Keep only the last 5 seconds
    const cutoff = now - 5000;
    this._speedWindow = this._speedWindow.filter(e => e.ts >= cutoff);

    const windowBytes = this._speedWindow.reduce((s, e) => s + e.bytes, 0);
    const windowSecs  = this._speedWindow.length > 1
      ? (now - this._speedWindow[0].ts) / 1000
      : 1;
    const speed  = windowSecs > 0 ? windowBytes / windowSecs : 0;
    const remain = this.file.size - this._uploaded;
    const eta    = speed > 0 ? remain / speed : Infinity;
    const percent = this.file.size > 0
      ? Math.round((this._uploaded / this.file.size) * 100)
      : 100;

    if (this._onProgress) {
      this._onProgress({
        uploaded: this._uploaded,
        total:    this.file.size,
        speed,
        eta,
        percent,
      });
    }
  }

  _buildURL(template) {
    return template.replace(/\{draft_id\}/g, encodeURIComponent(this._draftId || ''));
  }
}


/* ── MultiFileUploader ──────────────────────────────────────
   Uploads multiple files sequentially for request creation.
   Exposes per-file and overall progress.
   ─────────────────────────────────────────────────────────── */

class MultiFileUploader {
  /**
   * @param {File[]} files
   * @param {object} config
   * @param {string} config.initURL
   * @param {string} config.partURLTemplate
   * @param {string} config.completeURL
   * @param {object} [config.uploaderOptions]  Passed to ChunkedUploader.
   */
  constructor(files, config) {
    this.files  = Array.from(files);
    this.config = config;

    this._onFileProgress   = null;
    this._onFileComplete   = null;
    this._onOverallProgress = null;
    this._onAllComplete    = null;
    this._onError          = null;

    this._current  = null;   // Active ChunkedUploader
    this._aborted  = false;
    this._results  = [];
  }

  onFileProgress(cb)    { this._onFileProgress    = cb; return this; }
  onFileComplete(cb)    { this._onFileComplete    = cb; return this; }
  onOverallProgress(cb) { this._onOverallProgress = cb; return this; }
  onAllComplete(cb)     { this._onAllComplete     = cb; return this; }
  onError(cb)           { this._onError           = cb; return this; }

  abort() {
    this._aborted = true;
    if (this._current) this._current.abort();
  }

  async start() {
    const totalBytes = this.files.reduce((s, f) => s + f.size, 0);
    let uploadedBytes = 0;
    let prevFileBytes = 0;

    for (let i = 0; i < this.files.length; i++) {
      if (this._aborted) break;
      const file = this.files[i];

      const uploader = new ChunkedUploader(
        file,
        this.config.initURL,
        this.config.partURLTemplate,
        this.config.completeURL,
        this.config.uploaderOptions || {}
      );
      this._current = uploader;

      uploader.onProgress((p) => {
        if (this._onFileProgress) {
          this._onFileProgress({ fileIndex: i, file, ...p });
        }
        if (this._onOverallProgress) {
          const overallUploaded = uploadedBytes + p.uploaded;
          const overallPercent  = totalBytes > 0
            ? Math.round((overallUploaded / totalBytes) * 100)
            : 100;
          this._onOverallProgress({
            fileIndex: i,
            totalFiles: this.files.length,
            uploaded: overallUploaded,
            total:    totalBytes,
            percent:  overallPercent,
            speed:    p.speed,
            eta:      p.eta,
          });
        }
      });

      try {
        const result = await uploader.start();
        this._results.push({ file, result });
        uploadedBytes += file.size;
        prevFileBytes  = file.size;

        if (this._onFileComplete) {
          this._onFileComplete({ fileIndex: i, file, result });
        }
      } catch (err) {
        if (this._onError) this._onError({ fileIndex: i, file, error: err });
        throw err;
      }
    }

    if (!this._aborted && this._onAllComplete) {
      this._onAllComplete(this._results);
    }

    return this._results;
  }
}


/* ── Upload UI Helper ───────────────────────────────────────
   Wire up a drag-and-drop zone + file input to a progress bar.
   ─────────────────────────────────────────────────────────── */

/**
 * Initialise a complete upload UI.
 *
 * @param {object} config
 * @param {string}   config.uploadZoneId      ID of the .upload-zone element.
 * @param {string}   config.progressBarId     ID of the .progress-bar element.
 * @param {string}   config.fileInputId       ID of the <input type="file"> element.
 * @param {string}   config.initURL
 * @param {string}   config.partURLTemplate
 * @param {string}   config.completeURL
 * @param {function} [config.onSuccess]       Called with server response on completion.
 * @param {function} [config.onFileSelected]  Called with File when a file is chosen.
 * @param {object}   [config.uploaderOptions]
 */
function initUploadUI(config) {
  const zone      = document.getElementById(config.uploadZoneId);
  const barWrap   = document.getElementById(config.progressBarId);
  const fileInput = document.getElementById(config.fileInputId);

  if (!zone || !barWrap || !fileInput) {
    console.warn('initUploadUI: one or more elements not found', config);
    return;
  }

  const fill     = barWrap.querySelector('.progress-bar-fill');
  const infoEl   = barWrap.querySelector('.progress-info');
  const textEl   = barWrap.querySelector('.progress-text');

  let activeUploader = null;
  let _beforeUnloadBound = false;
  const _beforeUnload = (e) => { e.preventDefault(); e.returnValue = ''; };

  function _addBeforeUnload() {
    if (!_beforeUnloadBound) {
      window.addEventListener('beforeunload', _beforeUnload);
      _beforeUnloadBound = true;
    }
  }
  function _removeBeforeUnload() {
    if (_beforeUnloadBound) {
      window.removeEventListener('beforeunload', _beforeUnload);
      _beforeUnloadBound = false;
    }
  }

  function _setProgress(p) {
    if (fill)   fill.style.width = `${p.percent}%`;
    if (textEl) textEl.textContent = `${p.percent}%`;
    if (infoEl) {
      infoEl.innerHTML =
        `<span>${formatSize(p.uploaded)} / ${formatSize(p.total)}</span>` +
        `<span>${formatSize(p.speed)}/s &nbsp;·&nbsp; 剩余 ${formatDuration(p.eta)}</span>`;
    }
    barWrap.classList.remove('hidden');
  }

  function _handleFile(file) {
    if (!file) return;
    if (config.onFileSelected) config.onFileSelected(file);

    // Show zone as "selected"
    zone.classList.add('has-file');
    const titleEl = zone.querySelector('.upload-zone-title');
    const hintEl  = zone.querySelector('.upload-zone-hint');
    if (titleEl) titleEl.textContent = file.name;
    if (hintEl)  hintEl.textContent  = formatSize(file.size);

    // Reset progress
    if (fill)   fill.style.width = '0%';
    if (textEl) textEl.textContent = '0%';
    if (infoEl) infoEl.textContent = '';
    barWrap.classList.remove('hidden');
    if (fill) fill.classList.add('animated');

    _addBeforeUnload();

    activeUploader = new ChunkedUploader(
      file,
      config.initURL,
      config.partURLTemplate,
      config.completeURL,
      config.uploaderOptions || {}
    );

    activeUploader
      .onProgress(_setProgress)
      .onComplete((res) => {
        _removeBeforeUnload();
        if (fill) fill.classList.remove('animated');
        barWrap.classList.add('success');
        _setProgress({ percent: 100, uploaded: file.size, total: file.size, speed: 0, eta: 0 });
        showToast('文件上传成功', 'success');
        if (config.onSuccess) config.onSuccess(res);
        activeUploader = null;
      })
      .onError((err) => {
        _removeBeforeUnload();
        if (fill) fill.classList.remove('animated');
        barWrap.classList.add('danger');
        showToast(`上传失败：${err.message}`, 'error');
        activeUploader = null;
      });

    activeUploader.start();
  }

  // File input change
  fileInput.addEventListener('change', () => {
    if (fileInput.files.length > 0) _handleFile(fileInput.files[0]);
  });

  // Drag-and-drop
  zone.addEventListener('dragover', (e) => {
    e.preventDefault();
    zone.classList.add('dragover');
  });
  zone.addEventListener('dragleave', () => zone.classList.remove('dragover'));
  zone.addEventListener('drop', (e) => {
    e.preventDefault();
    zone.classList.remove('dragover');
    const file = e.dataTransfer.files[0];
    if (file) _handleFile(file);
  });

  // Click to open file dialog (if input not overlapping zone)
  zone.addEventListener('click', (e) => {
    if (e.target === zone || e.target.closest('.upload-zone-icon') ||
        e.target.closest('.upload-zone-title') || e.target.closest('.upload-zone-hint')) {
      fileInput.click();
    }
  });

  return {
    abort() { if (activeUploader) activeUploader.abort(); },
  };
}


/* ── Internal Utilities ─────────────────────────────────────
   Small private helpers used above.
   ─────────────────────────────────────────────────────────── */

function _escapeHTML(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function _sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}


/* ── DOMContentLoaded – Global wiring ───────────────────────
   Auto-initialise elements declared in HTML via data attributes.
   ─────────────────────────────────────────────────────────── */

document.addEventListener('DOMContentLoaded', () => {
  // ── Copy buttons  [data-copy="text to copy"]
  document.querySelectorAll('[data-copy]').forEach(btn => {
    btn.addEventListener('click', () => copyToClipboard(btn.dataset.copy, btn));
  });

  // ── Confirm buttons  [data-confirm="message"] [data-confirm-title] [data-confirm-style]
  document.querySelectorAll('[data-confirm]').forEach(el => {
    el.addEventListener('click', (e) => {
      e.preventDefault();
      const message = el.dataset.confirm;
      const title   = el.dataset.confirmTitle   || undefined;
      const style   = el.dataset.confirmStyle   || 'danger';
      const text    = el.dataset.confirmText    || undefined;

      confirmAction(message, () => {
        if (el.tagName === 'A' && el.href) {
          window.location.href = el.href;
        } else if (el.closest('form') && el.type === 'submit') {
          el.removeEventListener('click', arguments.callee);
          el.click();
        } else if (el.dataset.action) {
          eval(el.dataset.action); // Use with caution; prefer onConfirm wiring
        }
      }, { title, confirmText: text, confirmStyle: style });
    });
  });

  // ── Verify URL buttons  [data-verify-request-id="id"] [data-result-el="id"]
  document.querySelectorAll('[data-verify-request-id]').forEach(btn => {
    btn.addEventListener('click', () => {
      const requestId = btn.dataset.verifyRequestId;
      const resultEl  = btn.dataset.resultEl
        ? document.getElementById(btn.dataset.resultEl)
        : btn.parentElement.querySelector('.verify-result');
      verifyURL(requestId, btn, resultEl);
    });
  });

  // ── Toast from URL fragment  #toast=message&toast-type=success
  // (Useful after redirects)
  try {
    const params = new URLSearchParams(window.location.search);
    const msg    = params.get('_toast');
    const type   = params.get('_toast_type') || 'info';
    if (msg) {
      showToast(decodeURIComponent(msg), type);
      // Clean the URL without reloading
      const url = new URL(window.location.href);
      url.searchParams.delete('_toast');
      url.searchParams.delete('_toast_type');
      history.replaceState(null, '', url.toString());
    }
  } catch (_) {}
});


/* ── Public API ─────────────────────────────────────────────
   Expose functions on window for inline HTML usage.
   ─────────────────────────────────────────────────────────── */

window.SMB = {
  // Upload
  ChunkedUploader,
  MultiFileUploader,
  initUploadUI,

  // UI helpers
  showToast,
  copyToClipboard,
  confirmAction,
  verifyURL,

  // API
  apiCall,

  // Formatters
  formatSize,
  formatDuration,
};
