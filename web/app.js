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

function audioCtx() {
  if (!fx.ctx) {
    fx.ctx = new AudioContext({ latencyHint: 'interactive' });
    fx.gain = fx.ctx.createGain();
    fx.gain.gain.value = state.fxVol / 100;
    fx.gain.connect(fx.ctx.destination);
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

function renderTracks() {
  const list = $('#trackList');
  list.replaceChildren();
  $('#count').textContent = pad2(state.tracks.length);
  state.tracks.forEach((t, i) => {
    const row = document.createElement('div');
    row.className = 'track' + (i === selected ? ' selected' : '');
    row.innerHTML = `<span class="num">${pad2(i + 1)}</span><div class="track-info" tabindex="0" role="button"><strong></strong><small></small></div><span class="track-time">${fmt(t.duration)}</span><div class="row-actions"><button title="Mover para cima">↑</button><button title="Mover para baixo">↓</button><button title="Excluir faixa">×</button></div>`;
    row.querySelector('strong').textContent = t.name;
    row.querySelector('strong').title = t.name;
    row.querySelector('small').textContent = t.cues.length === 1 ? '1 deixa' : `${t.cues.length} deixas`;
    const info = row.querySelector('.track-info');
    info.onclick = () => select(i);
    info.ondblclick = () => rename(t, 'música', renderTracks);
    info.onkeydown = e => { if (e.key === 'Enter') select(i); };
    const [up, down, del] = row.querySelectorAll('button');
    up.disabled = i === 0;
    down.disabled = i === state.tracks.length - 1;
    up.onclick = () => move(i, -1);
    down.onclick = () => move(i, 1);
    del.onclick = () => removeTrack(i);
    list.append(row);
  });
  if (!state.tracks.length) list.innerHTML = '<div class="empty">Seu espetáculo começa aqui.<br>Importe uma música para preparar a primeira cena.</div>';
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

function select(i) {
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
  stopTicker();
  updatePlay();
  if (state.auto && selected < state.tracks.length - 1) { select(selected + 1); toggle(); }
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
$('#next').onclick = () => select(Math.min(state.tracks.length - 1, selected + 1));

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
