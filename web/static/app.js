'use strict';

const TOOLS = {
  images: {
    title: 'Combine images into a PDF',
    lead: 'One page per image, in the order of the list.',
    endpoint: '/api/images-to-pdf',
    accept: 'image/png,image/jpeg,image/webp,image/tiff,.tif,.tiff',
    dropTitle: 'Drop images here or click to browse',
    dropHint: 'PNG, JPEG, WebP or TIFF',
    action: 'Create PDF',
    min: 1,
    multiple: true,
    output: () => 'images.pdf',
  },
  merge: {
    title: 'Merge PDFs into one',
    lead: 'Drag the files to set the page order.',
    endpoint: '/api/merge',
    accept: 'application/pdf,.pdf',
    dropTitle: 'Drop PDF files here or click to browse',
    dropHint: 'At least two files',
    action: 'Merge PDFs',
    min: 2,
    multiple: true,
    output: () => 'merged.pdf',
  },
  watermark: {
    title: 'Add a watermark to a PDF',
    lead: 'Stamp text or an image over the pages of a document.',
    endpoint: '/api/watermark',
    accept: 'application/pdf,.pdf',
    dropTitle: 'Drop a PDF here or click to browse',
    dropHint: 'One file',
    action: 'Apply watermark',
    min: 1,
    multiple: false,
    output: (files) => files[0].file.name.replace(/\.pdf$/i, '') + '-watermarked.pdf',
  },
};

const $ = (id) => document.getElementById(id);
const form = $('form');
const picker = $('picker');
const drop = $('drop');
const list = $('files');
const submit = $('submit');
const errorBox = $('error');
const result = $('result');
const sheet = $('sheet');

const lists = { images: [], merge: [], watermark: [] };
let tool = 'images';
let limits = { maxUploadBytes: Infinity, maxFiles: Infinity };
let busy = false;
let resultUrl = null;
let stampUrl = null;

const current = () => lists[tool];

function formatSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1 << 20) return Math.round(bytes / 1024) + ' KB';
  return Number((bytes / (1 << 20)).toFixed(1)) + ' MB';
}

function showError(message) {
  errorBox.textContent = message;
  errorBox.hidden = !message;
}

function clearResult() {
  if (resultUrl) URL.revokeObjectURL(resultUrl);
  resultUrl = null;
  result.hidden = true;
}

function selectTool(name) {
  tool = name;
  const t = TOOLS[name];
  $('title').textContent = t.title;
  $('lead').textContent = t.lead;
  $('drop-title').textContent = t.dropTitle;
  $('drop-hint').textContent = t.dropHint;
  picker.accept = t.accept;
  picker.multiple = t.multiple;
  document.querySelectorAll('.tabs button').forEach((b) => {
    b.setAttribute('aria-selected', String(b.dataset.tool === name));
  });
  document.querySelectorAll('.options').forEach((o) => {
    o.hidden = o.dataset.for !== name;
  });
  $('preview').hidden = name !== 'watermark';
  showError('');
  clearResult();
  render();
  renderPreview();
}

function matchesTool(file) {
  if (tool === 'images') {
    return /^image\/(png|jpeg|webp|tiff)$/.test(file.type) || /\.(png|jpe?g|webp|tiff?)$/i.test(file.name);
  }
  return file.type === 'application/pdf' || /\.pdf$/i.test(file.name);
}

function addFiles(incoming) {
  const accepted = [...incoming].filter(matchesTool);
  const skipped = incoming.length - accepted.length;
  showError(skipped ? `${skipped} file(s) skipped: wrong type.` : '');
  clearResult();

  if (!TOOLS[tool].multiple) {
    current().forEach(release);
    lists[tool] = accepted.slice(0, 1).map(wrap);
  } else {
    const room = limits.maxFiles - current().length;
    if (accepted.length > room) showError(`The limit is ${limits.maxFiles} files per job.`);
    current().push(...accepted.slice(0, Math.max(room, 0)).map(wrap));
  }
  render();
}

function wrap(file) {
  const isImage = file.type.startsWith('image/') && file.type !== 'image/tiff';
  return { file, thumb: isImage ? URL.createObjectURL(file) : null };
}

function release(entry) {
  if (entry.thumb) URL.revokeObjectURL(entry.thumb);
}

function move(from, to) {
  const items = current();
  if (to < 0 || to >= items.length) return;
  items.splice(to, 0, items.splice(from, 1)[0]);
  clearResult();
  render();
}

