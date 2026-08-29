#!/usr/bin/env node
// 模拟 Gomocup 协议引擎（TCP 服务端），用于没有真实引擎时联调 GUI。
// 支持全部必选命令（START/BEGIN/INFO/BOARD/TURN/END）与
// TAKEBACK/RESTART/ABOUT/PLAY/RECTSTART(ERROR)，并在 START 后注入一条 MESSAGE。
// 用法: node tools/mock-engine.js [--port 9527] [--dumb]
//   --dumb  随机邻域落子（便于在 GUI 测试里快速连五）
'use strict';
import net from 'node:net';

const args = process.argv.slice(2);
const opt = (name, dflt) => { const i = args.indexOf(name); return i >= 0 ? args[i + 1] : dflt; };
const PORT = Number(opt('--port', process.env.MOCK_PORT || 9527));
const DUMB = args.includes('--dumb');

let size = 20;
let board = new Int8Array(size * size);
let boardCollecting = null; // BOARD 多行数据收集：[] 表示正在收集

const reset = (n) => { if (n) size = n; board = new Int8Array(size * size); };
const at = (x, y) => (x < 0 || y < 0 || x >= size || y >= size) ? 3 : board[y * size + x];
const put = (x, y, v) => { if (x >= 0 && y >= 0 && x < size && y < size) board[y * size + x] = v; };

function countLine(x, y, dx, dy, v) {
  let c = 0;
  for (let i = 1; i <= 5; i++) { if (at(x + dx * i, y + dy * i) === v) c++; else break; }
  return c;
}
function makesFive(x, y, v) {
  for (const [dx, dy] of [[1, 0], [0, 1], [1, 1], [1, -1]]) {
    if (1 + countLine(x, y, dx, dy, v) + countLine(x, y, -dx, -dy, v) >= 5) return true;
  }
  return false;
}
function lineScore(x, y, dx, dy, v) {
  let c = 1, block = 0;
  for (const s of [1, -1]) {
    let i = 1;
    while (at(x + dx * i * s, y + dy * i * s) === v) { c++; i++; }
    if (at(x + dx * i * s, y + dy * i * s) !== 0) block++;
  }
  if (c >= 5) return 100000;
  if (c === 4) return block === 0 ? 10000 : block === 1 ? 1200 : 0;
  if (c === 3) return block === 0 ? 1000 : block === 1 ? 90 : 0;
  if (c === 2) return block === 0 ? 90 : block === 1 ? 9 : 0;
  return block === 0 ? 1 : 0;
}
function score(x, y, me, opp) {
  let s = 0;
  for (const [dx, dy] of [[1, 0], [0, 1], [1, 1], [1, -1]]) {
    s += lineScore(x, y, dx, dy, me) + 0.8 * lineScore(x, y, dx, dy, opp);
  }
  return s;
}
function candidates() {
  const list = [];
  let any = false;
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (at(x, y) !== 0) continue;
      let near = false;
      for (let dy = -2; dy <= 2 && !near; dy++) {
        for (let dx = -2; dx <= 2; dx++) {
          if ((dx || dy) && at(x + dx, y + dy) !== 0 && at(x + dx, y + dy) !== 3) { near = true; break; }
        }
      }
      if (near) { list.push([x, y]); any = true; }
    }
  }
  if (!any) list.push([Math.floor(size / 2), Math.floor(size / 2)]);
  return list;
}
function chooseMove(me, opp) {
  const cells = candidates();
  for (const [x, y] of cells) if (makesFive(x, y, me)) return [x, y];
  if (!DUMB) for (const [x, y] of cells) if (makesFive(x, y, opp)) return [x, y];
  if (DUMB) return cells[Math.floor(Math.random() * cells.length)];
  let best = cells[0], bestScore = -Infinity;
  for (const [x, y] of cells) {
    const s = score(x, y, me, opp);
    if (s > bestScore) { bestScore = s; best = [x, y]; }
  }
  return best;
}
function parsePair(s) {
  const m = /^\s*(-?\d+)\s*,\s*(-?\d+)\s*$/.exec(s || '');
  return m ? [+m[1], +m[2]] : [null, null];
}

