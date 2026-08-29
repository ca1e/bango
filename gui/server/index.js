#!/usr/bin/env node
// 本地桥接服务：浏览器（管理器）⇄ TCP 引擎。
// 浏览器无法直接建立 TCP 连接，本服务只做字节搬运，不含任何协议语义：
//   GET  /                静态前端
//   GET  /api/events      SSE：推送引擎输出行（event: line）与连接状态（event: status）
//   POST /api/connect     建立 TCP 连接，body: {host, port}
//   POST /api/send        向引擎发送命令行，body: {lines: ["...", ...]}
//   POST /api/disconnect  断开 TCP
// 行切分遵循 protol.md：引擎侧 CR / LF / CRLF 均视为一行结束；空行不上抛。
'use strict';
import http from 'node:http';
import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PORT = Number(process.env.PORT || 9528);
const LISTEN_PORT = Number(process.env.BRIDGE_LISTEN_PORT || 0); // 可选：引擎主动拨入模式
const PUBLIC_DIR = path.join(__dirname, '..', 'public');
const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
};

let engine = null; // 当前引擎 socket
let rxBuffer = '';
let crHoldTimer = null;
const sseClients = new Set();

function broadcast(event, data) {
  const frame = `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
  for (const res of sseClients) {
    try { res.write(frame); } catch { /* 断开由 close 事件清理 */ }
  }
}

function setStatus(state, detail) {
  broadcast('status', { state, detail: detail || '' });
}

function feed(chunk) {
  rxBuffer += chunk;
  clearTimeout(crHoldTimer);
  extractLines(false);
  if (rxBuffer.endsWith('\r')) {
    // 行尾孤立 CR 可能是与下一包拼成的 CRLF，稍等再定
    crHoldTimer = setTimeout(() => extractLines(true), 20);
  }
}

function extractLines(forceCr) {
  for (;;) {
    const nl = rxBuffer.indexOf('\n');
    const cr = rxBuffer.indexOf('\r');
    let end = -1, skip = 0;
    if (cr !== -1 && (nl === -1 || cr < nl)) {
      if (cr === nl - 1) { end = cr; skip = 2; } // CRLF
      else if (cr === rxBuffer.length - 1 && !forceCr) return; // 等待更多数据
      else { end = cr; skip = 1; } // 孤立 CR
    } else if (nl !== -1) {
      end = nl; skip = 1;
    } else {
      return;
    }
    const line = rxBuffer.slice(0, end);
    rxBuffer = rxBuffer.slice(end + skip);
    if (line.length) broadcast('line', { line });
  }
}

function adoptSocket(sock) {
  if (engine) { try { engine.destroy(); } catch { /* 忽略 */ } }
  engine = sock;
  rxBuffer = '';
  clearTimeout(crHoldTimer);
  sock.setNoDelay(true);
  sock.on('data', feed);
  sock.on('error', (err) => setStatus('error', err.message));
  sock.on('close', () => {
    if (engine === sock) {
      engine = null;
      rxBuffer = '';
      setStatus('disconnected', '引擎连接已断开');
    }
  });
}

function connectEngine(host, port) {
  return new Promise((resolve) => {
    const sock = net.connect({ host, port });
    let done = false;
    const finish = (ok, error) => { if (!done) { done = true; resolve({ ok, error }); } };
    sock.setTimeout(4000, () => { sock.destroy(); finish(false, '连接超时'); });
    sock.once('connect', () => {
      sock.setTimeout(0);
      adoptSocket(sock);
      setStatus('connected', `${host}:${port}`);
      finish(true);
    });
    sock.once('error', (err) => finish(false, err.message));
  });
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    let size = 0;
    const chunks = [];
    req.on('data', (c) => {
      size += c.length;
      if (size > 1024 * 1024) { reject(new Error('body too large')); req.destroy(); return; }
      chunks.push(c);
    });
    req.on('end', () => {
      if (!chunks.length) return resolve({});
      try { resolve(JSON.parse(Buffer.concat(chunks).toString('utf8'))); }
      catch (e) { reject(e); }
    });
    req.on('error', reject);
  });
}

function sendStatic(req, res, urlPath) {
  let p = decodeURIComponent(urlPath.split('?')[0]);
  if (p === '/') p = '/index.html';
  const file = path.normalize(path.join(PUBLIC_DIR, p));
  if (!file.startsWith(PUBLIC_DIR)) { res.writeHead(403); res.end(); return; }
  fs.readFile(file, (err, data) => {
    if (err) { res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' }); res.end('404'); return; }
    res.writeHead(200, {
      'Content-Type': MIME[path.extname(file)] || 'application/octet-stream',
      'Cache-Control': 'no-store',
    });
    res.end(data);
  });
}

const server = http.createServer(async (req, res) => {
  const u = req.url || '/';
  try {
    if (req.method === 'GET' && u.startsWith('/api/events')) {
      res.writeHead(200, {
        'Content-Type': 'text/event-stream',
        'Cache-Control': 'no-store',
        'Connection': 'keep-alive',
      });
      res.write('retry: 1500\n\n');
      res.write(`event: status\ndata: ${JSON.stringify({ state: engine ? 'connected' : 'disconnected', detail: '' })}\n\n`);
      sseClients.add(res);
      req.on('close', () => sseClients.delete(res));
      return;
    }
    if (req.method === 'POST' && u.startsWith('/api/connect')) {
      const body = await readBody(req);
      const host = String(body.host || '127.0.0.1');
      const port = Number(body.port || 9527);
      if (!Number.isInteger(port) || port < 1 || port > 65535) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: '端口不合法' }));
        return;
      }
      const r = await connectEngine(host, port);
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(r));
      return;
    }
    if (req.method === 'POST' && u.startsWith('/api/send')) {
      const body = await readBody(req);
      const lines = Array.isArray(body.lines) ? body.lines : null;
      if (!lines || !lines.every((l) => typeof l === 'string') || lines.length > 600) {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: '参数不合法' }));
        return;
      }
      if (!engine) {
        res.writeHead(409, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: false, error: '未连接引擎' }));
        return;
      }
      engine.write(lines.map((l) => l + '\r\n').join(''));
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true }));
      return;
    }
    if (req.method === 'POST' && u.startsWith('/api/disconnect')) {
      if (engine) { try { engine.destroy(); } catch { /* 忽略 */ } }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true }));
      return;
    }
    if (req.method === 'GET') { sendStatic(req, res, u); return; }
    res.writeHead(405); res.end();
  } catch (err) {
    res.writeHead(500, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ ok: false, error: err.message }));
  }
});

// 心跳：防止中间层掐断空闲 SSE
setInterval(() => {
  for (const res of sseClients) { try { res.write(': hb\n\n'); } catch { /* 忽略 */ } }
}, 15000).unref();

server.listen(PORT, '127.0.0.1', () => {
  console.log(`五子棋 GUI 桥接已启动:  http://localhost:${PORT}`);
  console.log('引擎默认地址: 127.0.0.1:9527 （可在页面设置中修改）');
});

// 可选：引擎主动拨入模式（BRIDGE_LISTEN_PORT=9527 时，GUI 监听等待引擎连接）
if (LISTEN_PORT > 0) {
  net.createServer((sock) => {
    adoptSocket(sock);
    setStatus('connected', `引擎拨入 ${sock.remoteAddress}`);
  }).listen(LISTEN_PORT, '127.0.0.1', () => {
    console.log(`拨入模式监听中: 127.0.0.1:${LISTEN_PORT}`);
  });
}