function iconButton(label, text, onClick, disabled) {
  const b = document.createElement('button');
  b.type = 'button';
  b.textContent = text;
  b.title = label;
  b.setAttribute('aria-label', label);
  b.disabled = disabled;
  b.addEventListener('click', onClick);
  return b;
}

function renderRow(entry, i, items) {
  const li = document.createElement('li');
  const ordered = items.length > 1;

  if (ordered) {
    const index = document.createElement('span');
    index.className = 'index';
    index.textContent = i + 1;
    li.append(index);
  }

  if (entry.thumb) {
    const img = document.createElement('img');
    img.className = 'thumb';
    img.src = entry.thumb;
    img.alt = '';
    li.append(img);
  } else {
    const badge = document.createElement('span');
    badge.className = 'thumb badge';
    badge.textContent = /\.pdf$/i.test(entry.file.name) ? 'PDF' : 'IMG';
    li.append(badge);
  }

  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = entry.file.name;
  name.title = entry.file.name;

  const size = document.createElement('span');
  size.className = 'size';
  size.textContent = formatSize(entry.file.size);
  li.append(name, size);

  if (ordered) {
    li.append(
      iconButton(`Move ${entry.file.name} up`, '↑', () => move(i, i - 1), i === 0),
      iconButton(`Move ${entry.file.name} down`, '↓', () => move(i, i + 1), i === items.length - 1),
    );
    li.draggable = true;
    li.addEventListener('dragstart', (e) => {
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('application/x-folio-row', String(i));
      li.classList.add('dragging');
    });
    li.addEventListener('dragend', () => li.classList.remove('dragging'));
    li.addEventListener('dragover', (e) => {
      if (!e.dataTransfer.types.includes('application/x-folio-row')) return;
      e.preventDefault();
      li.classList.add('target');
    });
    li.addEventListener('dragleave', () => li.classList.remove('target'));
    li.addEventListener('drop', (e) => {
      const from = e.dataTransfer.getData('application/x-folio-row');
      if (from === '') return;
      e.preventDefault();
      e.stopPropagation();
      move(Number(from), i);
    });
  }

  li.append(iconButton(`Remove ${entry.file.name}`, '×', () => {
    release(items[i]);
    items.splice(i, 1);
    clearResult();
    render();
  }, false));
  return li;
}

function render() {
  const items = current();
  list.replaceChildren(...items.map(renderRow));
  updateSubmit();
}

function watermarkReady() {
  if (tool !== 'watermark') return true;
  const kind = form.elements.wm_kind.value;
  return kind === 'text' ? $('wm-text').value.trim() !== '' : $('wm-image').files.length === 1;
}

function updateSubmit() {
  const t = TOOLS[tool];
  submit.textContent = busy ? 'Processing...' : t.action;
  submit.disabled = busy || current().length < t.min || !watermarkReady();
}

function payload() {
  const data = new FormData();
  current().forEach((e) => data.append('files', e.file));
  const scope = form.querySelector(`.options[data-for="${tool}"]`);
  if (!scope) return data;

  scope.querySelectorAll('[name]').forEach((el) => {
    if (el.name !== 'wm_kind' && !el.closest('[hidden]')) data.append(el.name, el.value);
  });
  if (tool === 'watermark' && form.elements.wm_kind.value === 'image') {
    data.append('image', $('wm-image').files[0]);
  }
  return data;
}

async function run(event) {
  event.preventDefault();
  if (submit.disabled) return;

  const total = current().reduce((sum, e) => sum + e.file.size, 0);
  if (total > limits.maxUploadBytes) {
    showError(`The upload is limited to ${formatSize(limits.maxUploadBytes)}.`);
    return;
  }

  busy = true;
  showError('');
  clearResult();
  updateSubmit();
  try {
    const response = await fetch(TOOLS[tool].endpoint, { method: 'POST', body: payload() });
    if (!response.ok) {
      const body = await response.json().catch(() => null);
      throw new Error(body?.error ?? `The server answered with status ${response.status}.`);
    }
    resultUrl = URL.createObjectURL(await response.blob());
    result.href = resultUrl;
    result.download = TOOLS[tool].output(current());
    result.hidden = false;
    result.click();
  } catch (err) {
    showError(err instanceof TypeError ? 'The server could not be reached.' : err.message);
  } finally {
    busy = false;
    updateSubmit();
  }
}

