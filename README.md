# pbrain-bango — Gomocup 五子棋 AI 引擎

一个使用 Go 语言实现的五子棋（Gomoku）AI 引擎，遵循 [Gomocup/Piskvork 协议](protol.md)，通过标准输入输出与比赛管理器（Manager）进行文本交互。内置基于 **Minimax + 棋型评分 + Alpha-Beta 剪枝** 的 AI 算法，并支持迭代加深与时间控制。

## 一、项目概述

本项目是 Gomocup 五子棋 AI 竞赛中的「Brain（引擎）」端实现。其设计目标是将 UI 界面、对战调度与 AI 逻辑彻底分离，让开发者能够专注于 AI 算法本身。

- **语言**：Go 1.21（见 [`go.mod`](go.mod)）
- **模块名**：`gomoku`
- **可执行文件**：`pbrain-bango`（按协议要求，引擎可执行文件名必须以 `pbrain-` 开头）
- **通信方式**：基于 stdin/stdout 的文本协议，每行一条命令

## 二、项目结构

```text
gomoku/
├── main.go            # 程序入口：flag 解析、web(TCP)/管道双模式、双线程主循环与命令分发
├── engine.go          # 引擎核心：状态管理、棋盘操作、输出控制
├── algorithm.go       # AI 算法：评估函数（按线缓存）、Minimax+Alpha-Beta+PVS、门控 quiescence、
│                      # 杀手走法/历史启发、迭代加深、规则与禁手
├── book.go            # 开局库：数据源解析、8 对称规范化编译、查询
├── rules.go           # 规则判定：各规则胜负分派与连珠禁手检测
├── opening.go         # 经典 26 式开局：模式枚举、识别、理论区投影
├── tt.go              # 置换表：Zobrist 哈希与缓存读写
├── vcx.go             # 算杀：VCF/VCT 连续威胁搜索
├── protocol.go        # 协议数据结构：Command 定义与坐标解析
├── bookcmd.go         # 开局库维护子命令：validate / build
├── cmd/arena/         # 对弈 harness：双引擎管道对弈、随机开局、比分汇总（第八节验收用）
├── algorithm_test.go  # AI 单元测试：棋型评分、战术短路径、防守、quiescence 门控、走法生成契约
├── ai_selfplay_test.go # 自对弈回归测试与搜索统计
├── tt_test.go         # 搜索测试：置换表/PVS/杀手/历史开/关等价性、评估缓存位级校验、提速统计
├── time_test.go       # 时间控制测试：预算公式、中断回退、中断不污染置换表、续搜一致性
├── rules_test.go      # 规则测试：禁手形状（含四三/跳四交叉）、各规则胜负语义、连珠自对弈
├── engine_test.go     # 引擎状态测试：START 前棋盘分配、渲染、满盘兜底
├── vcx_test.go        # 算杀测试：杀棋发现、无假阳性、反击破解、强制挡四链
├── book_test.go / book_engine_test.go / bookcmd_test.go # 开局库：编译/查询/护栏/协议端到端/子命令
├── protocol_test.go   # 协议黑盒测试：管道与 TCP web 模式的一致性、命令错误路径
├── arena_test.go      # arena 冒烟测试（-short 跳过）
├── search_bench_test.go # 固定图面 + 固定深度的确定性搜索基准（go test -bench）
├── gui/               # 浏览器对弈前端（ES module）+ TCP 桥接服务；test/ 下三个 node 测试套件
├── openbook/          # 开局库源数据（默认启用 gomocup-2026-15x15.json + classic-26-15x15.json）
├── protol.md          # Gomocup/Piskvork 协议中文说明文档
├── go.mod             # Go 模块定义
└── .gitignore         # 忽略 pbrain-* 编译产物
```

> 编译产物 `pbrain-bango`（协议要求引擎可执行文件以 `pbrain-` 开头）不入版本库，按第六节自行编译生成。

## 三、核心架构

### 3.1 双线程设计（避免死锁）

协议明确要求避免单线程下的读写死锁（详见 [`protol.md`](protol.md) 第 26-29 行）。本项目采用双线程架构，在 [`main.go`](main.go) 中实现：

| 线程 | 职责 | 实现位置 |
| --- | --- | --- |
| 线程 1（读线程） | 唯一消费 stdin，解析命令并转发到通道 | [`readLoop()`](main.go:35) |
| 线程 2（思考线程） | 从通道取出命令，执行思考并写入回复 | [`thinkLoop()`](main.go:98) |

两个线程通过带缓冲的 [`cmdChan`](main.go:12)（容量 16）协调。读线程是 stdin 的唯一消费者，思考线程无需在思考时读取输入，从结构上规避了引擎与管理器互相等待的死锁。

### 3.2 状态与并发安全

引擎状态由 [`Engine`](engine.go:14) 结构体持有，所有对棋盘和状态的访问均通过内嵌的 [`sync.Mutex`](engine.go:15) 保护：

- [`resetBoard()`](engine.go:64)：重置/分配指定大小的空棋盘
- [`place()`](engine.go:75)：在合法坐标处落子（越界自动忽略）
- [`aiMove()`](algorithm.go:62)：计算落子位置（快照棋盘后搜索，不长时间持锁）

### 3.3 输出与缓冲区刷新

协议强调必须及时刷新输出缓冲区（详见 [`protol.md`](protol.md) 第 23-24 行）。引擎使用 [`bufio.Writer`](engine.go:25) 包装 stdout，[`writeLine()`](engine.go:56) 在每行写入后立即调用 [`flushOut()`](engine.go:51) 刷新，确保管理器不会因收不到数据而阻塞等待。所有回复以 CRLF（`\r\n`）结尾。

## 四、协议命令支持

引擎在 [`thinkLoop()`](main.go:98) 中分发处理以下命令，完整覆盖协议要求的标准命令集：

