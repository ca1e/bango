// 应用装配：对局状态机、UI 绑定、管理器流程（开始/落子/悔棋/重开）。
import { EngineLink, EngineManager, EngineError } from './protocol.js';
import { Game, EMPTY, BLACK, WHITE, FOUL_NAME } from './rules.js';
import { BoardView } from './board.js';

const $ = (id) => document.getElementById(id);
const els = {
  stateDot: $('stateDot'), stateText: $('stateText'), engineName: $('engineName'),
  turnStone: $('turnStone'), turnText: $('turnText'), sideText: $('sideText'), thinkingTag: $('thinkingTag'),
  startBtn: $('startBtn'), stopBtn: $('stopBtn'), undoBtn: $('undoBtn'),
  segFirst: $('segFirst'), segRule: $('segRule'), segSize: $('segSize'),
  engineAddr: $('engineAddr'), settingsHint: $('settingsHint'),
  moveList: $('moveList'), log: $('log'), logCount: $('logCount'),
  overlay: $('overlay'), resultIcon: $('resultIcon'), resultTitle: $('resultTitle'), resultReason: $('resultReason'),
  againBtn: $('againBtn'), viewBtn: $('viewBtn'), toasts: $('toasts'),
};

const SETTINGS_KEY = 'gomoku-gui-settings-v2';
const settings = Object.assign(
  { first: 'player', rule: 'free', size: 15, addr: '127.0.0.1:9527' },
  (() => { try { return JSON.parse(localStorage.getItem(SETTINGS_KEY) || '{}'); } catch { return {}; } })(),
);
const saveSettings = () => { try { localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings)); } catch { /* 忽略 */ } };

const link = new EngineLink();
const mgr = new EngineManager(link);

let game = null;
let ui = 'idle'; // idle | connecting | humanTurn | aiThinking | ended
let epoch = 0;   // 作代计数：异步流程回来后核对，避免重开后旧应答落地
let humanColor = BLACK;
let engineName = '';
let turnStartAt = 0;       // 本轮思考起点（玩家落子耗时 / AI 应答耗时）
let stoppedByUser = false; // 区分「已停止」与「已中断」

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const fmtDur = (ms) => {
  if (ms == null || Number.isNaN(ms)) return '';
  if (ms < 1000) return `${Math.max(1, Math.round(ms))}ms`;
  if (ms < 10000) return `${(ms / 1000).toFixed(2)}s`;
  return `${(ms / 1000).toFixed(1)}s`;
};
const infoLines = (renju) => [
  'INFO timeout_turn 10000',
  'INFO timeout_match 0',
  'INFO max_memory 0',
  'INFO game_type 0',
  `INFO rule ${renju ? 4 : 0}`,
];

function parseAddr(s) {
  const m = /^([a-zA-Z0-9.\-]+):(\d{1,5})$/.exec((s || '').trim());
  if (!m) throw new Error('引擎地址格式应为 host:port');
  const port = +m[2];
  if (port < 1 || port > 65535) throw new Error('端口不合法');
  return { host: m[1], port };
}

// ---------- 协议日志 ----------
function pushLog(kind, line) {
  const span = document.createElement('span');
  span.className = kind;
  const mark = kind === 'out' ? '→ ' : kind === 'in' ? '← ' : kind === 'msg' ? '« ' : '! ';
  span.textContent = mark + line + '\n';
  const atBottom = els.log.scrollTop + els.log.clientHeight >= els.log.scrollHeight - 12;
  els.log.appendChild(span);
  while (els.log.childNodes.length > 600) els.log.removeChild(els.log.firstChild);
  if (atBottom) els.log.scrollTop = els.log.scrollHeight;
  els.logCount.textContent = String(els.log.childNodes.length);
}
link.onSend((l) => pushLog('out', l));
link.onLine((l) => pushLog('in', l));
link.onStatus((s) => {
  const map = { connected: ['ok', '已连接'], disconnected: ['off', '未连接'], error: ['err', '连接错误'] };
  const [cls, text] = map[s.state] || map.disconnected;
  els.stateDot.className = 'dot ' + cls;
  els.stateText.textContent = s.state === 'error' ? `连接错误：${s.detail}` : text;
});
link.startEvents();

