'use strict';
// Deixa — mesa de áudio para espetáculos (versão desktop 2.1).
// O projeto e os áudios ficam no disco, numa pasta gerida pelo aplicativo
// (servidor local embutido no .exe). A música toca em streaming por um
// elemento <audio>; os efeitos ficam pré-carregados na memória (Web Audio)
// para disparar na hora, sem atraso e sem travar a interface.

const $ = s => document.querySelector(s);
const pad2 = n => String(n).padStart(2, '0');
const fmt = n => `${pad2(Math.floor((n || 0) / 60))}:${pad2(Math.floor((n || 0) % 60))}`;
const uid = () => crypto.randomUUID();
const audioURL = file => '/audio/' + encodeURIComponent(file);
const LONG_EFFECT = 90; // efeitos mais longos que isso tocam em streaming

let state = { format: 'deixa-desktop-1', name: 'Meu espetáculo', tracks: [], effects: [], auto: false, musicVol: 80, fxVol: 80 };
let queued = null; // música escolhida enquanto outra toca (entra na sequência)
let selected = 0, lastTime = 0, ticker = 0, seeking = false, toastTimer, saveTimer, dirty = false;
const fired = new Set();
const active = new Map(); // efeitos tocando: id -> { stop(), el? }
const music = new Audio();
music.preload = 'auto';

// ------------------------------------------------------------------ servidor

async function api(method, path, body) {
  const res = await fetch(path, { method, body, headers: { 'X-Deixa': '1' } });
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).error || msg; } catch { }
    throw new Error(msg);
  }
  return res.status === 204 ? null : res.json();
}

function setSaved(text) { $('#saved').textContent = text; }

function save(now = false) {
  dirty = true;
  setSaved('● Salvando…');
  clearTimeout(saveTimer);
  saveTimer = setTimeout(flush, now ? 0 : 400);
}

async function flush() {
  if (!dirty) return;
  dirty = false;
  try {
    await api('PUT', '/api/project', JSON.stringify(state));
    if (!dirty) setSaved('● Salvo neste computador');
  } catch (e) {
    setSaved('Falha ao salvar');
    toast('Não foi possível salvar: ' + e.message);
  }
}

// garante o último salvamento ao fechar a janela
addEventListener('pagehide', () => {
  if (!dirty) return;
  fetch('/api/project', { method: 'PUT', keepalive: true, headers: { 'X-Deixa': '1' }, body: JSON.stringify(state) });
});

function toast(s, ms = 4000) {
  const t = $('#toast');
  t.textContent = s;
  t.style.display = 'block';
  clearTimeout(toastTimer);
  if (ms) toastTimer = setTimeout(() => t.style.display = 'none', ms);
}

// ------------------------------------------------------------------ efeitos (Web Audio)

const fx = { ctx: null, gain: null, buffers: new Map(), loading: new Map() };

// ------------------------------------------------------------------ equalizador geral
// Tudo (música, efeitos curtos e longos) passa por eq.input -> filtros -> saída.
const EQ_BANDS = [
  { f: 60, type: 'lowshelf', label: 'Grave' },
  { f: 250, type: 'peaking', label: 'Médio-grave' },
  { f: 1000, type: 'peaking', label: 'Médio' },
  { f: 4000, type: 'peaking', label: 'Médio-agudo' },
  { f: 12000, type: 'highshelf', label: 'Agudo' },
];
const eq = { input: null, filters: [] };

function eqState() {
  if (!state.eq || !Array.isArray(state.eq.gains)) state.eq = { on: true, gains: [] };
  while (state.eq.gains.length < EQ_BANDS.length) state.eq.gains.push(0);
  return state.eq;
}

function applyEq(smooth = true) {
  if (!fx.ctx) return;
  const s = eqState();
  eq.filters.forEach((f, i) => {
    const g = s.on ? s.gains[i] : 0;
    if (smooth) f.gain.setTargetAtTime(g, fx.ctx.currentTime, 0.03);
    else f.gain.value = g;
  });
}

// liga um elemento <audio> ao equalizador (só uma vez por elemento)
function routeElement(el) {
  if (!fx.ctx || el._routed) return;
  try { fx.ctx.createMediaElementSource(el).connect(eq.input); el._routed = true; } catch { }
}

function audioCtx() {
  if (!fx.ctx) {
    fx.ctx = new AudioContext({ latencyHint: 'interactive' });
    fx.gain = fx.ctx.createGain();
    fx.gain.gain.value = state.fxVol / 100;
    eq.input = fx.ctx.createGain();
    let node = eq.input;
    for (const b of EQ_BANDS) {
      const f = fx.ctx.createBiquadFilter();
      f.type = b.type;
      f.frequency.value = b.f;
      if (b.type === 'peaking') f.Q.value = 1;
      node.connect(f);
      node = f;
      eq.filters.push(f);
    }
    node.connect(fx.ctx.destination);
    fx.gain.connect(eq.input);
    applyEq(false);
    routeElement(music);
  }
  if (fx.ctx.state === 'suspended') fx.ctx.resume().catch(() => { });
  return fx.ctx;
}

function loadBuffer(e) {
  if (fx.buffers.has(e.file)) return Promise.resolve(fx.buffers.get(e.file));
  if (e.duration > LONG_EFFECT) return Promise.resolve(null);
  if (!fx.loading.has(e.file)) {
    const p = fetch(audioURL(e.file))
      .then(r => r.arrayBuffer())
      .then(ab => audioCtx().decodeAudioData(ab))
      .then(b => { fx.buffers.set(e.file, b); return b; })
      .catch(() => null)
      .finally(() => fx.loading.delete(e.file));
    fx.loading.set(e.file, p);
  }
  return fx.loading.get(e.file);
}

// pré-carrega um efeito por vez, em segundo plano
async function preloadEffects() {
  for (const e of [...state.effects]) await loadBuffer(e);
}