| 命令 | 格式 | 说明 | 处理函数 |
| --- | --- | --- | --- |
| `START` | `START [size]` | 初始化游戏并创建空棋盘（size 须 > 5，默认 15） | [`cmdStart()`](main.go:126) |
| `BEGIN` | `BEGIN` | 引擎作为先手，立即输出第一步落子 | [`cmdBegin()`](main.go:138) |
| `TURN` | `TURN X,Y` | 告知对手落子，引擎分析后返回己方落子 | [`cmdTurn()`](main.go:144) |
| `BOARD` | `BOARD` + 多行 `X,Y,ID` + `DONE` | 设置完整棋盘状态（恢复对局） | [`cmdBoard()`](main.go:161) |
| `INFO` | `INFO key value` | 接收管理器推送的时间控制等配置 | [`cmdInfo()`](main.go:174) |
| `END` | `END` | 结束对局，回复 `OK` | [`thinkLoop()`](main.go:111) |
| `RESTART` | `RESTART` | 重置棋盘并重新开始，回复 `OK` | [`thinkLoop()`](main.go:113) |
| `ABOUT` | `ABOUT` | 返回引擎信息 | [`thinkLoop()`](main.go:116) |
| `PRINT` | `PRINT` | 打印当前棋盘（调试用） | [`cmdPrint()`](main.go:209) |
| `TAKEBACK` | `TAKEBACK X,Y` | 可选命令：悔棋，移除指定棋子，回复 `OK` | [`cmdTakeback()`](main.go) |
| `PLAY` | `PLAY X,Y` | 可选命令：管理器强制落子（SUGGEST 的应答），回显坐标 | [`cmdPlay()`](main.go) |
| `RECTSTART` | `RECTSTART W,H` | 可选命令：矩形棋盘初始化；仅支持方形（W==H），否则 `ERROR` | [`cmdRectStart()`](main.go) |
| `SWAP2BOARD` | `SWAP2BOARD` + 多行 `X,Y` + `DONE` | 可选命令：Swap2 开局（摆三子 / SWAP 或续着） | [`cmdSwap2()`](main.go) |
| 未知命令 | - | 回复 `UNKNOWN` | [`thinkLoop()`](main.go:120) |

### 4.1 BOARD 多行命令处理

`BOARD` 是协议中唯一的例外——单条命令跨多行。读线程在 [`readBoardBody()`](main.go:65) 中逐行累积 `X,Y,ID` 三元组，直到遇到 `DONE` 才组装成完整的 [`Command`](protocol.go:10) 发往通道，保证思考线程收到的是已解析完毕的数据。

### 4.2 INFO 配置存储

[`Info`](engine.go:31) 结构体存储管理器推送的配置，同时保留原始键值对（[`raw`](engine.go:39)）。当前仅存储不做事前规则判定（如连珠禁手检查），支持的键包括：

| 键 | 字段 | 含义 |
| --- | --- | --- |
| `timeout_turn` | `TimeoutTurn` | 每步限时（毫秒，0 = 尽快落子） |
| `timeout_match` | `TimeoutMatch` | 整局限时（毫秒） |
| `max_memory` | `MaxMemory` | 内存上限（字节） |
| `time_left` | `TimeLeft` | 剩余时间（毫秒） |
| `game_type` | `GameType` | 游戏类型码 |
| `rule` | `Rule` | 规则码：1 自由、2 连珠、4 连珠、9 Caro |
| `fast` | `Fast` | 快速模式 |

## 五、AI 算法（Minimax + 评分 + Alpha-Beta 剪枝）

[`algorithm.go`](algorithm.go) 实现了完整的传统搜索 AI，由以下模块组成：

### 5.1 棋盘表示与胜负判定

搜索在棋盘的扁平副本 `b[y*size+x]`（0 空 / 1 己方 / 2 对手）上进行，落子/撤子即写入/清零。胜负判定由 [`winsMove()`](rules.go) 按当前规则分派（见 5.8 节）。

### 5.2 着法生成

[`genMoves()`](algorithm.go:268) 只考虑与已有棋子切比雪夫距离 ≤ 2 的空点，避免全盘枚举导致分支爆炸。

**候选数据增量化**：落子/提子唯一原语 `setStone` 在评估线缓存之外再维护三份增量状态——子数计数器、半径 2 / 半径 1 两张邻域引用计数表（取代每空格的 5×5 / 3×3 扫描）、以及**逐格逐向棋型值缓存**：`pointScore` 的单方向体（双向数子 + 开端 + 跳形折半）按 (格 × 4 向 × 2 方) 缓存，`setStone` 对 4 个方向上 ±9 步依赖窗口内的格子打脏标记（按方向×方共 8 位），读取时惰性重算。正确性门槛：[`TestShapeCacheExact`](shape_cache_test.go) 在随机 make/undo 序列上逐步断言缓存值与独立 oracle 逐位相等（含子数与两张邻域表）。搜索树与逐空格扫描版本逐位一致。

**威胁梯子双保留不变量（2026-08 修复「活三不防」）**：[`genMovesRanked()`](algorithm.go) 的每一级威胁梯子都同时保留**双方**的格子——行棋方自己的获胜点在前、对方的紧随其后（参考实现 eval.js `bySide`/`orderedSet` 同款结构）。成五级合并 `fivesMe+fivesOpp`、活四级合并 `liveFourMe+liveFourOpp` 并让双方冲四随行；单边提前返回只在引擎行棋节点语义成立（"我方活四下一手必胜"），放到对方行棋节点会把对方自己的反击点整体隐藏——搜索据此构造出幻影必胜分（例如对方节点候选只剩挡点时，己方活四威胁被评成 `winScore−6`），根节点于是放弃挡对方的活三转头"进攻"（`TestGenMovesKeepsOpponentCounterThreats` / `TestGenMovesKeepsOpponentFive` / `TestAnswersLiveThreeWithOwnFourThreat` 回归）。附带收益：活四级随行对方的冲四点，`x.ooo` 跳四的缺口封堵点重新进入候选集。