function send(sock, line) { sock.write(line + '\r\n'); }

function handle(line, sock) {
  if (boardCollecting) {
    if (line.toUpperCase() === 'DONE') {
      const cells = boardCollecting;
      boardCollecting = null;
      reset();
      let mine = 0, theirs = 0;
      for (const [x, y, f] of cells) {
        put(x, y, f === 1 ? 1 : f === 2 ? 2 : 3);
        if (f === 1) mine++;
        else if (f === 2) theirs++;
      }
      if (mine <= theirs) {
        const [x, y] = chooseMove(1, 2);
        put(x, y, 1);
        send(sock, `${x},${y}`);
      } else {
        send(sock, 'OK');
      }
      return;
    }
    const m = /^\s*(-?\d+)\s*,\s*(-?\d+)\s*,\s*([123])\s*$/.exec(line);
    if (m) boardCollecting.push([+m[1], +m[2], +m[3]]);
    return;
  }
  const sp = line.indexOf(' ');
  const cmd = (sp < 0 ? line : line.slice(0, sp)).toUpperCase();
  const arg = sp < 0 ? '' : line.slice(sp + 1).trim();
  switch (cmd) {
    case 'START': {
      const n = parseInt(arg, 10);
      if (!Number.isInteger(n) || n < 5 || n > 26) { send(sock, `ERROR unsupported board size ${arg}`); break; }
      reset(n);
      send(sock, 'MESSAGE MockBrain ready');
      send(sock, 'OK');
      break;
    }
    case 'RECTSTART': send(sock, 'ERROR rectangular board not supported'); break;
    case 'RESTART': reset(); send(sock, 'OK'); break;
    case 'INFO': break;
    case 'ABOUT': send(sock, 'name="MockBrain", version="1.0", author="zcode", country="CN"'); break;
    case 'BOARD': boardCollecting = []; break;
    case 'BEGIN': {
      const [x, y] = chooseMove(1, 2);
      put(x, y, 1);
      send(sock, `${x},${y}`);
      break;
    }
    case 'TURN': {
      const [ox, oy] = parsePair(arg);
      if (ox === null) { send(sock, 'ERROR bad move'); break; }
      put(ox, oy, 2);
      const [x, y] = chooseMove(1, 2);
      put(x, y, 1);
      send(sock, `${x},${y}`);
      break;
    }
    case 'TAKEBACK': {
      const [x, y] = parsePair(arg);
      if (x === null) { send(sock, 'ERROR bad takeback'); break; }
      if (at(x, y) === 0 || at(x, y) === 3) { send(sock, 'ERROR empty cell'); break; }
      put(x, y, 0);
      send(sock, 'OK');
      break;
    }
    case 'PLAY': {
      const [x, y] = parsePair(arg);
      if (x === null) { send(sock, 'ERROR bad play'); break; }
      put(x, y, 1);
      send(sock, `${x},${y}`);
      break;
    }
    case 'END': sock.end(); break;
    default: send(sock, `UNKNOWN command: ${cmd}`);
  }
}

net.createServer((sock) => {
  let buf = '';
  sock.on('data', (d) => {
    buf += d.toString();
    let i;
    while ((i = buf.indexOf('\n')) >= 0) {
      let line = buf.slice(0, i);
      buf = buf.slice(i + 1);
      line = line.replace(/\r$/, '').trim();
      if (line) {
        try { handle(line, sock); } catch { /* 单条命令异常不终止会话 */ }
      }
    }
  });
  sock.on('error', () => {});
}).listen(PORT, '127.0.0.1', () => {
  console.log(`mock-engine: 监听 127.0.0.1:${PORT}${DUMB ? '（dumb 模式）' : ''}`);
});
