# 五子棋协议对弈 GUI

浏览器图形界面的 Gomocup 协议五子棋对弈程序：前端扮演协议中的**管理器（Manager）**，
AI 引擎扮演 **Brain**，两者通过 TCP `127.0.0.1:9527` 通信。
协议逻辑完全遵循 [`../protol.md`](../protol.md)（官方 Piskvork 协议），唯一差别是
stdin/stdout 换成了网络字节流。

![对局中](docs/screenshots/02-对局中.png)

## 架构

```
浏览器（管理器逻辑 + 胜负/禁手判定 + Canvas 棋盘）
   │  SSE 推送引擎输出行 ／ POST 下发命令行
本地桥接服务（Node，零依赖，只做字节搬运）
   │  TCP
AI 引擎 127.0.0.1:9527
```

浏览器无法直接建立 TCP 连接，因此需要一个本地桥接；桥接刻意不做任何协议语义，
全部协议状态机在前端 `public/js/protocol.js` 中实现，方便对照页面底部的
**协议日志**（双向收发行着色展示，含 MESSAGE/DEBUG）调试。

## 快速开始

```bash
cd gui

# 1) 启动 GUI（浏览器打开 http://localhost:9528）
node server/index.js

# 2a) 接入本仓库引擎（原生 TCP web 模式）
cd .. && ./gomoku web --port 9527

# 2b) 或接入任意标准 pbrain 引擎（stdio↔TCP 包装，每个连接拉起一个进程）
node gui/tools/tcp-wrap.js --engine ../pbrain-bango --port 9527

# 2c) 或没有引擎时用协议模拟引擎联调（--dumb 为随机落子）
node gui/tools/mock-engine.js --port 9527 [--dumb]
```

端口可用环境变量覆盖：`PORT`（GUI，默认 9528）、`BRIDGE_LISTEN_PORT`（可选：
桥接改为监听模式，等待引擎拨入）。引擎地址可在页面"对局设置"中修改，默认
`127.0.0.1:9527`，设置经 localStorage 记忆。

## 功能

| 功能 | 说明 |
| --- | --- |
| 先手选择 | 玩家（执黑）/ AI（执黑），玩家执白时状态栏会提示 |
| 禁手规则 | 无禁手（默认，`INFO rule 0`）/ 有禁手·连珠（`INFO rule 4`） |
| 棋盘尺寸 | 15 路（默认）/ 20 路，引擎不接受时自动回退 20 路 |
| 棋子步数 | 每颗棋子上显示手数（末手以朱红圆环标记） |
| 落子计时 | 记录每步耗时：轮到你→点击为你的思考耗时，发出 TURN/BEGIN→应答为 AI 耗时；动态单位（`860ms` / `3.25s` / `12.4s`）显示在落子记录右侧 |
| 停止对局 | 对局中可随时停止：`END`→断开→回到空闲态，设置解锁可改，棋盘保留复盘 |
| 胜负判定 | 前端独立判定：无禁手连五及以上胜；连珠黑恰五胜、长连犯规判白胜、禁手落子判白胜、满盘和棋、黑无合法点判白胜 |
| 禁手标记 | 有禁手时黑方所有禁手点标红叉（RIF 9.3 递归口径），点击拒绝并 toast 说明 |
| 悔棋 | 撤回一个完整回合；对每颗引擎已知的子逆序发 `TAKEBACK`，引擎答 `UNKNOWN` 时自动回退为 END→重连→`START`→`BOARD` 重放 |
| 胜负提示 | 全屏结果卡（结果 + 原因）+ 棋盘胜线金环高亮 +「再来一局 / 查看棋盘」 |
| 重新开始 | 任意时刻可重开：`END` → 断开 → 重连 → `START`，引擎崩溃断线后同样可恢复 |
| 光标反馈 | 轮到你：可落点 `pointer`＋半透明预览子、禁点 `not-allowed`；非本轮落子（AI 思考/未开局/已结束）：`not-allowed`、无预览 |
| 协议日志 | 双向收发行、MESSAGE/DEBUG、意外行，均可折叠查看 |

![胜利弹窗](docs/screenshots/04-胜利弹窗.png)
![禁手标记](docs/screenshots/05-禁手标记.png)
![真实引擎](docs/screenshots/09-真实引擎bango.png)

## 协议实现要点

- 使用的命令：`START` `INFO` `BEGIN` `TURN` `TAKEBACK` `BOARD` `END` `ABOUT`；
  应答解析：`x,y` / `OK` / `UNKNOWN` / `ERROR` / `MESSAGE` / `DEBUG`。
- 发送统一 `CR LF`；接收兼容 `CR` / `LF` / `CRLF`（跨包 CRLF 短暂等待）；
  空行忽略；意外行记入日志、不退出。
- 每次落子命令前发 `INFO time_left 2147483647`（无限时哨兵）；开局前发
  `timeout_turn 10000`、`timeout_match 0`、`max_memory 0`、`game_type 0`、`rule`。
- 会话 = 一条 TCP 连接；开始对局/重开均为干净重连，与引擎 web 模式的串行会话吻合。
- 玩家的制胜手不再发给引擎（对局已结束）；每颗子记录 `sent` 标记，悔棋只回撤
  引擎已知的子。

## 禁手判定口径

对候选点四方向枚举 4 步内空点试填：填后过候选点连长恰为 5 计一个"四"；
恰为 4 且两端皆空（活四）计一个"三"；按参与棋子集合去重（活四两端同属一个四）。
三的成活点本身须非禁手点（RIF 9.3 递归，深度封顶 6 的近似）；恰五优先于一切
禁手形状。与引擎 `rules.go` 的口径一致（引擎未做递归，只会把合法点误判为禁手、
不会放过真禁手，两者不冲突）。

## 测试

```bash
node test/forbidden.test.mjs   # 12 个规则用例：恰五/长连/三三/四四/活四/四三/眠三/递归假活三/白胜
```

浏览器端到端走查（截图见 `docs/screenshots/`）：玩家/AI 先手两向对弈、连五胜负
弹窗与胜线金环、悔棋、禁手标记与点击拒绝、重新开始、引擎断线的优雅报错与恢复、
375px 窄屏布局；并已与真实引擎 `pbrain-bango web` 完成对接验证（落子、应答、
TAKEBACK 悔棋后再落子状态一致）。

## 已知边界

- 单浏览器会话设计（多标签页同时操作同一引擎连接的行为未定义）。
- 浏览器刷新会丢失前端局面（引擎侧无恙，重新开始即可），不做断线续局。
- 悔棋在 AI 思考中禁用（协议无取消命令），可随时「重新开始」硬重置。
- RIF 禁手递归深度封顶 6，极端嵌套局面按不禁处理（宽松方向近似）。

## 目录

```
gui/
├── PLAN.md              开发计划（架构与决策记录）
├── server/index.js      桥接服务：静态托管 + SSE/POST ↔ TCP
├── public/              前端（原生 ES Modules，无构建）
│   ├── index.html / css/style.css
│   └── js/protocol.js   EngineLink 传输层 + EngineManager（协议状态机）
│   └── js/rules.js      胜负判定 + RIF 禁手
│   └── js/board.js      Canvas 棋盘
│   └── js/app.js        应用装配
├── tools/mock-engine.js 协议模拟引擎
├── tools/tcp-wrap.js    stdio pbrain 引擎的 TCP 包装器
└── test/forbidden.test.mjs
```