### 5.3 静态局面评分（评估函数）

[`evaluate()`](algorithm.go:404) 逐行扫描全部行、列、对角线，由 [`scoreLineFor()`](algorithm.go:452) 识别棋型并累计分值，最终评分 = 己方总分 − 对方总分。评分表：

| 棋型 | 分数 | 棋型 | 分数 |
| --- | --- | --- | --- |
| 五连 | 10,000,000 | 眠三 | 5,000 |
| 活四 | 1,000,000 | 跳活三 | 15,000 |
| 冲四 | 100,000 | 活二 | 2,000 |
| 活三 | 30,000 | 眠二 | 500 |

评分同时处理跳棋型（如 `oo.oo` 为冲四、`.oo.o.` 为跳活三），并要求活三具备延伸空间（`x.ooo.x` 记为眠三）。

全盘扫描的结果按线缓存（见 5.14）：落子/撤子只重扫穿过的 4 条线，`evaluate()` 本身 O(1) 运行总和；[`evaluateFull()`](algorithm.go) 保留原扫描逻辑作为位级对照基准。

### 5.4 Negamax 搜索 + Alpha-Beta 剪枝

搜索采用教程（guidebook）第 9 章的 **negamax** 形式：单一角色，每个节点的分值都从**行棋方**视角出发（`stmEvaluate`），父节点把子节点结果取负——`score = -negamax(depth-1, -beta, -alpha)`——不再有最大化/最小化两份分支，PVS 窗口、胜负分、置换表分值全部统一为一种记法。`alpha`/`beta` 约束行棋方视角的分值，剪掉不可能影响决策的分支。搜索中还做了几个关键优化：

- **即时胜负检测**：每层落子后先看 `winsMove`，能成五直接返回 `winScore - ply`（行棋方赢为正；取负后的父节点天然看到"越晚输越好"，靠近根的胜利分更高，AI 优先选最快的赢法）；
- **偶数层迭代**：从深度 2 开始按偶数加深，保证叶子局面总是对手应对之后的局面，规避水平线效应；
- **PVS 主变例搜索**：见 5.12；
- **LMR 后期走法降深**：安静尾巴（`quietFrom` 之后）第 4 手起、深度 ≥3 的着法先按 depth−2 零窗侦察，越窗才全深重搜。与剪枝不同，LMR 会改变搜索值（故以 `lmrEnabled` 门控，位等价测试套件在无 LMR 栈上运行），强度收益由战术套件 + arena 对弈验收：固定深度 12 节点数 34,858 → 25,047（−28%）。
### 5.5 着法排序

[`pointScore()`](algorithm.go:330) 对每个候选点做快速的「落子后四方向棋型」预估，以 进攻分 + 防守分 降序排序。好的排序让 Alpha-Beta 剪枝接近最优效率，是搜索深度达到 6~8 层的关键。

### 5.6 迭代加深与时间控制

[`run()`](algorithm.go:109) 从深度 2 逐层加深，上一轮最佳着法在下一轮优先搜索；预算由 `INFO timeout_turn` / `time_left`（毫秒）换算（默认 1 秒，取 `timeout_turn` 的 90%，并保留整局时钟 3/4 的余量），超时的层直接放弃、沿用上一层结果。**根节点 aspiration 窗口**：以上一层分值 ±3 万（一个活三量级，按局面波动自适应放大）为窗口搜索下一层，落窗外围绕观测边界几何加宽重搜——最终重搜保证根分值与全窗逐位一致，仅在分值平稳时省节点。15 路中局实测：深度 12 累计 ~0.55s，**深度 14 于 ~2.8s 健全完成**（`maxSearchDepth` 上限即 14）。

### 5.7 置换表与 Zobrist 哈希

不同的走法顺序可能到达同一局面（"置换"，如先下 A 再下 B ≡ 先下 B 再下 A）。[`tt.go`](tt.go) 为每个 (盘面, 行棋方) 节点维护 64 位 Zobrist 指纹，缓存搜索结果：

- **哈希**：每个交叉点 × 双方各分配一个固定种子（splitmix64）生成的 64 位随机数，局面哈希为全部棋子随机数的异或；落子/撤子时只需增量 XOR 一次。行棋方也参与键值（双方角色搜索中"我走"与"对方走"是不同节点）。
- **查询**：进入 `minimax` 节点先查表。表内深度足够时：EXACT 直接返回；LOWER（下界，来自 β 截断）满足 `score ≥ β` 时返回；UPPER（上界）满足 `score ≤ α` 时返回；深度不足则仅复用表内**最佳着法**置前排序。
- **存储**：节点搜索完成后按结果分类——β 截断记 LOWER、值不超过 α 记 UPPER、否则 EXACT（均为**行棋方视角**的 negamax 分值）。表为**两路组相联**（guidebook §7.2 同款）："浅而新"与"深而旧"共存一桶，替换选较浅者、严格更深者绝不因更浅新条目被逐（§7.4 深度优先语义的两路版）；默认 2¹⁸ 桶 ×2 路，内存维持 ~12.6MB。每路槽位为 `atomic.Pointer[ttEntry]`（2026-08 为 lazy SMP 并发化）：写入构造新条目后一次原子替换，读取零锁无撕裂。
- **正确性保障**：胜负分 `±(winScore − ply)` 是相对根节点的值，永不作为表内截断返回（其最佳着法仍用于排序）；超时中断的层不写表。表长默认 2¹⁹ 条（约 12.6MB），按 `INFO max_memory` 自动收缩（变化即时重建）。
- **跨手保留**（2026-08）：表由 `Engine` 持有而非每次思考新建——Zobrist 种子固定，跨手哈希天然一致，上一手的深条目下一手直接命中。`START/RESTART` 同尺寸沿用；换棋盘尺寸或 `max_memory` 变化时重建；新对局（`resetSession`）丢弃。实测同预算下跨手复用可将转置局面的重搜降到 1%（12 节点 vs 1864），arena 50 局 28:22 优胜。
- **验收测试**：固定深度下开启/关闭置换表必须得到完全相同的根分值与着法（[`tt_test.go`](tt_test.go) `TestTTSearchEquivalence`），同深度 8 节点数降至原来的 ~83%，迭代加深整流程耗时约减半。