// ---------- 棋盘 ----------
let forbiddenPoints = [];
let forbiddenSet = new Set();
function refreshForbidden() {
  const n = game ? game.size : settings.size;
  forbiddenPoints = game && game.renju && !game.winner && game.current === BLACK
    ? game.allForbiddenPoints() : [];
  forbiddenSet = new Set(forbiddenPoints.map((p) => p.y * n + p.x));
  return forbiddenPoints;
}

const boardView = new BoardView($('board'), onCellClick);
function refreshBoard(animate = false) {
  const forbidden = refreshForbidden();
  const moveNumbers = new Map();
  if (game) game.moves.forEach((m, i) => moveNumbers.set(m.y * game.size + m.x, i + 1));
  boardView.setState({
    size: game ? game.size : settings.size,
    getStone: (x, y) => (game ? game.at(x, y) : EMPTY),
    lastMove: game ? game.lastMove() : null,
    forbidden,
    winLine: game ? game.winLine : null,
    interactive: ui === 'humanTurn',
    isForbiddenCell: (c) => forbiddenSet.has(c.y * (game ? game.size : settings.size) + c.x),
    currentColor: game ? game.current : BLACK,
    moveNumbers,
    animate,
  });
  renderSide();
}

// ---------- UI 渲染 ----------
function setUI(next) { ui = next; render(); }

function render() {
  els.startBtn.disabled = ui === 'connecting';
  els.startBtn.textContent = ui === 'idle' ? '开始对局' : '重新开始';
  els.stopBtn.hidden = !(ui === 'connecting' || ui === 'humanTurn' || ui === 'aiThinking');
  const canUndo = !!game && (ui === 'humanTurn' || ui === 'ended') && game.countBy(humanColor) > 0;
  els.undoBtn.disabled = !canUndo;

  const lock = ui === 'connecting' || ui === 'humanTurn' || ui === 'aiThinking';
  for (const seg of [els.segFirst, els.segRule, els.segSize]) {
    for (const b of seg.querySelectorAll('button')) b.disabled = lock;
  }
  els.engineAddr.disabled = lock;
  els.settingsHint.hidden = !lock;
  renderSide();
  renderMoves();
}

function renderSide() {
  const stoneCls = game && game.current === WHITE ? 'mini-stone white' : 'mini-stone black';
  els.turnStone.className = game ? stoneCls : 'mini-stone';
  els.thinkingTag.hidden = ui !== 'aiThinking';
  if (!game) {
    els.turnText.textContent = '准备开始';
    els.sideText.textContent = '';
  } else if (ui === 'ended') {
    els.turnText.textContent = game.winner === 'draw' ? '和棋' : (game.winner === humanColor ? '你获胜' : 'AI 获胜');
  } else if (ui === 'aiThinking') {
    els.turnText.textContent = 'AI 落子';
  } else if (ui === 'humanTurn') {
    els.turnText.textContent = '轮到你落子';
  } else if (ui === 'connecting') {
    els.turnText.textContent = '连接引擎…';
  } else {
    els.turnText.textContent = stoppedByUser ? '已停止' : '已中断';
  }
  if (game) {
    const side = humanColor === BLACK ? '你执黑（先手）' : '你执白';
    const rule = game.renju ? '有禁手 · 连珠' : '无禁手';
    els.sideText.textContent = `${side} · ${rule} · ${game.size}路 · 第 ${game.moves.length} 手`;
  }
}

function renderMoves() {
  els.moveList.innerHTML = '';
  if (!game) return;
  game.moves.forEach((m, i) => {
    const li = document.createElement('li');
    const mine = m.player === humanColor;
    li.innerHTML =
      `<span class="no">${i + 1}</span>` +
      `<span class="st ${m.player === BLACK ? 'b' : 'w'}"></span>` +
      `<span class="who">${mine ? '你' : 'AI'}</span>` +
      `<span class="xy">${m.x},${m.y}</span>` +
      `<span class="ms">${m.ms != null ? fmtDur(m.ms) : ''}</span>`;
    els.moveList.appendChild(li);
  });
  els.moveList.scrollTop = els.moveList.scrollHeight;
}

function toast(msg, kind = 'info') {
  const t = document.createElement('div');
  t.className = 'toast ' + kind;
  t.textContent = msg;
  els.toasts.appendChild(t);
  requestAnimationFrame(() => t.classList.add('show'));
  setTimeout(() => { t.classList.remove('show'); setTimeout(() => t.remove(), 350); }, 3800);
}