async function trigger(id, scheduled = false) {
  const e = state.effects.find(x => x.id === id);
  if (!e) return;
  const old = active.get(id);
  if (old) {
    old.stop();
    active.delete(id);
    if (!scheduled) return updatePads(); // segundo clique para o efeito
  }
  const ctx = audioCtx();
  const buffer = fx.buffers.get(e.file) || await loadBuffer(e);
  let handle;
  if (buffer) {
    const src = ctx.createBufferSource();
    src.buffer = buffer;
    src.connect(fx.gain);
    handle = { stop: () => { try { src.stop(); } catch { } } };
    src.onended = () => { if (active.get(id) === handle) { active.delete(id); updatePads(); } };
    src.start();
  } else {
    const el = new Audio(audioURL(e.file));
    el.volume = state.fxVol / 100;
    routeElement(el);
    handle = { el, stop: () => el.pause() };
    el.onended = () => { if (active.get(id) === handle) { active.delete(id); updatePads(); } };
    try { await el.play(); } catch { toast('Este efeito não pôde ser reproduzido.'); return updatePads(); }
  }
  active.set(id, handle);
  updatePads();
}

function stopEffects() {
  for (const h of active.values()) h.stop();
  active.clear();
  updatePads();
}

// ------------------------------------------------------------------ faixas

// músicas tocadas até o fim nesta apresentação (guardado neste computador)
let played = new Set();
try { played = new Set(JSON.parse(localStorage.getItem('deixa-tocadas') || '[]')); } catch { }
function savePlayed() { try { localStorage.setItem('deixa-tocadas', JSON.stringify([...played])); } catch { } }
function markPlayed(id) { if (!id) return; played.add(id); savePlayed(); renderTracks(); }
function updatePlayedInfo() {
  const n = state.tracks.filter(t => played.has(t.id)).length;
  $('#playedInfo').textContent = n ? `✓ ${n} de ${state.tracks.length} tocadas` : '';
  $('#clearPlayed').hidden = !n;
}