### 5.8 规则体系与禁手（INFO rule）

[`rules.go`](rules.go) 按 `INFO rule` 代码实现各规则的胜负判定与连珠禁手：

| 代码 | 规则 | 胜负与禁手 |
| --- | --- | --- |
| 0 | Freestyle | 5 个或以上同色连子获胜，无禁手（默认） |
| 1 | Standard | 恰好 5 个获胜，长连不算赢 |
| 4 | Renju | 黑方禁手（三三、四四、长连），白方无禁手且长连算赢 |
| 8/9 | Caro | 恰好 5 个获胜，但两端同时被对方堵住则不算赢（边界算开放端） |

- **胜负判定**统一走 [`winsMove()`](rules.go)：搜索、算杀、战术捷径全部按当前规则判定，不再使用固定的 `>=5`。
- **禁手在着法生成阶段过滤**（`genMoves` 与算杀的 `forcingMoves`）：黑方的候选列表永不包含禁手点，搜索树不会进入非法分支，评估函数与剪枝不受扰动。禁手检测在四个方向上统计「填一格可成五」的四与「填一格可成活四」的三，按成 virtually 同一棋型去重（活四两端/活三两端只算一个，`oo_P_oo` 同线双三正确算两个）；五子优先原则（RIF）：成五的落点永不禁手。三的枚举要求补填格真正加入连内（`k ∈ [−a, b]`），避免 P 自身已成活四时线旁旁观空格让 c==4 成立、凭空多出假三把「四三」误判成 3-3 禁手（`TestFourThreeIsLegal` 回归）。
- **黑方归属**：引擎可能执黑或执白。`BEGIN`（引擎先行）、首着 `TURN`（对手先行）与 `BOARD` 首子（连珠按落子顺序）自动判定黑方归属，禁手只作用于黑方。
- 已知简化：三的「该三的成四点本身是否禁手」递归判定未实现（RIF 完整规则），极端局面下可能保守地避开一个合法点，不会走出非法着法。

### 5.9 算杀（VCF/VCT）

[`vcx.go`](vcx.go) 实现教程第八章的「算杀」：只考虑连续威胁（冲四/活三）的证明搜索。正常迭代加深没有找到必胜解时，先做 12 层 VCF（仅冲四），再做 10 层 VCT（冲四+活三），各占一步预算的 1/4：

- **MAX 层（进攻方）**：只走己方的四（VCT 再加活三），任一分支成五即证明杀棋；
- **MIN 层（防守方）**：走双方所有的四/活三（防守含反击）**与挡成五点、挡活三的成活四点**，有一种防住即证明不成杀。防守列表按 quiescence `fourThreats` 同款双视角构建——若只列防守方自己的四/活三，「没有反四」会被误读成「无法防守」，单纯冲四（挡一格即化解）就被当成必胜返回；成五点入防守列表（key 略低于己方成五，有五先赢）。**2026-08 二次修复**：VCT 模式下防守层再加「对方活三的成活四点」（key=冲四−1）——否则进攻方把活二长成新活三时，该三尚无成五点可挡、防守集合为空，连一个裸活二都被「证明」必胜并绕过主搜索直接落子（`TestKillSearchLiveTwoNotAKill` / `TestKillSearchFreshThreeDefended` 回归，真杀测试 7/7 保全；修复后 arena 50 局 26:24 优胜）。活四仍不可防、含强制挡四的真杀链不受影响；
- **无假阳性**：搜索超出深度视作防守成功，绝不误报杀棋。
- **已证胜局护栏（2026-08）**：主搜索已证明己方必胜（`prevScore ≥ winScore−64`）时整个杀棋搜索跳过——只看连续威胁的窄证明不得用未证明的替代着法覆盖主搜索的全宽胜解。
- **对方杀棋探测（2026-08，参考实现 candidateMinmax 防线）**：己方 VCF/VCT 均未证明时，以对方为进攻方跑一次 VCT——若对方（假如下一手轮到它）存在连续威胁必胜，则**占据其主威胁点**，且落子后重探验证封锁确实瓦解证明才采纳（封不掉的多点叉威胁交回主搜索着法；连珠下黑方引擎落封锁点前查禁手，预算占 1/8）。`TestOppKillProbeBlocksOpponentKill` / `TestOppKillProbeLiveTwoNotAKill` / `TestOppKillProbeRejectsUnblockableFork` 回归。

### 5.10 门控 quiescence

叶节点采用参考实现同款的 quiescence search，取代教程第九章的「冲四延伸」（落子成四的分支额外 +2 层）——延伸会让强制链子树越搜越深，而 PVS 的零窗口（β=α+1）无法廉价证伪这些链：

- **门控**：`quiescence()` 入口检查「最后一手是否刚成四」（复用 `extendsOn`）。不变量：quiescence 自身展开的每一手都是成四点或挡四点，未决的四只可能来自刚刚那一手——不在门内直接返回静态评估，安静叶节点零额外开销；
- **展开集合**：[`fourThreats()`](algorithm.go) 列出双方的成四/成五点（活四两端、跳四缺口均由 `pointScore` 识别），双方同权、强者先行，连珠禁手照常过滤；深度上限 4 层（恰好「四→挡→反四→挡」一个来回）；
- **stand-pat**：轮走方若已在 (α, β) 之外则直接截断（negamax 视角：行棋方分值 ≥ β 返回、子节点取负递归 `-quiescence(3-side, -β, -α)`）；
- **盲区与兜底**：双四被挡其一后遗留的四由静态评估的量级兜底（盘面四型已按 −100 万/−10 万 记分）；
- **活三扩展（2026-08，默认开）**：`qThreeEnabled` 把门控放宽到「末手刚成（含跳）活三」——按穿过该子的实际连子判定（连三须两端皆空；跳三按 ±4 窗口内同色 3 子、跨度 ≤5），展开集合同步把「对方活三的成活四点」纳入挡点（与算杀防守层同款双视角修正，否则门一开对方活三无人能挡）。验收：arena A/B 50 局、400ms/手、seed 11，**开 28:22 胜关**，默认开、`BANGO_QTHREE=0` 可关；属值改变特性，黄金基线与 LMR 一同排除。

