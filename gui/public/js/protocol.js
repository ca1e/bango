// 传输层 + 协议管理器。
// 浏览器侧完整实现 Gomocup 协议的 Manager 逻辑（protol.md）：
//   - 一行一命令；发送统一 CRLF；接收行由桥接服务切好后经 SSE 推来；
//   - MESSAGE/DEBUG 剥离；未知行不得导致退出；
//   - 先挂应答监听、再发命令，避免应答早于监听到达而丢失。
// 与协议规范的唯一差别：stdin/stdout 换成本地桥接（SSE + POST）转发的 TCP。

export class EngineLink {
  constructor() {
    this.lineCbs = new Set();
    this.statusCbs = new Set();
    this.sendCbs = new Set();
    this.es = null;
    this.status = { state: 'disconnected', detail: '' };
  }
  onLine(cb) { this.lineCbs.add(cb); return () => this.lineCbs.delete(cb); }
  onStatus(cb) { this.statusCbs.add(cb); return () => this.statusCbs.delete(cb); }
  onSend(cb) { this.sendCbs.add(cb); return () => this.sendCbs.delete(cb); }

  startEvents() {
    if (this.es) return;
    this.es = new EventSource('/api/events');
    this.es.addEventListener('line', (e) => {
      const { line } = JSON.parse(e.data);
      for (const cb of this.lineCbs) cb(line);
    });
    this.es.addEventListener('status', (e) => {
      this.status = JSON.parse(e.data);
      for (const cb of this.statusCbs) cb(this.status);
    });
    this.es.onerror = () => { /* EventSource 自动重连 */ };
  }

  async connect(host, port) {
    const r = await fetch('/api/connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ host, port }),
    });
    const j = await r.json();
    if (!j.ok) throw new Error(j.error || '连接失败');
  }

  async send(lines) {
    const arr = Array.isArray(lines) ? lines : [lines];
    for (const l of arr) for (const cb of this.sendCbs) cb(l);
    const r = await fetch('/api/send', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ lines: arr }),
    });
    const j = await r.json();
    if (!j.ok) throw new Error(j.error || '发送失败');
  }

  async disconnect() {
    try { await fetch('/api/disconnect', { method: 'POST' }); } catch { /* 忽略 */ }
  }
}

const RE_COORD = /^\s*(-?\d+)\s*,\s*(-?\d+)\s*$/;
export const parseCoords = (line) => {
  const m = RE_COORD.exec(line);
  return m ? { x: +m[1], y: +m[2] } : null;
};
const isOK = (line) => /^\s*OK\s*$/i.test(line);
const isError = (line) => /^\s*ERROR\b/i.test(line);
const isUnknown = (line) => /^\s*UNKNOWN\b/i.test(line);
const errMsg = (line) => line.replace(/^\s*ERROR\s*/i, '').trim() || '引擎返回 ERROR';

export class EngineError extends Error {}
export const CTRL_TIMEOUT = 10000;   // 控制命令应答超时
export const THINK_TIMEOUT = 90000;  // 落子应答超时（引擎思考）

export class EngineManager {
  constructor(link) { this.link = link; }

  // 等待一行匹配 expect 的应答；MESSAGE/DEBUG 剥离进 messages；其余意外行按协议忽略。
  expect(expectFn, timeoutMs, what) {
    let cleanup = null;
    const promise = new Promise((resolve, reject) => {
      const messages = [];
      const offLine = this.link.onLine((line) => {
        if (/^\s*(MESSAGE|DEBUG)\b/i.test(line)) {
          messages.push(line.replace(/^\s*\w+\s+/, ''));
          return;
        }
        if (expectFn(line)) { cleanup(); resolve({ reply: line, messages }); }
        // 非期待应答：协议要求不得因此退出，记录（应用层日志）后继续等待
      });
      const offStatus = this.link.onStatus((s) => {
        if (s.state !== 'connected') { cleanup(); reject(new Error('引擎连接已断开')); }
      });
      const timer = setTimeout(() => {
        cleanup();
        reject(new Error(`等待${what}超时（${Math.round(timeoutMs / 1000)}s）`));
      }, timeoutMs);
      cleanup = () => { clearTimeout(timer); offLine(); offStatus(); };
    });
    return { promise, cancel: () => cleanup && cleanup() };
  }

  async cmd(lines, expectFn, timeoutMs, what) {
    const w = this.expect(expectFn, timeoutMs, what);
    try {
      await this.link.send(lines);
    } catch (e) {
      w.cancel();
      throw e;
    }
    return w.promise;
  }

  async about() {
    const { reply } = await this.cmd(['ABOUT'], () => true, CTRL_TIMEOUT, 'ABOUT 应答');
    return reply;
  }

  static parseAbout(line) {
    const out = {};
    const re = /([A-Za-z_]\w*)\s*=\s*"([^"]*)"/g;
    let m;
    while ((m = re.exec(line))) out[m[1].toLowerCase()] = m[2];
    return out;
  }

  async start(size) {
    const { reply } = await this.cmd(
      [`START ${size}`],
      (l) => isOK(l) || isError(l),
      CTRL_TIMEOUT, 'START 应答');
    if (isError(reply)) throw new EngineError(errMsg(reply));
  }

  async info(lines) { await this.link.send(lines); }

  async begin() {
    const { reply } = await this.cmd(
      ['INFO time_left 2147483647', 'BEGIN'],
      (l) => parseCoords(l) || isError(l),
      THINK_TIMEOUT, 'AI 首手应答');
    if (isError(reply)) throw new EngineError(errMsg(reply));
    return parseCoords(reply);
  }

  async turn(x, y) {
    const { reply } = await this.cmd(
      ['INFO time_left 2147483647', `TURN ${x},${y}`],
      (l) => parseCoords(l) || isError(l),
      THINK_TIMEOUT, 'AI 应答');
    if (isError(reply)) throw new EngineError(errMsg(reply));
    return parseCoords(reply);
  }

  // 返回 'ok' | 'unknown' | 'error:<消息>'
  async takeback(x, y) {
    const { reply } = await this.cmd(
      [`TAKEBACK ${x},${y}`],
      (l) => isOK(l) || isUnknown(l) || isError(l),
      CTRL_TIMEOUT, 'TAKEBACK 应答');
    if (isOK(reply)) return 'ok';
    if (isUnknown(reply)) return 'unknown';
    return `error:${errMsg(reply)}`;
  }

  // BOARD 重放（含 time_left 前缀与 DONE 结束）。
  // 协议：轮到引擎则回着法，否则回 OK。计数型引擎轮次口径不同时可能回着法，
  // 调用方对 coords 需要再 TAKEBACK 修正。返回 { ok:true } 或 { coords }。
  async board(stones) {
    const lines = ['INFO time_left 2147483647', 'BOARD'];
    for (const s of stones) lines.push(`${s.x},${s.y},${s.field}`);
    lines.push('DONE');
    const { reply } = await this.cmd(
      lines,
      (l) => isOK(l) || parseCoords(l) || isError(l),
      THINK_TIMEOUT, 'BOARD 应答');
    if (isError(reply)) throw new EngineError(errMsg(reply));
    const c = parseCoords(reply);
    return c ? { coords: c } : { ok: true };
  }
}