// ---------- 对局流程 ----------
async function startGame() {
  epoch++;
  const e = epoch;
  stoppedByUser = false;
  ui = 'connecting';
  render();
  const renju = settings.rule === 'renju';
  try {
    // 干净会话：旧连接先 END（协议：引擎尽快退出/复位）再断开重连
    if (link.status.state === 'connected') {
      try { await link.send('END'); } catch { /* 忽略 */ }
      await sleep(300);
    }
    await link.disconnect();
    const { host, port } = parseAddr(settings.addr);
    await link.connect(host, port);
    if (e !== epoch) return;

    engineName = '';
    try {
      engineName = EngineManager.parseAbout(await mgr.about()).name || '';
    } catch { /* ABOUT 失败不阻塞开局 */ }
    els.engineName.textContent = engineName;

    let usedSize = settings.size;
    try {
      await mgr.start(usedSize);
    } catch (err) {
      if (usedSize !== 20) {
        toast(`引擎不接受 ${usedSize} 路（${err.message}），已改用 20 路`, 'warn');
        settings.size = 20;
        saveSettings();
        syncSegs();
        usedSize = 20;
        await mgr.start(usedSize);
      } else {
        throw err;
      }
    }
    if (e !== epoch) return;
    await mgr.info(infoLines(renju));

    game = new Game(usedSize, renju);
    humanColor = settings.first === 'player' ? BLACK : WHITE;
    refreshBoard();

    if (settings.first === 'ai') {
      ui = 'aiThinking';
      render();
      turnStartAt = performance.now(); // AI 首手思考计时
      const m = await mgr.begin();
      if (e !== epoch) return;
      applyEngineMove(m, e);
    } else {
      ui = 'humanTurn';
      render();
      refreshBoard();
      turnStartAt = performance.now(); // 玩家首手思考计时
    }
  } catch (err) {
    if (e !== epoch) return;
    toast(err.message, 'err');
    ui = 'idle';
    render();
    refreshBoard();
  }
}

function applyEngineMove(m, e) {
  if (e !== epoch) return;
  if (!m) { failGame('引擎应答格式错误'); return; }
  if (!game.inB(m.x, m.y) || game.at(m.x, m.y) !== EMPTY) {
    failGame(`引擎返回非法坐标 (${m.x},${m.y})`);
    return;
  }
  const res = game.place(m.x, m.y, true); // 引擎自知的棋，sent = true
  game.lastMove().ms = Math.round(performance.now() - turnStartAt); // AI 应答耗时
  if (res.over) { onGameOver(res); return; }
  ui = 'humanTurn';
  render();
  refreshBoard(true);
  turnStartAt = performance.now(); // 玩家思考计时开始
}

function failGame(msg) {
  toast(msg, 'err');
  ui = 'idle';
  render();
  refreshBoard();
}

function onCellClick(x, y) {
  if (ui !== 'humanTurn' || !game) return;
  if (game.at(x, y) !== EMPTY) return; // 已占点：忽略（与悬停 not-allowed 光标一致）
  if (game.renju && game.current === BLACK) {
    const foul = game.forbiddenAt(x, y);
    if (foul) {
      toast(`此处为禁手点：${FOUL_NAME[foul]}`, 'warn');
      return;
    }
  }
  const res = game.place(x, y, false); // 制胜手不再发给引擎
  game.lastMove().ms = Math.round(performance.now() - turnStartAt); // 玩家落子耗时
  if (res.over) { onGameOver(res); return; }
  ui = 'aiThinking';
  render();
  refreshBoard(true);
  turnStartAt = performance.now(); // AI 思考计时开始
  const e = epoch;
  mgr.turn(x, y)
    .then((m) => { if (e !== epoch) return; applyEngineMove(m, e); })
    .catch((err) => { if (e !== epoch) return; failGame(err.message); });
}

function onGameOver(res) {
  ui = 'ended';
  render();
  refreshBoard();
  const title = res.winner === 'draw' ? '和棋'
    : res.winner === BLACK ? '黑方获胜' : '白方获胜';
  const icon = res.winner === 'draw' ? '🤝'
    : res.winner === humanColor ? '🏆' : '🤖';
  const tail = res.winner === 'draw' ? ''
    : res.winner === humanColor ? ' · 恭喜！' : ' · 再接再厉';
  els.resultIcon.textContent = icon;
  els.resultTitle.textContent = title;
  els.resultReason.textContent = res.reason + tail;
  els.overlay.hidden = false;
}