效果对比（对手冲四在手、`TestQuiescenceSeesFive` 实测，行棋方视角）：静态评估 1,000,600（只见四型），quiescence 9,999,998（看到五）。

### 5.11 开局库

[`book.go`](book.go) 移植教程原生项目（gobang）的开局库模块，数据格式互通：

- **数据源**：JSON（`id/rules/size/coordinateSystem/openings[].coordinates`），支持 `board-row-column` 与 `center-relative-x-y`（Gomocup 官方开局页格式）两种坐标系；`rules` 支持 `freestyle`/`renju`，规则或棋盘尺寸不匹配的书整本禁用
- **经典 26 式开局**（[`opening.go`](opening.go)，2026-08）：黑1 天元 + 白1 直指/斜指 + 黑2 在天元 5×5 盒内的对称类，规范化去重后**恰好 13 直指 + 13 斜指 = 26 式**，与经典计数吻合。`book gen-classic` 枚举种子线，`book build --seeds` 以固定深度搜索生长续着（成五点终止行、绝不入书），产出 `openbook/classic-26-15x15.json`。**定位为模式引导书**（`modeBook`）：其续着来自深度 8 自搜索、弱于实战搜索，arena 实测直接跟走会输棋（11:19），故只作理论区来源、绝不直接采纳；直接采纳仍只用赛事级 gomocup 书
- **模式识别与理论区**（P2/P3）：`openingModeOf` 按同一规范化机器识别前三子的模式（黑1 须天元、白1 须贴身直/斜、黑2 须在盒内，4 子时忽略偏离的白2；非天元/远距应答 → 非经典模式，干净降级）；`modeTheoryZone` 把模式引导书在该模式规范前缀下的应答投影回棋盘框架——作为**软先验**：3-4 子时理论格始终保留并排根列表最前，其余安静候选仍须贴身/马步；搜索仍自行裁决
- **编译**：每条开局序列按前缀展开成「局面 → 下一手候选」映射，局面经 **8 对称规范化**（4 旋转 × 2 镜像，字典序最小 key，并记录自同构群）；同局面多来源/多对称命中时权重累加
- **查询键**为石子集合（颜色相对黑方编码）——引擎无需着法历史，`BOARD` 恢复的局面同样可命中；**首着泛化**：单子局面未直接命中时，把书里所有「第 1→2 手」位移平移到实际首子位置，按贴身/马步准则过滤（与开局先验同判据——远距位移对随机首子是几何上的任意点，自对弈实测 42 局随机开局中 38 局产生远距首应答，过滤后归零），且**只偏置排序、不直接采纳**（arena 实测翻译候选直接采纳对基线 26:34 落败，降级为排序后 29:31 平手；精确命中的书着不受影响照常采纳）（`TestTranslatedFallbackClassicOnly` 回归）
- **使用决策**（[`run()`](algorithm.go)，与原生实现一致）：当前局面**无强制性威胁**（`hasOpenThreat`：任一方有成四/成五点，或按精确棋型——连三两端空 / ±4 窗口 3 子跳三——存在活三制造点）时直接采纳权重最高的书着法；战斗局面只把书权重注入根节点排序（`applyBookOrdering`），搜索仍自行裁决。**2026-08 门槛重写**：旧 `hasThreatAtLeast(活三)` 用四方向 `pointScore` 求和，而 `dirShape` 对跳形打半价（15000），两个互不相干的跳形在不同方向求和 30100 即冒充活三——加上贴身接触局面从第二手起必然存在真实 30000 点，"直接采纳"路径实际从未生效；新判定逐方向精确分类（`TestHasOpenThreatShapes` 回归）。**颜色映射修复**：书键按"相对黑方"编码颜色，`aiMove` 原本只在 `rule != 0` 时调用 `setRule`，freestyle（默认规则）下执白时 `blackSide` 停留在默认 `playerMe`，查询键颜色全部颠倒、第二手起永远匹配失败（只有不校验颜色的单子平移兜底幸存）——现无条件设置映射（`TestBookWhiteFreestyleAdoption` 回归）
- **护栏**：书着法先过 `isForbidden`（连珠）/占位过滤；书文件缺失或损坏静默降级为纯搜索
- **加载**：`INFO folder` 指定的持久目录下 `pbrain-bango/book.json` 优先，其次引擎可执行文件同目录 `book.json`（协议提供即独占）；否则装载工作目录/可执行目录 `openbook/` 下的默认书——`gomocup-2026-15x15.json`（直接采纳书）与 `classic-26-15x15.json`（模式引导书），工作目录兜底覆盖 `go run .` 的临时可执行文件场景；规则或尺寸不匹配时静默禁用
- **无库开局先验**（2026-08，默认开，`BANGO_OPENPRIOR=0` 关）：书未命中/未采纳时，前 4 子内根节点的**安静候选**（落子后不成活三及以上，精确棋型判定）必须与任意棋子贴身（切比雪夫 ≤1）或成马步（曼哈顿 3）——远距斜二、带缺口的分裂跳二等非经典发展被过滤；强制着法（成三/四/五点）与书采纳路径完全不受影响，过滤后为空则回退原列表

维护子命令：