function renderTracks() {
  const list = $('#trackList'), top = list.scrollTop;
  list.replaceChildren();
  $('#count').textContent = pad2(state.tracks.length);
  state.tracks.forEach((t, i) => {
    const row = document.createElement('div');
    row.className = 'track' + (i === selected ? ' selected' : '') + (i === queued ? ' queued' : '') + (played.has(t.id) ? ' played' : '');
    row.innerHTML = `<span class="num">${pad2(i + 1)}</span><div class="track-info" tabindex="0" role="button"><strong></strong><small></small></div><span class="track-time">${fmt(t.duration)}</span><div class="row-actions"><button title="Mover para cima">↑</button><button title="Mover para baixo">↓</button><button title="Excluir faixa">×</button></div>`;
    row.querySelector('strong').textContent = t.name;
    row.querySelector('strong').title = t.name;
    if (played.has(t.id)) {
      const ok = document.createElement('span');
      ok.className = 'played-check';
      ok.textContent = '✓';
      ok.title = 'Tocada até o fim — clique para desmarcar';
      ok.onclick = ev => { ev.stopPropagation(); played.delete(t.id); savePlayed(); renderTracks(); };
      row.querySelector('.num').after(ok);
    }
    row.querySelector('small').textContent = (t.cues.length === 1 ? '1 deixa' : `${t.cues.length} deixas`) + (t.scene ? ' · ✎ ' + t.scene : '');
    const info = row.querySelector('.track-info');
    info.onclick = () => pick(i);
    info.ondblclick = () => rename(t, 'música', renderTracks);
    info.onkeydown = e => { if (e.key === 'Enter') pick(i); };
    const [up, down, del] = row.querySelectorAll('button');
    up.disabled = i === 0;
    down.disabled = i === state.tracks.length - 1;
    up.onclick = () => move(i, -1);
    down.onclick = () => move(i, 1);
    del.onclick = () => removeTrack(i);
    list.append(row);
  });
  if (!state.tracks.length) list.innerHTML = '<div class="empty">Seu espetáculo começa aqui.<br>Importe uma música para preparar a primeira cena.</div>';
  list.scrollTop = top;
  updatePlayedInfo();
  showNotes();
  const sel = list.querySelector('.track.selected'); // mantém a faixa atual visível, só dentro da lista
  if (sel) {
    const y = sel.offsetTop - list.offsetTop;
    if (y < list.scrollTop) list.scrollTop = y;
    else if (y + sel.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = y + sel.offsetHeight - list.clientHeight;
  }
}

function removeTrack(i) {
  if (!confirm(`Excluir "${state.tracks[i].name}" e suas deixas?`)) return;
  const current = state.tracks[selected]?.id;
  const wasCurrent = i === selected;
  state.tracks.splice(i, 1);
  if (wasCurrent) select(Math.min(i, state.tracks.length - 1));
  else { selected = state.tracks.findIndex(x => x.id === current); renderTracks(); }
  save();
}

function move(i, d) {
  const id = state.tracks[selected]?.id;
  [state.tracks[i], state.tracks[i + d]] = [state.tracks[i + d], state.tracks[i]];
  selected = state.tracks.findIndex(t => t.id === id);
  renderTracks();
  save();
}

function rename(item, kind, rerender) {
  const name = prompt(`Novo nome para ${kind === 'música' ? 'a música' : 'o efeito'}:`, item.name);
  if (name === null || !name.trim()) return;
  item.name = name.trim();
  rerender();
  if (kind === 'música' && state.tracks[selected] === item) $('#currentName').textContent = item.name;
  renderCues();
  save();
}

// Clique numa música: se outra estiver tocando, ela não é interrompida;
// a escolhida fica como PRÓXIMA (▶Ⅰ ou o fim da atual a colocam em cena).
function pick(i) {
  if (!music.paused && i === selected) { queued = null; renderTracks(); return; } // não reinicia a música em cena
  if (!music.paused) {
    queued = queued === i ? null : i;
    renderTracks();
    const t = state.tracks[i];
    if (queued != null && t?.scene) goToScene(t.scene);
    if (queued != null) toast('Fica como próxima. A música atual continua tocando.');
    return;
  }
  select(i);
}
function nextIndex() { return queued ?? Math.min(state.tracks.length - 1, selected + 1); }
function select(i) {
  queued = null;
  stopTicker();
  music.pause();
  music.removeAttribute('src');
  music.load();
  selected = Math.max(0, i);
  fired.clear();
  lastTime = 0;
  const t = state.tracks[selected];
  if (t) {
    t.cues.sort((a, b) => a.time - b.time);
    music.src = audioURL(t.file);
    $('#currentName').textContent = t.name;
    $('#currentLabel').textContent = `CENA ${pad2(selected + 1)} DE ${pad2(state.tracks.length)}`;
    $('#duration').textContent = fmt(t.duration);
  } else {
    $('#currentName').textContent = 'Nenhuma música';
    $('#currentLabel').textContent = 'IMPORTE SUA PRIMEIRA FAIXA';
    $('#duration').textContent = '00:00';
  }
  $('#seek').value = 0;
  $('#elapsed').textContent = '00:00';
  renderTracks();
  renderCues();
  updatePlay();
  if (t?.scene) goToScene(t.scene);
}

// ------------------------------------------------------------------ reprodução

function updatePlay() {
  const playing = !music.paused;
  $('.player').classList.toggle('playing', playing);
  $('#play').textContent = playing ? 'Ⅱ' : '▶';
  $('#playState').textContent = playing ? 'MÚSICA EM CENA' : music.currentTime > 0 ? 'PAUSADO · AGUARDANDO SUA DEIXA' : 'PRONTO PARA ENTRAR EM CENA';
}

async function toggle() {
  if (!state.tracks.length) return toast('Importe uma música primeiro.');
  audioCtx(); // libera o áudio dos efeitos no primeiro gesto
  if (music.paused) {
    if (music.ended) { fired.clear(); music.currentTime = 0; lastTime = 0; renderCues(); }
    try { await music.play(); } catch { toast('Não foi possível tocar este áudio. Tente importá-lo novamente.'); }
  } else music.pause();
}

function stopMusic() {
  music.pause();
  if (music.getAttribute('src')) music.currentTime = 0;
  lastTime = 0;
  fired.clear();
  renderCues();
  updatePlay();
}

function stopAll() {
  stopMusic();
  stopEffects();
  toast('Todos os sons foram parados.');
}

// Verifica as deixas com frequência maior que o "timeupdate" (~4x/s) para
// disparar efeitos e pausas com precisão de poucos centésimos de segundo.
function cueTick() {
  const now = music.currentTime;
  if (!seeking) {
    $('#elapsed').textContent = fmt(now);
    $('#seek').value = music.duration ? now / music.duration * 1000 : 0;
  }
  if (music.paused) return;
  const cues = state.tracks[selected]?.cues || [];
  let changed = false;
  for (const c of cues) {
    if (c.time > now) break;
    if (fired.has(c.id) || c.time < lastTime - 0.15) continue;
    fired.add(c.id);
    changed = true;
    if (c.action === 'pause') {
      music.pause();
      music.currentTime = c.time;
      toast(`Deixa de pausa em ${fmt(c.time)}`);
      break;
    }
    trigger(c.effectId, true);
  }
  lastTime = music.paused ? music.currentTime : now;
  if (changed) renderCues();
}

function startTicker() { if (!ticker) ticker = setInterval(cueTick, 30); }
function stopTicker() { clearInterval(ticker); ticker = 0; }

music.ontimeupdate = cueTick;
music.onplay = () => { startTicker(); updatePlay(); };
music.onpause = () => { stopTicker(); cueTick(); updatePlay(); };
music.onended = () => {
  markPlayed(state.tracks[selected]?.id);
  stopTicker();
  updatePlay();
  if (queued != null || (state.auto && selected < state.tracks.length - 1)) { const go = state.auto; select(nextIndex()); if (go) toggle(); }
};
music.onerror = () => { if (music.getAttribute('src')) toast('Áudio indisponível ou formato não suportado.'); };

// ------------------------------------------------------------------ pads de efeito

const glyphs = ['✧', '≋', 'ϟ', '✳'];

function renderPads() {
  const p = $('#pads');
  p.replaceChildren();
  $('#fxCount').textContent = pad2(state.effects.length);
  state.effects.forEach((e, i) => {
    const b = document.createElement('button');
    b.className = 'pad';
    b.dataset.id = e.id;
    b.innerHTML = `<span style="font-size:19px">${glyphs[i % 4]}</span><span class="key">${i < 9 ? i + 1 : 'FX'}</span><strong></strong><small></small><span class="pad-remove" role="button" tabindex="0" aria-label="Excluir efeito">×</span>`;
    b.querySelector('strong').textContent = e.name;
    b.title = 'Clique para tocar · duplo clique no nome para renomear';
    b.onclick = () => trigger(e.id);
    b.querySelector('strong').ondblclick = ev => { ev.stopPropagation(); rename(e, 'efeito', renderPads); };
    const remove = b.querySelector('.pad-remove');
    remove.onclick = ev => {
      ev.stopPropagation();
      if (!confirm(`Excluir o efeito "${e.name}" e as deixas que o utilizam?`)) return;
      active.get(e.id)?.stop();
      active.delete(e.id);
      state.effects = state.effects.filter(x => x.id !== e.id);
      for (const t of state.tracks) t.cues = t.cues.filter(c => c.effectId !== e.id);
      renderPads(); renderCues(); renderTracks();
      save();
    };
    remove.onkeydown = ev => { if (ev.key === 'Enter') { ev.preventDefault(); remove.click(); } };
    p.append(b);
  });
  if (!state.effects.length) p.innerHTML = '<p class="empty">Importe efeitos para criar seus pads.</p>';
  updatePads();
}

// atualiza só o estado visual dos pads (sem recriar os botões)
function updatePads() {
  for (const b of document.querySelectorAll('.pad')) {
    const e = state.effects.find(x => x.id === b.dataset.id);
    if (!e) continue;
    const on = active.has(e.id);
    b.classList.toggle('active', on);
    b.querySelector('small').textContent = on ? 'TOCANDO · CLIQUE PARA PARAR' : fmt(e.duration);
  }
}

// ------------------------------------------------------------------ deixas

function renderCues() {
  const p = $('#cueList');
  p.replaceChildren();
  const t = state.tracks[selected];
  if (!t?.cues.length) {
    p.innerHTML = '<div class="empty">Cada detalhe tem sua hora.<br>Adicione um efeito ou uma pausa no tempo da música.</div>';
    return;
  }
  for (const c of t.cues) {
    const row = document.createElement('div');
    row.className = 'cue-row';
    row.innerHTML = `<time>${fmt(c.time)}</time><span></span><button title="Excluir deixa">×</button>`;
    row.querySelector('span').textContent = (fired.has(c.id) ? '✓ ' : '') +
      (c.action === 'pause' ? 'Ⅱ Pausar música' : '✧ ' + (state.effects.find(x => x.id === c.effectId)?.name || 'Efeito indisponível'));
    row.querySelector('button').onclick = () => {
      t.cues = t.cues.filter(x => x.id !== c.id);
      renderCues(); renderTracks();
      save();
    };
    p.append(row);
  }
}

// ------------------------------------------------------------------ importação
//
// Qualquer arquivo pode ser importado: o servidor identifica o formato pelos
// bytes. MP3, WAV, OGG, Opus, FLAC, AAC/M4A e WebM entram como estão; os demais
// (WMA, AIFF, ALAC, AC3, AMR, áudio de vídeos…) são convertidos para WAV.
// Se o player não conseguir abrir um arquivo "nativo", ele é convertido também.

// arquivos claramente sem áudio são ignorados sem incomodar
const NOT_AUDIO = /\.(jpe?g|png|gif|bmp|webp|svg|ico|heic|pdf|docx?|xlsx?|pptx?|odt|txt|rtf|zip|rar|7z|exe|msi|lnk|ini|json|html?|js|css|psd|ai|ttf|otf|woff2?)$/i;

function probeDuration(url) {
  return new Promise((resolve, reject) => {
    const a = new Audio();
    let timer;
    const done = () => { clearTimeout(timer); a.removeAttribute('src'); a.load(); };
    a.preload = 'metadata';
    a.onloadedmetadata = () => {
      const n = a.duration;
      if (Number.isFinite(n) && n > 0) { done(); resolve(n); return; }
      // alguns arquivos (MP3 sem cabeçalho, WebM) só informam a duração no fim
      a.ondurationchange = () => { if (Number.isFinite(a.duration) && a.duration > 0) { const d = a.duration; done(); resolve(d); } };
      a.currentTime = 1e9;
    };
    a.onerror = () => { done(); reject(Error('o player não abre este formato')); };
    timer = setTimeout(() => { done(); reject(Error('tempo esgotado ao ler o áudio')); }, 20000);
    a.src = url;
  });
}

// Recebe o resultado do servidor ({file, converted}) e garante que o player toca.
async function finalizeImport(res) {
  try {
    return { file: res.file, duration: await probeDuration(audioURL(res.file)), converted: res.converted };
  } catch (first) {
    if (res.converted) {
      api('POST', '/api/discard', JSON.stringify({ file: res.file })).catch(() => { });
      throw first;
    }
  }
  // formato "nativo" que o player recusou (ex.: M4A com ALAC, OGG com Speex): converte
  let conv;
  try {
    conv = await api('POST', '/api/convert', JSON.stringify({ file: res.file }));
  } catch (e) {
    api('POST', '/api/discard', JSON.stringify({ file: res.file })).catch(() => { });
    throw e;
  }
  return { file: conv.file, duration: await probeDuration(audioURL(conv.file)), converted: true };
}

let importing = Promise.resolve();

// sources: lista de { name, upload() } — upload devolve a resposta do servidor
function importSources(sources, asEffects = false, selectNew = false) {
  if (!sources.length) return importing;
  importing = importing.then(async () => {
    let ok = 0, converted = 0, firstNew = -1;
    const failed = [];
    for (const [i, src] of sources.entries()) {
      toast(`Importando ${i + 1} de ${sources.length}: ${src.name}`, 0);
      try {
        const res = await finalizeImport(await src.upload());
        const item = { id: uid(), name: src.name.replace(/\.[^.]+$/, '') || src.name, file: res.file, duration: res.duration };
        if (asEffects) { state.effects.push(item); loadBuffer(item); }
        else { state.tracks.push({ ...item, cues: [] }); if (firstNew < 0) firstNew = state.tracks.length - 1; }
        ok++;
        if (res.converted) converted++;
        save(true); // grava a cada arquivo: nada se perde se o PC desligar
      } catch (e) {
        failed.push(`${src.name} (${e.message})`);
      }
    }
    if (!asEffects && firstNew >= 0 && (selectNew ? music.paused : !music.getAttribute('src'))) select(firstNew);
    renderTracks();
    renderPads();
    const what = asEffects ? (ok === 1 ? 'efeito importado' : 'efeitos importados') : (ok === 1 ? 'música importada' : 'músicas importadas');
    const conv = converted ? ` (${converted} ${converted === 1 ? 'convertida' : 'convertidas'} para WAV)` : '';
    toast(`${ok} ${what}${conv}.` + (failed.length ? ` Não foi possível importar: ${failed.join('; ')}` : ''), failed.length ? 12000 : 4000);
  });
  return importing;
}

function importFiles(files, asEffects = false) {
  const all = [...files];
  const list = all.filter(f => !NOT_AUDIO.test(f.name) || f.type.startsWith('audio/') || f.type.startsWith('video/'));
  if (!list.length) return toast(all.length ? 'Esses arquivos não parecem ser de áudio.' : 'Nenhum arquivo selecionado.');
  return importSources(list.map(f => ({
    name: f.name,
    upload: () => {
      const ext = (f.name.match(/\.([a-z0-9]+)$/i)?.[1] || '').toLowerCase();
      return api('POST', '/api/audio?ext=' + encodeURIComponent(ext) + '&name=' + encodeURIComponent(f.name), f);
    },
  })), asEffects);
}

// Arquivos abertos pelo Windows ("Abrir com", arrastar para o ícone do programa…)
let checkingPending = false;
async function checkPending() {
  if (checkingPending) return;
  checkingPending = true;
  try {
    const items = await api('GET', '/api/pending');
    if (items.length) {
      await importSources(items.map(it => ({
        name: it.name,
        upload: () => api('POST', '/api/import-path', JSON.stringify({ path: it.path })),
      })), false, true);
    }
  } catch { } finally { checkingPending = false; }
}
window.checkPending = checkPending;
setInterval(checkPending, 1500);
addEventListener('focus', checkPending);

$('#tracks').onchange = async e => { await importFiles(e.target.files); e.target.value = ''; };
$('#effectsInput').onchange = async e => { await importFiles(e.target.files, true); e.target.value = ''; };

// Arrastar e soltar: efeitos no painel de efeitos; em qualquer outro lugar,
// os arquivos entram como músicas na sequência.
addEventListener('dragover', e => { e.preventDefault(); $('.sequence').classList.add('drop'); });
addEventListener('dragleave', e => { if (!e.relatedTarget) $('.sequence').classList.remove('drop'); });
addEventListener('drop', e => {
  e.preventDefault();
  $('.sequence').classList.remove('drop');
  if (e.dataTransfer?.files?.length) importFiles(e.dataTransfer.files, false);
});
{
  const el = $('.effects');
  el.addEventListener('dragover', e => { e.preventDefault(); e.stopPropagation(); $('.sequence').classList.remove('drop'); el.classList.add('drop'); });
  el.addEventListener('dragleave', e => { if (!el.contains(e.relatedTarget)) el.classList.remove('drop'); });
  el.addEventListener('drop', e => { e.preventDefault(); e.stopPropagation(); el.classList.remove('drop'); importFiles(e.dataTransfer.files, true); });
}

// ------------------------------------------------------------------ controles

$('#play').onclick = toggle;
$('#stop').onclick = stopMusic;
$('#panic').onclick = stopAll;
$('#prev').onclick = () => select(Math.max(0, selected - 1));
$('#next').onclick = () => { const wasPlaying = !music.paused, q = queued != null; select(nextIndex()); if (wasPlaying && q) toggle(); };

const seek = $('#seek');
seek.addEventListener('pointerdown', () => { seeking = true; });
seek.oninput = e => {
  if (Number.isFinite(music.duration)) $('#elapsed').textContent = fmt(+e.target.value / 1000 * music.duration);
};
seek.onchange = e => {
  seeking = false;
  const t = state.tracks[selected];
  if (!t || !Number.isFinite(music.duration)) return;
  const target = +e.target.value / 1000 * music.duration;
  // para frente: deixas anteriores são ignoradas; para trás: ficam rearmadas
  for (const c of t.cues) c.time < target ? fired.add(c.id) : fired.delete(c.id);
  lastTime = target;
  music.currentTime = target;
  renderCues();
};

$('#musicVol').oninput = e => {
  state.musicVol = +e.target.value;
  music.volume = state.musicVol / 100;
  $('#volValue').textContent = state.musicVol + '%';
  save();
};
$('#fxVol').oninput = e => {
  state.fxVol = +e.target.value;
  if (fx.gain) fx.gain.gain.setTargetAtTime(state.fxVol / 100, fx.ctx.currentTime, 0.02);
  for (const h of active.values()) if (h.el) h.el.volume = state.fxVol / 100;
  save();
};
$('#clearPlayed').onclick = () => {
  if (!confirm('Desmarcar todas as músicas tocadas? (para começar uma nova apresentação)')) return;
  played.clear(); savePlayed(); renderTracks();
};
$('#auto').onchange = e => { state.auto = e.target.checked; save(); };
$('#showName').onblur = () => {
  state.name = $('#showName').textContent.trim() || 'Meu espetáculo';
  $('#showName').textContent = state.name;
  save();
};
$('#showName').onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); e.target.blur(); } };

