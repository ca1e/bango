#!/usr/bin/env node
// 把标准 pbrain- 引擎（stdin/stdout 协议）暴露为 TCP 服务，供 GUI 连接。
// 每个TCP 连接拉起一个引擎进程，字节透传（stdin↔socket / stdout↔socket）。
// 用法: node tools/tcp-wrap.js --engine /path/to/pbrain-xxx [--port 9527]
'use strict';
import net from 'node:net';
import { spawn } from 'node:child_process';
import path from 'node:path';

const args = process.argv.slice(2);
const opt = (n, d) => { const i = args.indexOf(n); return i >= 0 ? args[i + 1] : d; };
const enginePath = opt('--engine');
const port = Number(opt('--port', 9527));

if (!enginePath) {
  console.error('用法: node tools/tcp-wrap.js --engine <pbrain-可执行文件> [--port 9527]');
  process.exit(1);
}

net.createServer((sock) => {
  const child = spawn(enginePath, [], {
    cwd: path.dirname(enginePath) || '.',
    stdio: ['pipe', 'pipe', 'inherit'],
  });
  sock.pipe(child.stdin);
  child.stdout.pipe(sock);
  child.on('error', (err) => console.error(`tcp-wrap: 引擎启动失败: ${err.message}`));
  child.on('exit', () => { try { sock.destroy(); } catch { /* 忽略 */ } });
  sock.on('close', () => { try { child.kill('SIGKILL'); } catch { /* 忽略 */ } });
  sock.on('error', () => {});
  console.log(`tcp-wrap: 新连接，已拉起引擎进程 pid=${child.pid}`);
}).listen(port, '127.0.0.1', () => {
  console.log(`tcp-wrap: ${enginePath} → 127.0.0.1:${port}`);
});