```bash
pbrain-bango book validate book.json   # 六项校验：越界/重复手/成五/连珠禁手/行棋方/局面重复
pbrain-bango book build --out book.json --depth 8 --ply 4 --width 2 --time 60000
                                       # 自搜索增长开局树（确定性深度搜索评分，逐位置保留
                                       # margin 内的最优候选，按规范键去重置换）
```

### 5.12 PVS（主变例搜索）

PVS 的依据是：主要变例通常只有一条，找到第一个能产生截断的走法后，其余走法大概率不比它好。[`minimax()`](algorithm.go) 与根节点 [`searchRoot()`](algorithm.go) 中：第一个走法全窗口搜索；其后走法先以零窗口（己方 (α, α+1)、对方 (β−1, β)）侦察，只证明该走法落在窗口外；侦察值仍落在 (α, β) 内才以全窗口重搜定值。`winsMove` 与叶子分支本就是精确值，不侦察不重搜。

PVS 是**值等价**变换——固定深度下根分值与着法与普通 Alpha-Beta 完全一致（`TestPVSSearchEquivalence`），节省的时间转化为同预算下更深的迭代。收益高度依赖排序质量：威胁阶梯 + TT 手置首 + 杀手走法使「第一手即最佳」的假设大体成立（实测 16 万次侦察仅 246 次落窗重搜）。

### 5.13 杀手走法与历史启发

排序信号在威胁阶梯与 TT 手之上再加两级统计（教程第九章链接项，参考实现同款机制）：

- **杀手走法**：每个 ply 两槽记录「刚在本层引发 β 截断的着法」（去重移位），排序时提到 TT 手之后的 1、2 号位——它们是本层被验证过的反驳着法，且紧随其后的零窗口侦察让失败成本近零；
- **历史启发**：按角色分表的 `history[2][n×n]`，截断时 `+= depth²`（越深的子树被截断越值得早试）；每次迭代加深前全表减半老化。应用范围严格分段：[`genMovesRanked()`](algorithm.go) 返回安静尾巴起点，成五/冲四/双三的威胁阶梯原序保留（`TestGenMovesPriorityLadder` 金丝雀），仅活三及更弱的尾巴按统计稳定重排，slot 0 永不重排。

实测（中局基准深度 12）：杀手走法把节点数砍半（73,717 → 35,296）；历史启发在杀手之后边际收益 ~0.5%（杀手指槽已覆盖大部分截断场景），保留因其成本近零、且安静尾巴更宽的实战局面中收益更大。

### 5.14 评估按线缓存

全盘 `6n−2` 条线（n 行、n 列、两族对角各 2n−1）各自缓存双方棋型分，`evalTotal` 为运行总和。落子/撤子统一走 [`setStone()`](algorithm.go)：增量 XOR 哈希 + 只重扫穿过该点的 4 条线（O(n)），`evaluate()` O(1) 返回。选线缓存而非教程的逐点缓存，是为了**位级等价**——评估语义一字不改，全部开/关等价测试自动成为回归覆盖。正确性门槛：[`TestEvalIncrementalExact`](tt_test.go) 在 9/15 路、freestyle/renju 四种模式下随机走子+悔子 3,000 步，每步断言缓存总和与全盘扫描逐位相等。基准耗时再降 3~4%（杀手剪枝后叶节点占比已低；叶评估 O(1) 化为后续「位置加成」类改进提供增量维护的基础）。

### 5.14a 位置加成（默认关）与 lazy SMP（默认关）

两个 env 门控特性，代码就绪但默认关闭：

- **位置加成 `BANGO_POS=1`**：中心金字塔项（天元 7 环、每环 ×10、至多 70 分，压在眠二 500 之下）按子增量维护（`posTotal` 运行总和），`evaluateFull` oracle 同步扩展——位级等价传统保留（`TestEvalIncrementalExact` 断言语义 `evaluate()` 对 oracle）。**arena 验收未通过**（50 局、400ms/手、seed 7：关 30:20 胜开），判定每节点成本与中心化平手判定得不偿失，默认关；量级调小或限定开局阶段后可重验。
- **lazy SMP `BANGO_SMP=N`（N>1 个 worker）**：`run()` 分裂 N 个私有 searcher（私有棋盘/历史/杀手，共享一张 Engine 级 TT），各自完整跑迭代加深、根着法按 worker 序号轮转（搜索树互补填表），聚合取最深完成迭代、同深 worker 0 优先。前提是 TT 并发安全（每路 `atomic.Pointer`，见 5.7）。**测试语义**：`TestSMPWorkersAgreeOnScore` 断言各 worker 分值与单线程全等（表共享只返回合法条目——硬门槛），着法不作断言（轮转下并列最优可合法漂移）；`TestSMPTTRaceClean` 以 4 worker 压小表过 `-race`。SMP 天然非确定，默认关，收益由 arena 让时对弈评估。

### 5.15 测试