// 悔棋：撤回到玩家上一次决策点。末手是玩家（制胜手未发引擎）撤 3 手，否则撤 2 手。
async function undo() {
  if (!game || game.countBy(humanColor) === 0) return;
  const last = game.lastMove();
  const n = last && last.player === humanColor ? 3 : 2;
  if (game.moves.length < n) return;
  epoch++;
  const e = epoch;
  const removed = game.moves.splice(game.moves.length - n, n);
  rebuildBoard();
  ui = 'connecting';
  render();
  try {
    for (const m of removed.slice().reverse()) {
      if (!m.sent) continue; // 玩家制胜手从未告知引擎
      const r = await mgr.takeback(m.x, m.y);
      if (r === 'unknown') { await resyncBoard(e); break; }
      if (r.startsWith('error:')) throw new Error(r.slice(6));
    }
  } catch (err) {
    if (e === epoch) toast('悔棋同步失败：' + err.message, 'err');
  }
  if (e !== epoch) return;
  ui = 'humanTurn';
  render();
  refreshBoard();
  turnStartAt = performance.now(); // 悔棋后重新计时
}

// 停止对局：END → 断开 → 回到空闲态（设置解锁，棋盘保留复盘）
async function stopGame() {
  epoch++;
  const e = epoch;
  stoppedByUser = true;
  if (link.status.state === 'connected') {
    try { await link.send('END'); } catch { /* 忽略 */ }
    await sleep(250);
  }
  await link.disconnect();
  if (e !== epoch) return;
  ui = 'idle';
  render();
  refreshBoard();
}

function rebuildBoard() {
  game.board.fill(EMPTY);
  for (const m of game.moves) game.board[m.y * game.size + m.x] = m.player;
  game.winner = null;
  game.winReason = '';
  game.winLine = null;
  refreshBoard();
}

// TAKEBACK 未实现（UNKNOWN）时的回退：END → 重连 → START → INFO → BOARD 重放。
async function resyncBoard(e) {
  if (link.status.state === 'connected') {
    try { await link.send('END'); } catch { /* 忽略 */ }
    await sleep(300);
  }
  await link.disconnect();
  const { host, port } = parseAddr(settings.addr);
  await link.connect(host, port);
  if (e !== epoch) return;
  await mgr.start(game.size);
  await mgr.info(infoLines(game.renju));
  const aiColor = humanColor === BLACK ? WHITE : BLACK;
  const stones = game.moves.map((m) => ({ x: m.x, y: m.y, field: m.player === aiColor ? 1 : 2 }));
  const r = await mgr.board(stones);
  if (r.coords) await mgr.takeback(r.coords.x, r.coords.y); // 计数型引擎多应的一手退掉
}

// ---------- 控件绑定 ----------
function syncSegs() {
  for (const [el, val] of [
    [els.segFirst, settings.first],
    [els.segRule, settings.rule],
    [els.segSize, String(settings.size)],
  ]) {
    for (const b of el.querySelectorAll('button')) b.classList.toggle('on', b.dataset.v === val);
  }
}
function bindSeg(el, apply) {
  el.addEventListener('click', (ev) => {
    const b = ev.target.closest('button');
    if (!b || b.disabled) return;
    apply(b.dataset.v);
    saveSettings();
    syncSegs();
    if (ui === 'idle') refreshBoard();
  });
}
bindSeg(els.segFirst, (v) => { settings.first = v; });
bindSeg(els.segRule, (v) => { settings.rule = v; });
bindSeg(els.segSize, (v) => { settings.size = +v; });
els.engineAddr.addEventListener('change', () => {
  settings.addr = els.engineAddr.value.trim() || '127.0.0.1:9527';
  els.engineAddr.value = settings.addr;
  saveSettings();
});

els.startBtn.addEventListener('click', startGame);
els.stopBtn.addEventListener('click', stopGame);
els.undoBtn.addEventListener('click', undo);
els.againBtn.addEventListener('click', () => { els.overlay.hidden = true; startGame(); });
els.viewBtn.addEventListener('click', () => { els.overlay.hidden = true; });

// ---------- 启动 ----------
syncSegs();
els.engineAddr.value = settings.addr;
refreshBoard();
render();