$('#addCue').onclick = () => {
  const t = state.tracks[selected];
  if (!t) return toast('Importe uma música primeiro.');
  const sel = $('#cueEffect');
  sel.replaceChildren();
  for (const e of state.effects) {
    const o = document.createElement('option');
    o.value = e.id;
    o.textContent = e.name;
    sel.append(o);
  }
  $('#cueTime').value = fmt(Math.min(Math.floor(music.currentTime) + 5, Math.max(0, Math.floor(t.duration) - 1)));
  $('#effectSelectLabel').hidden = $('#cueAction').value === 'pause';
  $('#cueDialog').showModal();
};
$('#cueAction').onchange = () => $('#effectSelectLabel').hidden = $('#cueAction').value === 'pause';
$('#cancelCue').onclick = () => $('#cueDialog').close();
$('#cueForm').onsubmit = e => {
  e.preventDefault();
  const [m, s] = $('#cueTime').value.split(':').map(Number), time = m * 60 + s, t = state.tracks[selected];
  if (time >= t.duration) return toast('A deixa deve estar antes do fim da música.');
  const action = $('#cueAction').value;
  if (action === 'effect' && !$('#cueEffect').value) return toast('Importe um efeito primeiro.');
  const cue = { id: uid(), time, action, effectId: action === 'effect' ? $('#cueEffect').value : null };
  t.cues.push(cue);
  t.cues.sort((a, b) => a.time - b.time);
  if (time < music.currentTime) fired.add(cue.id); // já passou: não dispara agora
  renderCues(); renderTracks();
  save();
  $('#cueDialog').close();
};