- [`negamax_test.go`](negamax_test.go)：13 组固定局面固定深度的根分值/着法黄金基线，保值搜索栈（LMR 关闭）必须逐位复现预置数值（根节点行棋方恒为引擎，分值语义不变）；
- [`algorithm_test.go`](algorithm_test.go)：棋型评分表逐例校验；「一步成五必取」「对方四必挡」「活三必防」等战术断言；评估函数换边反对称校验；着法生成优先级阶梯（成五/活四级双方合并、行棋方在前，`TestGenMovesKeepsOpponentCounterThreats` 回归「活三不防」根因）、Chebyshev-2 候选窗的边界裁剪与去重、`quietFrom` 强制段/安静尾巴排序契约；门控 quiescence 的门控等价、五威胁识别、lastP 未知保守展开、双活四/遗留四的静态兜底（`TestQuiescenceGateQuietPosition`/`TestQuiescenceSeesFive`/`TestQuiescenceLeftoverFourFallsBackToStatic` 等）；
- [`tt_test.go`](tt_test.go)：Zobrist 增量哈希一致性、置换表读写/替换策略/同槽异锁碰撞保护、固定深度与**整轮迭代加深**的开/关等价性、`max_memory` 表收缩、剪枝安全性（开/关 Alpha-Beta 结果必须一致）与提速统计；PVS、杀手走法、历史启发各自的**开/关等价性**与提速统计；TT 胜利分区间守卫的正/反对照（区外深度条目直接复用、区内绝不作 cutoff，`TestTTWinScoreGuard`）；评估线缓存的位级属性测试与线 id 编解码契约（`TestEvalIncrementalExact`/`TestLineGatherRoundTrip`），含悔棋-重放型序列的 `TestEvalIncrementalWithTakebacks`；
- [`shape_cache_test.go`](shape_cache_test.go)：增量候选状态（子数/邻域引用计数/逐向棋型值缓存）的正确性门槛——随机 make/undo 序列逐步断言缓存与独立 oracle、计数器与全盘重扫逐位相等；
- [`time_test.go`](time_test.go)：`thinkBudget` 预算公式表驱动校验；极小预算下战术捷径必命中、真实思考必返回合法着法；deadline 过期后 `run` 回退且**置换表零写入**；中止节点（入口/移循环中）不入表；小预算中断后续搜满深度必须收敛到全新搜索的同分同着（`TestInterruptedThenResumedConsistent`）；
- [`search_bench_test.go`](search_bench_test.go)：中局/战术两个固定图面的确定性搜索基准（`go test -run '^$' -bench BenchmarkSearch -benchtime 1x -v`），输出节点数与截断数供优化对比；
- [`vcx_test.go`](vcx_test.go)：双三/活三必胜杀棋必须找到、安静局面不得误报、对方冲四可破解杀棋、可挡冲四不得误报必杀、经过强制挡四的真杀链必须找到；
- [`rules_test.go`](rules_test.go)：三三/四四/长连禁手、**四三合法**、跳四交叉 4-4、白棋无禁手、五子优先覆盖禁手、着法生成过滤（黑滤/白留）、各规则（0/1/4/8/9）胜负语义（含 caro 边缘端点、standard 长连不胜）、连珠自对弈完局；禁手枚举后增量评估/哈希不被扰动；
- [`book_test.go`](book_test.go) / [`book_engine_test.go`](book_engine_test.go) / [`bookcmd_test.go`](bookcmd_test.go)：开局库编译与对称查询、权重累积、首着平移、规则/尺寸护栏、禁手候选过滤、协议端到端命中、validate/build 闭环；**执白 freestyle 颜色映射采纳**、精确威胁门槛六例（安静/双方向跳二伪威胁/连三/跳三/x.x.x/成四点）、openbook 默认书路径与装载；
- [`ai_selfplay_test.go`](ai_selfplay_test.go)：完整自对弈回归（合法性、能分出胜负）与搜索深度/节点数统计（`go test -v -run TestSearchStats` 查看）；
- [`protocol_test.go`](protocol_test.go)：黑盒协议一致性（管道/TCP）、START 尺寸边界（5 OK / 4 与非法输入 ERROR）、TURN/TAKEBACK 错误路径与越界坐标宽容语义、BOARD 回着、field-3 标记格、可选命令、连珠避禁手、未知命令保活；
- [`engine_test.go`](engine_test.go)：START 前棋盘已分配（回归）、渲染内容、满盘 `run` 返回 (−1,−1) 与 `aiMove` 盘内兜底；
- [`arena_test.go`](arena_test.go)：`cmd/arena` 冒烟测试（`-short` 跳过），两局快棋比分汇总校验；
- `gui/test/`：三个 node 测试套件（`npm test`）——`forbidden.test.mjs` 前端规则 12 例、`protocol.test.mjs` EngineManager 应答配对/MESSAGE 剥离/超时/断连/TAKEBACK·BOARD 分型（FakeLink 注入，`EngineLink` 的 fetch/EventSource 浏览器路径不测）、`board.test.mjs` 棋盘坐标映射与 `cellAt` 命中判定（Proxy 伪 ctx 桩掉 canvas）。

## 六、构建与运行

### 6.1 编译

```bash
# 日常开发
go build -o pbrain-bango .

# 发布构建（优化参数：-s -w 去除符号表与 DWARF 调试信息，
# -trimpath 抹除本地路径，产物更小且不含构建机信息）
go build -trimpath -ldflags="-s -w" -o pbrain-bango .
```

### 6.2 运行

引擎作为控制台程序，通过 stdin/stdout 与管理器交互。可手动测试：

```bash
echo -e "START 15\nBEGIN\nTURN 7,8\nEND" | ./pbrain-bango
```

预期输出示例：

```text
OK
7,7
6,6
OK
```


### 6.3 Web 模式（TCP socket）

除 stdin/stdout 外，引擎可通过 `web` 子命令把协议迁移到 TCP socket 上：

```bash
./pbrain-bango web                          # 监听 0.0.0.0:9527
./pbrain-bango web --port 8080              # 指定端口
./pbrain-bango web --port 8080 --addr 127.0.0.1  # 指定端口与绑定地址
```

| 子命令/参数 | 默认值 | 说明 |
| --- | --- | --- |
| `web` | — | 子命令：启用 TCP socket 模式，替代 stdin/stdout |
| `--port` | 9527 | 监听端口（**仅在 `web` 子命令下有效**） |
| `--addr` | 0.0.0.0 | 绑定地址（仅在 `web` 子命令下有效） |

不带子命令直接运行（`./pbrain-bango`）即管道模式；在管道模式下传 `--port`/`--addr` 会得到 `unknown command` 错误并退出。

行为说明：

- 客户端连上后，协议命令从 socket 按行读取、回复按行写回，行格式与命令语义和管道模式完全一致（telnet / nc 可直接调试）。
- **串行单会话**：同一时刻只服务一个连接；客户端断开或发送 `END` 后连接关闭、棋盘重置，服务进程回到 accept 等待下一个连接（第二个客户端在 backlog 中排队）。
- web 模式下 `END` 表示对局结束：无应答、关闭当前连接、等待新会话；进程常驻。
- 监听地址打印到 stderr。