// Mirrors tileOffsets in internal/pdfops/watermark.go, in fractions of the page width.
function tileOffsets(ratio, scale) {
  const stepX = scale * 1.2;
  const stepY = stepX * 0.7;
  const rows = Math.floor(ratio / 2 / stepY) + 1;
  const cols = Math.floor(0.5 / stepX) + 1;
  const out = [];
  for (let r = -rows; r <= rows; r++) {
    const shift = r % 2 !== 0 ? stepX / 2 : 0;
    for (let c = -cols; c <= cols; c++) out.push([c * stepX + shift, r * stepY]);
  }
  return out;
}

function makeStamp(kind, width) {
  if (kind === 'image') {
    const img = document.createElement('img');
    img.src = stampUrl;
    img.alt = '';
    img.style.width = width + 'px';
    return img;
  }
  const span = document.createElement('span');
  span.textContent = $('wm-text').value;
  span.style.color = $('wm-color').value;
  return span;
}

function renderPreview() {
  $('wm-opacity-out').textContent = Math.round($('wm-opacity').value * 100) + '%';
  $('wm-rotation-out').textContent = $('wm-rotation').value + '°';
  $('wm-size-out').textContent = Math.round($('wm-size').value * 100) + '%';
  if (tool !== 'watermark') return;

  const kind = form.elements.wm_kind.value;
  sheet.replaceChildren();
  if ((kind === 'text' && !$('wm-text').value.trim()) || (kind === 'image' && !stampUrl)) return;

  const pageWidth = sheet.clientWidth;
  const pageHeight = sheet.clientHeight;
  const scale = Number($('wm-size').value);
  const tiled = $('wm-layout').value === 'tiled';
  const offsets = tiled ? tileOffsets(pageHeight / pageWidth, scale) : [[0, 0]];

  for (const [x, y] of offsets) {
    const stamp = makeStamp(kind, scale * pageWidth);
    stamp.classList.add('stamp');
    stamp.style.left = pageWidth / 2 + x * pageWidth + 'px';
    stamp.style.top = pageHeight / 2 + y * pageWidth + 'px';
    stamp.style.opacity = $('wm-opacity').value;
    // PDF rotation is counterclockwise, CSS is clockwise.
    stamp.style.transform = `translate(-50%, -50%) rotate(${-$('wm-rotation').value}deg)`;
    sheet.append(stamp);
  }

  if (kind === 'text') {
    const first = sheet.firstElementChild;
    first.style.fontSize = '100px';
    const size = (100 * scale * pageWidth) / first.offsetWidth;
    sheet.querySelectorAll('.stamp').forEach((s) => { s.style.fontSize = size + 'px'; });
  }
}

function hasFiles(event) {
  return event.dataTransfer.types.includes('Files');
}

document.querySelectorAll('.tabs button').forEach((b) => {
  b.addEventListener('click', () => selectTool(b.dataset.tool));
});

picker.addEventListener('change', () => {
  addFiles([...picker.files]);
  picker.value = '';
});

drop.addEventListener('dragover', (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  drop.classList.add('over');
});
drop.addEventListener('dragleave', () => drop.classList.remove('over'));
drop.addEventListener('drop', (e) => {
  e.preventDefault();
  drop.classList.remove('over');
  addFiles([...e.dataTransfer.files]);
});

// A file dropped next to the zone would otherwise navigate away from the app.
window.addEventListener('dragover', (e) => { if (hasFiles(e)) e.preventDefault(); });
window.addEventListener('drop', (e) => { if (hasFiles(e)) e.preventDefault(); });

form.addEventListener('submit', run);

form.addEventListener('input', () => {
  updateSubmit();
  renderPreview();
});

form.querySelectorAll('input[name="wm_kind"]').forEach((radio) => {
  radio.addEventListener('change', () => {
    form.querySelectorAll('[data-kind]').forEach((el) => {
      el.hidden = el.dataset.kind !== radio.value;
    });
    updateSubmit();
    renderPreview();
  });
});

$('wm-image').addEventListener('change', () => {
  if (stampUrl) URL.revokeObjectURL(stampUrl);
  const file = $('wm-image').files[0];
  stampUrl = file ? URL.createObjectURL(file) : null;
  updateSubmit();
  renderPreview();
});

$('page-size').addEventListener('change', () => {
  const fit = $('page-size').value === 'fit';
  form.elements.orientation.disabled = fit;
  form.elements.margin.disabled = fit;
});

window.addEventListener('resize', renderPreview);

async function loadLimits() {
  try {
    const response = await fetch('/api/limits');
    limits = await response.json();
    $('limits').textContent = `Up to ${limits.maxFiles} files and ${formatSize(limits.maxUploadBytes)} per job`;
  } catch {
    $('limits').textContent = '';
  }
}

selectTool('images');
loadLimits();