function renderEq() {
  const s = eqState(), box = $('#eqBands');
  $('#eqOn').checked = s.on;
  box.classList.toggle('off', !s.on);
  box.innerHTML = '';
  EQ_BANDS.forEach((b, i) => {
    const d = document.createElement('div');
    d.className = 'eq-band';
    const hz = b.f >= 1000 ? b.f / 1000 + ' kHz' : b.f + ' Hz';
    d.innerHTML = `<output></output><input type="range" min="-12" max="12" step="0.5" aria-label="${b.label} (${hz})"><span>${hz}</span>`;
    const r = d.querySelector('input'), out = d.querySelector('output');
    const show = () => out.textContent = (s.gains[i] > 0 ? '+' : '') + s.gains[i] + ' dB';
    r.value = s.gains[i];
    show();
    r.oninput = () => { s.gains[i] = +r.value; show(); applyEq(); save(); };
    r.ondblclick = () => { s.gains[i] = 0; r.value = 0; show(); applyEq(); save(); };
    d.title = b.label + ' — duplo clique zera';
    box.append(d);
  });
}
$('#eqOpen').onclick = () => { renderEq(); $('#eqDialog').showModal(); };
$('#eqClose').onclick = () => $('#eqDialog').close();
$('#eqOn').onchange = e => { eqState().on = e.target.checked; $('#eqBands').classList.toggle('off', !e.target.checked); applyEq(); save(); };
$('#eqReset').onclick = () => { eqState().gains = EQ_BANDS.map(() => 0); renderEq(); applyEq(); save(); };
// ------------------------------------------------------------------ roteiro
// Um texto único do espetáculo; linhas que começam com "CENA" ou "#" dividem as cenas.
// Marca-texto: no texto fica ==trecho== (amarelo), ==r:trecho== (vermelho), ==b:trecho== (azul).
const MARK_RE = /==(?:([yrb]):)?(.+?)==/g;
function parseLine(line) {
  const prefix = (line.match(/^\s*#+\s*/) || [''])[0];
  const body = line.slice(prefix.length);
  let plain = '', colors = [], last = 0;
  for (const m of body.matchAll(MARK_RE)) {
    const before = body.slice(last, m.index);
    plain += before; colors.push(...Array(before.length).fill(null));
    plain += m[2]; colors.push(...Array(m[2].length).fill(m[1] || 'y'));
    last = m.index + m[0].length;
  }
  const rest = body.slice(last);
  plain += rest; colors.push(...Array(rest.length).fill(null));
  return { prefix, plain, colors };
}
function runs(plain, colors) {
  const out = [];
  for (let i = 0; i < plain.length;) {
    let j = i;
    while (j < plain.length && colors[j] === colors[i]) j++;
    out.push([plain.slice(i, j), colors[i]]);
    i = j;
  }
  return out;
}
function serializeLine(prefix, plain, colors) {
  return prefix + runs(plain, colors).map(([t, c]) => !c ? t : c === 'y' ? `==${t}==` : `==${c}:${t}==`).join('');
}
// aplica (ou limpa, color=null) a marca no trecho selecionado do roteiro
// Lê o roteiro direto da tela (vale para leitura e edição): linhas com cor por caractere.
function domToScript(view) {
  const lines = [];
  let cur = null;
  const newLine = prefix => { cur = { prefix: prefix || '', plain: '', colors: [] }; lines.push(cur); };
  const walk = (node, color) => {
    if (node.nodeType === 3) {
      if (!cur) newLine();
      const t = node.nodeValue.replace(/ /g, ' ');
      cur.plain += t; cur.colors.push(...Array(t.length).fill(color));
      return;
    }
    if (node.nodeType !== 1) return;
    if (node.tagName === 'BR') { if (!cur) newLine(); newLine(); return; }
    const block = /^(DIV|P|H\d)$/.test(node.tagName);
    if (block) newLine(node.dataset.prefix);
    const c = node.tagName === 'MARK' ? (node.className.match(/hl-([yrb])/) || [])[1] || 'y' : color;
    for (const ch of node.childNodes) walk(ch, c);
    if (block) cur = null;
  };
  for (const ch of view.childNodes) walk(ch, null);
  // linhas vazias (o espaço de preenchimento vira nada)
  for (const l of lines) if (l.plain === ' ' && l.colors[0] == null && !l.prefix) l.plain = '', l.colors = [];
  return lines;
}
const linesToText = lines => lines.map(l => serializeLine(l.prefix, l.plain, l.colors)).join('\n');

// aplica (ou limpa, color=null) a marca no trecho selecionado do roteiro
function markSelection(color) {
  const sel = getSelection(), view = $('#scriptView');
  if (!sel.rangeCount || sel.isCollapsed) return;
  const r = sel.getRangeAt(0);
  if (!view.contains(r.commonAncestorContainer)) return;
  // marcadores temporários no início e no fim da seleção
  const S = '', E = '';
  const end = r.cloneRange(); end.collapse(false); end.insertNode(document.createTextNode(E));
  const st = r.cloneRange(); st.collapse(true); st.insertNode(document.createTextNode(S));
  const lines = domToScript(view);
  let on = false;
  for (const l of lines) {
    let plain = '', colors = [];
    for (let i = 0; i < l.plain.length; i++) {
      const ch = l.plain[i];
      if (ch === S) { on = true; continue; }
      if (ch === E) { on = false; continue; }
      plain += ch; colors.push(on ? color : l.colors[i]);
    }
    l.plain = plain; l.colors = colors;
  }
  const top = view.scrollTop;
  state.script = linesToText(lines);
  sel.removeAllRanges();
  hideMarkBar();
  renderScript();
  view.scrollTop = top;
  save();
}
// transforma (ou desfaz) a(s) linha(s) selecionada(s) em título de cena
function toggleSceneSelection() {
  const sel = getSelection(), view = $('#scriptView');
  if (!sel.rangeCount) return;
  const r = sel.getRangeAt(0);
  if (!view.contains(r.commonAncestorContainer)) return;
  const S = '', E = '';
  const end = r.cloneRange(); end.collapse(false); end.insertNode(document.createTextNode(E));
  const st = r.cloneRange(); st.collapse(true); st.insertNode(document.createTextNode(S));
  const lines = domToScript(view);
  let on = false;
  const touched = [];
  for (const l of lines) {
    const hasS = l.plain.includes(S), hasE = l.plain.includes(E);
    if (hasS) on = true;
    if (on && l.plain.replace(/[]/g, '').trim()) touched.push(l);
    if (hasE) on = false;
    const keep = [...l.plain].map((c, i) => c !== S && c !== E);
    l.colors = l.colors.filter((_, i) => keep[i]);
    l.plain = l.plain.replace(/[]/g, '');
  }
  if (!touched.length) return;
  const makeScene = !touched[0].prefix;
  for (const l of touched) l.prefix = makeScene ? '# ' : '';
  const top = view.scrollTop;
  state.script = linesToText(lines);
  sel.removeAllRanges();
  hideMarkBar();
  renderScript();
  view.scrollTop = top;
  save();
  toast(makeScene ? 'Linha marcada como cena.' : 'A linha voltou a ser texto.');
}
$('#sceneBtn').onmousedown = e => e.preventDefault();
$('#sceneBtn').onclick = toggleSceneSelection;
function hideMarkBar() { $('#markBar').hidden = true; }
document.addEventListener('selectionchange', () => {
  const bar = $('#markBar'), sel = getSelection(), view = $('#scriptView');
  if (!sel.rangeCount || sel.isCollapsed || view.hidden || !view.contains(sel.anchorNode)) return hideMarkBar();
  const rect = sel.getRangeAt(0).getBoundingClientRect(), box = $('.script-main').getBoundingClientRect();
  bar.hidden = false;
  bar.style.top = Math.max(4, rect.top - box.top - bar.offsetHeight - 8) + 'px';
  bar.style.left = Math.min(box.width - bar.offsetWidth - 4, Math.max(4, rect.left - box.left + rect.width / 2 - bar.offsetWidth / 2)) + 'px';
});
for (const bt of document.querySelectorAll('#markBar button[data-c]')) {
  bt.onmousedown = e => e.preventDefault(); // não perde a seleção
  bt.onclick = () => markSelection(bt.dataset.c || null);
}
// "CENA 1", "Cena 2 – ...", "CENA III" ou qualquer linha curta começando com CENA (não um parágrafo)
function isSceneTitle(plain) {
  // aceita "Cena 1", "CENA III", "1ª CENA: ...", "5ª Cena – ...", "Ato 2", "1º ATO" (linhas curtas)
  const t = plain.trim().replace(/^[^\p{L}\p{N}]+/u, ''); // ignora emoji/símbolos no início
  if (t.length > 80) return false;
  return /^(cena|ato)\s*(\d+|[ivxlc]+\b)/i.test(t)
    || /^\d+\s*[ªºo°a]?\.?\s*(cena|ato)\b/i.test(t)
    || (/^(cena|ato)\b/i.test(t) && t.length <= 60);
}
const SCENE_RE = /^\s*(#+\s*|cena\b)/i;
function renderScript() {
  const view = $('#scriptView'), list = $('#sceneList');
  const fs = state.scriptFs || 18;
  view.style.fontSize = fs + 'px';
  view.innerHTML = ''; list.innerHTML = '';
  const text = state.script || '';
  if (!text.trim()) {
    if (!view.isContentEditable) view.innerHTML = '<p class="empty" contenteditable="false">Nenhum roteiro ainda. Clique em <b>✎ Editar</b> e cole ou escreva o texto.</p>';
  }
  let n = 0;
  text.split('\n').forEach((line, li) => {
    const { prefix, plain, colors } = parseLine(line);
    const scene = !!prefix || isSceneTitle(plain);
    const el = document.createElement(scene ? 'h3' : 'p');
    el.dataset.line = li;
    if (prefix) el.dataset.prefix = prefix;
    if (!plain) el.textContent = ' ';
    else for (const [t, c] of runs(plain, colors)) {
      if (!c) { el.append(t); continue; }
      const m = document.createElement('mark');
      m.className = 'hl-' + c;
      m.textContent = t;
      el.append(m);
    }
    view.append(el);
    if (scene) {
      const bt = document.createElement('button');
      bt.textContent = bt.title = plain.trim();
      if (colors.some(Boolean)) bt.classList.add('marked');
      bt.onclick = () => view.scrollTo({ top: el.offsetTop - 12, behavior: 'smooth' });
      bt._h = el;
      list.append(bt);
      n++;
    }
  });
  $('#sceneCount').textContent = pad2(n);
  list.hidden = !n;
  $('.script-body').style.gridTemplateColumns = n ? '' : '1fr';
  markScene();
}
function markScene() {
  const view = $('#scriptView'), btns = [...$('#sceneList').children];
  let cur = null;
  for (const b of btns) if (b._h.offsetTop - 40 <= view.scrollTop) cur = b;
  cur = cur || btns[0];
  btns.forEach(b => b.classList.toggle('current', b === cur));
  cur?.scrollIntoView({ block: 'nearest' });
}
$('#scriptView').addEventListener('scroll', markScene, { passive: true });

// ------------------------------------------------------------------ anotações por música
// Num musical, cada música é uma cena: as anotações ficam na própria música.
// A música pode ser ligada a um trecho do roteiro (📍); ao selecioná-la, o roteiro vai até lá.
let notesKey = null; // id da música cujas anotações estão na caixa
const notesTrack = () => state.tracks.find(t => t.id === notesKey);
function lineEls() { return [...$('#scriptView').querySelectorAll('[data-line]')]; }
function goToScene(text) {
  const el = lineEls().find(x => x.textContent.trim() === text);
  if (el) $('#scriptView').scrollTo({ top: el.offsetTop - 12, behavior: 'smooth' });
}
function growNotes() { const t = $('#notesText'); t.style.height = 'auto'; t.style.height = Math.min(t.scrollHeight + 2, 200) + 'px'; }
function showNotes() {
  const idx = queued ?? selected, t = state.tracks[idx];
  const box = $('#sceneNotes'), ta = $('#notesText');
  box.hidden = !t;
  if (!t) { notesKey = null; return; }
  $('#notesScene').textContent = `${pad2(idx + 1)} · ${t.name}` + (queued != null ? ' (próxima)' : '');
  if (notesKey !== t.id || document.activeElement !== ta) ta.value = t.notes || '';
  notesKey = t.id;
  box.classList.toggle('has', !!(t.notes || '').trim());
  $('#anchorText').textContent = t.scene ? '📍 ' + t.scene : 'Não ligada a um trecho do roteiro';
  $('#anchorText').classList.toggle('on', !!t.scene);
  $('#anchorClear').hidden = !t.scene;
  growNotes();
}
function renderSceneMusic() { showNotes(); }
$('#notesText').oninput = e => {
  const t = notesTrack();
  if (!t) return;
  if (e.target.value.trim()) t.notes = e.target.value; else delete t.notes;
  $('#sceneNotes').classList.toggle('has', !!e.target.value.trim());
  growNotes();
  save();
};
// liga a música ao trecho do roteiro que está no topo da tela agora
$('#anchorSet').onclick = () => {
  const t = notesTrack(), view = $('#scriptView');
  if (!t) return;
  const el = lineEls().find(x => x.offsetTop + x.offsetHeight > view.scrollTop + 4 && x.textContent.trim().length > 1);
  if (!el) return toast('Role o roteiro até o trecho desta música e clique de novo.');
  t.scene = el.textContent.trim().slice(0, 200);
  renderTracks(); save();
  toast('Música ligada a este trecho do roteiro.');
};
$('#anchorClear').onclick = () => { const t = notesTrack(); if (t) { delete t.scene; renderTracks(); save(); } };
$('#anchorText').onclick = () => { const t = notesTrack(); if (t?.scene) goToScene(t.scene); };
const sceneTitles = () => [...$('#sceneList').children].map(b => b.title);
// se o título de uma cena mudar na edição, a ligação da música acompanha (pela posição)
function carryNotes(before) {
  const after = sceneTitles();
  before.forEach((old, i) => {
    const neu = after[i];
    if (!neu || old === neu || after.includes(old)) return;
    for (const t of state.tracks) if (t.scene === old) t.scene = neu;
  });
}
// Edição direto no texto, com as marcas coloridas visíveis (sem códigos)
let editTimer;
function syncFromEditor() { state.script = linesToText(domToScript($('#scriptView'))); save(); }
function setEditing(on) {
  const view = $('#scriptView');
  const titlesBefore = sceneTitles();
  if (!on && view.isContentEditable) { clearTimeout(editTimer); syncFromEditor(); }
  view.contentEditable = on ? 'true' : 'false';
  view.classList.toggle('editing', on);
  $('#scriptEdit').textContent = on ? '✓ Concluir' : '✎ Editar';
  if (!on) { renderScript(); carryNotes(titlesBefore); renderTracks(); markScene(); save(); }
  else {
    if (!(state.script || '').trim()) { view.innerHTML = '<p data-line="0"> </p>'; }
    view.focus();
  }
}
$('#scriptEdit').onclick = () => setEditing(!$('#scriptView').isContentEditable);
$('#scriptView').addEventListener('input', () => {
  if (!$('#scriptView').isContentEditable) return;
  clearTimeout(editTimer);
  editTimer = setTimeout(syncFromEditor, 400);
});
// colar sempre como texto simples
$('#scriptView').addEventListener('paste', e => {
  if (!$('#scriptView').isContentEditable) return;
  e.preventDefault();
  document.execCommand('insertText', false, e.clipboardData.getData('text/plain'));
});
const setFs = d => { if ($('#scriptView').isContentEditable) syncFromEditor(); state.scriptFs = Math.min(40, Math.max(12, (state.scriptFs || 18) + d)); renderScript(); save(); };
$('#fsUp').onclick = () => setFs(2);
$('#fsDown').onclick = () => setFs(-2);
function sideMini(on) {
  document.body.classList.toggle('side-mini', on);
  $('#sideToggle').title = on ? 'Expandir o menu' : 'Encolher o menu';
  for (const b of document.querySelectorAll('.nav')) b.title = on ? b.querySelector('span')?.textContent || '' : '';
  try { localStorage.setItem('deixa-menu', on ? 'mini' : ''); } catch { }
}
$('#sideToggle').onclick = () => sideMini(!document.body.classList.contains('side-mini'));
try { if (localStorage.getItem('deixa-menu') === 'mini') sideMini(true); } catch { }

function showScript(on) {
  document.body.classList.toggle('with-script', on);
  $('#scriptNav').classList.toggle('active', on);
  try { localStorage.setItem('deixa-roteiro', on ? '1' : '0'); } catch { }
  if (on) markScene();
}
$('#scriptClose') && ($('#scriptClose').onclick = () => showScript(false));
$('#scriptNav').onclick = () => showScript(!document.body.classList.contains('with-script'));
try { if (localStorage.getItem('deixa-roteiro') === '1') showScript(true); } catch { }
$('#guide').onclick = () => $('#guideDialog').showModal();
$('#closeGuide').onclick = () => $('#guideDialog').close();
$('#desk').onclick = () => scrollTo({ top: 0, behavior: 'smooth' });
$('#folder').onclick = () => api('POST', '/api/open-folder').catch(() => { });

document.addEventListener('keydown', e => {
  if (/INPUT|TEXTAREA|SELECT/.test(e.target.tagName) || e.target.isContentEditable || document.querySelector('dialog[open]')) return;
  if (e.code === 'Space') { e.preventDefault(); toggle(); }
  else if (e.key === 'Escape') stopAll();
  else if (/^[1-9]$/.test(e.key) && !e.repeat && state.effects[+e.key - 1]) trigger(state.effects[+e.key - 1].id);
  else if (e.key === 'F5' || (e.ctrlKey && e.key.toLowerCase() === 'r')) e.preventDefault(); // evita recarregar no meio do show
});

// ------------------------------------------------------------------ backup

$('#backup').onclick = async () => {
  await flush();
  const a = document.createElement('a');
  a.href = '/api/export';
  a.download = '';
  a.click();
  toast('Gerando backup (.zip) com todos os áudios. Ele será salvo na pasta Downloads.', 7000);
};

$('#restore').onchange = async e => {
  const f = e.target.files[0];
  e.target.value = '';
  if (!f) return;
  if (!confirm(`Abrir "${f.name}" e substituir o espetáculo atual?`)) return;
  stopAll();
  toast('Abrindo backup… isso pode levar alguns segundos.', 0);
  try {
    await flush();
    state = await api('POST', '/api/restore', f);
    fx.buffers.clear();
    applyState();
    preloadEffects();
    toast('Espetáculo restaurado.');
  } catch (err) {
    toast('Não foi possível abrir o backup: ' + err.message, 9000);
  }
};

// ------------------------------------------------------------------ início

function applyState() {
  $('#showName').textContent = state.name;
  $('#auto').checked = state.auto;
  $('#musicVol').value = state.musicVol;
  $('#fxVol').value = state.fxVol;
  music.volume = state.musicVol / 100;
  if (fx.gain) fx.gain.gain.value = state.fxVol / 100;
  $('#volValue').textContent = state.musicVol + '%';
  applyEq();
  renderEq();
  queued = null;
  // anotações antigas (por título de cena) passam para a música ligada
  for (const t of state.tracks) if (!t.notes && t.scene && state.sceneNotes?.[t.scene]) t.notes = state.sceneNotes[t.scene];
  renderScript();
  if (document.body.classList.contains('with-script')) markScene();
  select(0);
  renderPads();
}

async function init() {
  for (let i = 0; i < 76; i++) {
    const bar = document.createElement('i');
    bar.style.height = (5 + Math.abs(Math.sin(i * .91) * Math.cos(i * .23)) * 29) + 'px';
    $('#wave').append(bar);
  }
  try {
    state = await api('GET', '/api/project');
    setSaved('● Salvo neste computador');
  } catch (e) {
    toast('Não foi possível carregar o projeto: ' + e.message, 0);
  }
  applyState();
  preloadEffects();
  checkPending();
}

init();