### 6.4 接入管理器

将编译产物 `pbrain-bango` 配置到 Gomocup 竞赛管理器（如 Piskvork）中即可参与对局。引擎会响应 `ABOUT` 命令返回：

```text
name="pbrain-bango", version="0.1", author="cale && GLM5.3", ai="minimax+alphabeta+vcx"
```

## 七、典型对局流程

以引擎先手为例（详见 [`protol.md`](protol.md) 第 198-211 行）：

```text
管理器 → 引擎：START 15      引擎 → 管理器：OK
管理器 → 引擎：BEGIN         引擎 → 管理器：7,7      （引擎第一步）
管理器 → 引擎：TURN 8,8      引擎 → 管理器：9,8      （引擎落子）
...（对局继续）...
管理器 → 引擎：END           引擎 → 管理器：OK
```

## 八、后续扩展方向

搜索优化（PVS、门控 quiescence + 活三扩展、杀手/历史、置换表、评估缓存）、VCF/VCT 算杀、连珠禁手与开局库均已落地（见第五节）。**2026-08 第二轮优化**已完成以下各项（各自带验收，详见对应小节）：

- **首着泛化贴身过滤 + 降级排序**（5.11，2026-08 第四轮）：平移兜底候选按贴身/马步准则过滤（远距首应答 38/42 → 0）并降级为仅排序——arena 实测直接采纳 26:34 回归，排序化后 29:31 与基线平手
- **经典 26 式开局：枚举书 + 模式识别 + 理论区先验**（5.11，2026-08 第四轮）：对称枚举独立复现 26 式计数（13 直 + 13 斜），`book build --seeds` 自搜索生长经典书并入默认装载；模式分类器与理论区投影给偏离局面软引导；顺手修复 `book validate` 多行间不悔棋的潜伏缺陷
- **开局库三层修复 + 无库开局先验**（5.11 / 5.2，2026-08 第三轮）：freestyle 执白颜色映射修复、`hasOpenThreat` 精确门槛重写、默认装载 `openbook/gomocup-2026-15x15.json`（Gomocup 2026 官方开局）；前 4 子安静候选贴身/马步先验（`BANGO_OPENPRIOR` 可关），协议端到端验证白棋对天元/跳形开局全部按书应答
- **威胁梯子双保留 + 对方杀棋探测**（5.2 / 5.9，2026-08 第三轮）：修复「活三不防」正确性缺陷——对方行棋节点上单边提前返回隐藏对方反击点、搜索构造幻影必胜并放弃防守；照参考实现 eval.js 把成五/活四级改为双方合并、行棋方在前，并新增对方杀棋探测（验证封锁才采纳）与已证胜局护栏。arena 对旧引擎 50 局 **50:0**（seed 7/13 各 30/20 局、300ms/手），回归测试 6 例
- **VCT 防守层补挡活三**（5.9）：修复「活二级假杀」正确性缺陷，arena 26:24 优胜
- **TT 跨手保留**：表由 `Engine` 持有、`START/RESTART` 同尺寸沿用、换尺寸/换预算重建；`newSearcherWithTT` 借用注入，跨手命中复用实测 12 节点 vs 1864 节点（1%），arena 28:22 优胜
- **协议参数补齐**：`INFO max_depth`（钳制迭代加深上限）/ `max_node`（`limitHit()` 周期检查、走既有中断路径）已解析并挂钩，端到端测试覆盖
- **quiescence 活三扩展**（5.10，默认开）：arena 28:22 优胜
- **lazy SMP**（默认关）：TT 每路 `atomic.Pointer` 化，`run()` 分裂多 worker 共享表、根着法轮转散开搜索树、取最深完成迭代；`go test -race` 全绿（含顺手修复的存量 stderr 竞争），`BANGO_SMP=N` 开启。**arena 验收：4 worker 31:19 优胜单线程**（50 局、400ms/手、seed 13，单侧 p≈0.04）——收益明确；默认暂关以保持引擎着法可复现，竞赛场景建议 `BANGO_SMP=4` 启动
- **位置加成**（默认关）：中心金字塔项按子增量维护、oracle 同步扩展（位级等价保留），但 arena 50 局**关 30:20 胜开**——每节点成本与中心化平手判定得不偿失，代码留 `BANGO_POS=1` 开关供后续调参（缩小量级或仅限前 N 手时再验收）

按预期收益排序的后续方向：

- **SMP 默认值评估**：首轮 arena 数据见下文，样本量与显著性不足时保持默认关
- **位置加成调参再验收**：量级减半（≤35）或仅开放局前 10 手，重跑 arena
- **棋力验收**：值等价或位级等价的优化（固定深度棋力不变、速度提升）由开/关等价测试守护；战术视野类改动（如 quiescence）最终需同预算、≥50 局、开局多样化的自对弈对局集确认
- **对弈 harness**：上述验收固化为可复现命令（[`cmd/arena`](cmd/arena/main.go)，管道对弈、随机首着开局（`PLAY` 强制，对手经首个 `TURN` 获知；两颗预置子会让后手引擎盲视，不可行）、颜色各半、非法着/超时/崩溃判负，比分逐局打印，`-verbose` 打印每手、`-json` 落盘，`-time-turn-b` 支持让时对弈）：

  ```bash
  go run ./cmd/arena -games 40 -time-turn 400 ./pbrain-a ./pbrain-b
  go run ./cmd/arena -games 30 -time-turn 200 -time-turn-b 400 ./pbrain-new ./pbrain-old
  ```

关于教程第九章列出但**不实现**的两项，理由备查：**TSS（威胁空间搜索）**已由 `genMoves` 威胁分级（防守点与攻击点同权分类）+ VCF/VCT 的防守层覆盖；**米字进攻路径**教程作者自认存在未解决的误判情形，威胁分级生成已达成同类剪枝效果。
