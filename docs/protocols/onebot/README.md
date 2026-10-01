# OneBot 11 / QQ

通过 [NapCatQQ](https://github.com/NapNeko/NapCatQQ) 的 OneBot 11 正向 WebSocket 接入 QQ 群。`onebot` 是本分支内置的 protocol，直接使用 matterbridge 的配置、日志、网关路由和可执行文件。

## 构建与启动

在仓库根目录使用 Go 1.26+ 构建：

```sh
go build -tags goolm -o matterbridge .
cp docs/protocols/onebot/matterbridge.example.toml matterbridge.toml
chmod 600 matterbridge.toml
# 编辑账号、Token、群号和其他平台配置后运行
./matterbridge -conf matterbridge.toml
```

默认构建包含 OneBot；`go build -tags goolm,noonebot` 可以排除该协议。`goolm` 沿用上游构建方式，避免系统 libolm 依赖。

先登录 NapCat，确保机器人 QQ 账号已经加入目标群。在 NapCat WebUI 启用 **WebSocket 服务端（正向 WebSocket）**，设置 Token，消息格式选 `array`，关闭上报自身消息。可参考 [NapCat 网络配置示例](napcat-onebot.example.json)。NapCat 仍是独立服务；桥接只需要一个 matterbridge 进程。

## 配置

```toml
[onebot.qq]
Server = "ws://127.0.0.1:3001/"
Token = "your-napcat-token"
RemoteNickFormat = "[{PROTOCOL}] <{NICK}> "

[[gateway]]
name = "qq-groups"
enable = true

[[gateway.inout]]
account = "onebot.qq"
channel = "123456789"

[[gateway.inout]]
account = "onebot.qq"
channel = "987654321"
```

此配置让两个 QQ 群互通。连接其他平台时，在同一份 TOML 中配置相应账号，并将频道加入所需 gateway。完整示例：[IRC + QQ](matterbridge.example.toml)、[纯 QQ 群间桥接](qq-only.example.toml)。

| 设置 | 含义 |
| --- | --- |
| `Server` | 必填，同时支持事件和动作的 `ws://` 或 `wss://` 地址；通常使用根路径，不使用 `/api` 或 `/event` |
| `Token` | NapCat 配置的访问令牌，通过 Bearer 请求头发送；服务端未设令牌时可留空 |
| `RemoteNickFormat` | matterbridge 通用昵称格式，直接使用已格式化昵称作为发送前缀 |
| `AllowMention` | 发往 QQ 的提及类型，默认 `["users"]`；`[]` 全部关闭，加入 `"everyone"` 可开启全体提及 |
| `PreserveThreading` | 默认 `true`，把已映射的父消息转成原生引用；`false` 使用文字引用摘要 |
| `channel` | 正整数十进制 QQ 群号字符串，不加前缀或前导零 |

`Server` 不接受 URL 用户信息、查询参数或片段，令牌请单独设置。可用 `MATTERBRIDGE_ONEBOT_QQ_TOKEN` 环境变量覆盖 `[onebot.qq]` 的 Token。容器部署时填写可互通的地址；跨公网使用 WSS 或私有隧道。

一个账号可以桥接多个群，多个 NapCat 实例分别配置成 `onebot.work`、`onebot.home` 等账号。`gateway.in`、`out`、`inout` 分别控制只入站、只出站和双向转发；过滤、昵称、同群多网关订阅都沿用 matterbridge 规则。`JoinChannel` 只订阅已加入的群，不自动申请加群。调整群号或 gateway 后重启进程。

## 消息与可靠性

| 输入 | 转发形式 |
| --- | --- |
| 普通文本、Unicode | QQ 文本段；符合规则的 `@` 转成原生提及段 |
| `user_action` / IRC `/me` | 带 `*` 的动作文本 |
| matterbridge 附件 | 文件名、HTTP(S) 链接和说明，不下载或重新上传 |
| QQ 文本 | 带源账号、群号、用户 ID 和消息 ID 的原生消息；群名片优先于昵称 |
| QQ @、表情 | 可读文本标记 |
| QQ 回复/引用 | 保留父消息关系；目标支持且消息映射仍在时使用原生引用，否则发送文字摘要 |
| QQ 图片、语音、视频、文件段 | 标记及事件提供的 HTTP(S) URL |
| QQ 合并转发 | IRC 显示临时频道入口；其他协议保留占位文字 |
| QQ JSON/XML 卡片 | 占位文字 |

支持接收 CQ 字符串和消息段数组。其他平台发来的 CQ 代码始终作为普通文字，不执行 CQ 中的 @、回复或文件操作。媒体链接可能过期，没有 URL 时只显示标记。暂不支持私聊、群管理通知、撤回、编辑同步、提及和引用以外的原生富文本；仅以 `notice` 上报的群文件也不转发。

机器人自身消息会过滤；重复事件按账号、群和消息 ID 在内存中去重 10 分钟，最多保留 4096 条。路由器负责排除来源频道和选择目标。WebSocket 断线后按 1–30 秒退避重连，并使用 Ping/Pong 检测失联。入站队列上限 256 条，满时丢弃新事件并记录错误；发送最长等待 15 秒，验证回执并返回目标消息 ID。发送失败或超时不会自动重试，没有持久化离线补发。关闭进程沿用 matterbridge 默认信号行为。

## IRC 合并转发临时频道

收到原生 `forward` 段后，通过 [`get_forward_msg`](https://github.com/botuniverse/onebot-11/blob/master/api/public.md#get_forward_msg-获取合并转发消息) 的字符串参数 `id` 读取内容。支持官方 `message` / `node` 数组，以及 NapCat 实际返回的 `messages` 消息对象数组。嵌套转发优先展开 `data.content` 中已有的消息，缺少内联内容时才按 ID 查询：NapCat 的内层 ID 不一定能用于 API。也支持内联 `node` 节点。CQ 字符串和消息段数组均可识别；转义后的 CQ 文字不会触发读取。

主频道显示 `[查看合并转发：/join #mb-forward-…]`。进入后，bot 按顺序发送作者、正文和媒体链接；每有新读者进入会从头重播，已有读者也会看到重播。无需客户端支持 IRC 历史消息。查看窗口使用普通 bot 消息，不依赖 `UseRelayMsg`，不把转发节点注册为可 @ 的 QQ 用户。

- 临时频道设置 `+snmt`（隐藏、禁止外部发言、只读、仅频道管理员修改主题），使用随机名称；不要把保留前缀 `#mb-forward-` 加进 gateway。
- 最后一位读者 PART、QUIT 或被 KICK 后，bot 自动 PART 并丢弃余下的展示队列；改名不会遗留虚假的成员。bot 被踢或 IRC 断线时丢弃窗口，不自动重建。
- 刚创建的频道只有 bot，默认留 **300 秒**等待首位读者；无人进入则自动 PART。在 `[irc.local]` 设置 `ForwardChannelTimeout = 300` 可调整等待秒数。清理后入口失效。
- 每个 IRC 账号最多同时打开 32 个窗口，所有窗口共用按 `MessageDelay` 限速的展示队列；每个窗口最多展示 1000 行。
- 每条 QQ 消息最多等待 5 秒、调用 8 次 API、保留 200 个内容节点和约 128 KiB 正文；嵌套转发最多展开 8 层，以 `↳` 标记层级。循环引用、读取失败和截断会显示说明。JSON/XML 卡片仍显示占位文字。

窗口仅用于阅读，不参与网关路由，不回传 QQ。内容保存在内存，媒体链接沿用 OneBot 返回值，可能过期。IRC 生命周期行为使用 Ergo 2.19.1 验证。

## 引用与回复

OneBot 和 IRC 账号默认开启 `PreserveThreading`，可显式设置为 `false` 改用文字引用。其他协议需按该协议的配置开启 `PreserveThreading`。引用依赖同一 gateway 内曾经转发过的消息；父消息按账号和频道分别映射，不会把源群的消息 ID 直接用于另一个 QQ 群。

- **IRC → QQ**：支持 IRCv3 的客户端使用回复功能发送 `+draft/reply` 标签，桥接将父消息映射为 QQ 原生 `reply` 段。回复一条从 QQ 转到 IRC 的消息时，也会找到 QQ 原消息。
- **QQ → IRC**：IRC 服务需支持 `message-tags` 并为消息分配 `msgid`；机器人通过 `echo-message` 获取自己发出的消息 ID。桥接发送 `+draft/reply`，具体引用显示由 IRC 客户端决定。长消息拆分后，引用定位到首个片段。
- **QQ 群间转发**：引用会指向目标群内对应的桥接消息，而不是源群消息。
- **不支持原生引用、关闭此功能或映射缺失**：保留 `[回复 昵称：原文摘要]`；原文不可用时保留 `[回复 #消息ID]`。IRC 的 `>` 文字和 `[CQ:reply,...]` 不会自动转换成原生引用。

消息映射保存在内存中，每个 gateway 最多保留 5000 个消息端点，重启或缓存淘汰后使用文字回退。OneBot 每个账号缓存最多 4096 条原文摘要；引用旧消息时可通过 `get_msg` 补取，最多等待 2 秒，并核对返回的群号与消息 ID。摘要最多保留 240 个 Unicode 字符，摘要中的 `@` 不会转换为 QQ 原生提及。不支持 IRCv3 的服务只能显示文字引用。

## 发往 QQ 的 @ 提及

同一 gateway 内，其他频道发来的正文支持以下写法：

| 写法 | QQ 目标群中的行为 |
| --- | --- |
| `@123456789 看一下` | 直接用 QQ 号构造原生个人提及 |
| `@张三 看一下` | 按目标群的群名片或昵称匹配成员，唯一匹配时构造原生提及 |
| `@Alice Smith 看一下` | 支持名字中的空格，优先匹配最长完整名字 |
| `@全体成员`、`@all` | 仅开启 `"everyone"` 时构造全体提及，默认保留文字 |

默认开启个人提及，无需额外配置。需要调整时，在对应账号下设置：

```toml
[onebot.qq]
# 与 Server、Token 等设置放在同一个账号段内。
AllowMention = ["users"]             # 默认：仅个人提及
# AllowMention = ["users", "everyone"] # 同时允许全体提及，仍受 QQ 权限和额度限制
# AllowMention = []                   # 全部作为普通文字
```

`@` 放在行首、空白或常见左括号/标点后，名字或 QQ 号后用空白、常见标点或行尾分隔。名字区分大小写，按每个目标群分别匹配；重名、找不到成员时保留原文，不会任选一人。重名时可改用 `@QQ号`。这不是跨平台身份绑定：来源平台的名字与 QQ 名字不同时，需要直接写 QQ 名字或号码。

成员列表在需要名字匹配时才获取，按群缓存 5 分钟；查找最多等待 3 秒，失败后保留名字文字并继续发送，30 秒后允许再次查询。直接写 QQ 号不需要查询成员列表。昵称前缀、附件说明、URL、邮箱和原始 CQ 代码不参与提及转换。QQ 号不会预先校验是否属于目标群，实际提醒由 QQ 按成员状态和平台规则处理。

QQ 群间转发时，目标 OneBot 账号会按上述规则处理。QQ 发往其他平台的原生成员提及显示为 `@群名片`（无名片时使用昵称，查询失败或无名称时保留 `@QQ号`），全体提及显示为 `@全体成员`；IRC 可按下面的配置启用反向提及。

## QQ → IRC 的 @ 提及

在 `matterbridge.toml` 的目标 IRC 账号段启用，默认关闭：

```toml
[irc.local]
# 与 Server、Nick 等设置放在同一个账号段内。
ReverseMention = true
BotMentionTarget = "alice" # 可选：QQ 原生 @机器人 时提醒的固定 IRC nick，不带 @
```

| QQ 中的写法 | IRC 收到的正文 |
| --- | --- |
| 正文输入 `@bob 看一下` | `bob 看一下`，可指定任意 IRC nick |
| 使用 QQ 提及功能选择机器人，再写 `看一下` | `alice 看一下`，由 `BotMentionTarget` 指定 |
| QQ 原生提及其他成员 | `@群名片` / `@昵称`，无法查询时保留 `@QQ号` |
| QQ 原生提及全体成员 | 保留 `@全体成员` |

成员显示名查询复用群成员缓存（5 分钟；失败缓存 30 秒），单次查询最多等待 3 秒。此显示不依赖 `ReverseMention`，也不绑定 IRC 用户。QQ 群间转发仍保留原生提及的 QQ 号身份。

只需要任意用户模式时，省略 `BotMentionTarget` 或设为 `""`；原生 `@bot` 将保留为 `@机器人QQ号`。设置固定用户后，正文 `@nick` 仍可使用。固定目标按 IRC 账号配置，作用于该账号的所有桥接频道；不同 IRC 账号可以设置不同目标。

任意用户模式按完整 nick 处理，不查询 QQ 成员或 IRC 在线名单，不建立跨平台身份绑定。`@nick` 前后需要空白、常见标点或正文边界；支持常规 IRC 昵称字符及 Unicode 字母。机器人提及通过 OneBot 原生 `at` 段与 `self_id` 判断；手打 `@机器人QQ号`、转义的 CQ 代码不会触发固定用户映射。

只转换正文；发送者前缀、引用摘要、媒体描述、URL 和邮箱保持原样。若 Tengo、`ReplaceMessages`、表情替换等处理已改变正文，本次转换会跳过，保留处理后的文字，避免将旧的提及位置用于新正文。发往其他协议的副本不受此 IRC 设置影响。

IRC 使用正文中的完整昵称触发客户端高亮，无需 IRCv3；实际通知由客户端设置决定。`ReverseMention = false` 只关闭桥接转换，不能禁止 IRC 客户端对原始文字自行高亮。修改配置后重启 matterbridge。

## Ergo RELAYMSG 虚拟昵称

使用上游已有的 `RELAYMSG`，一个机器人连接即可用多个虚拟昵称发言和发送动作。在目标 IRC 账号启用：

```toml
[irc.local]
UseRelayMsg = true
RemoteNickFormat = "{NICK}-{USERID}/{PROTOCOL}"
ReverseMention = true
BotMentionTarget = "alice" # 可选，沿用 QQ 原生 @机器人 的固定目标
```

Ergo 默认启用 `server.relaymsg.enabled` 和 `available-to-chanops`，由频道管理员执行
`/MODE #频道 +o 机器人昵称` 即可授权；也可给机器人具有 `relaymsg` 能力的 IRC oper 身份。
matterbridge 的 `UseRelayMsg` 默认关闭，需要显式启用。虚拟昵称必须包含服务器保留的
分隔符，Ergo 默认为 `/`；桥接会自动检测并在格式缺少分隔符时补上，建议在格式中明确指定。

例如在支持 Unicode 昵称的 Ergo 上，QQ 用户「张三」（QQ 号 123456）发言后，IRC 显示发送者 `张三-123456/onebot`：

| IRC 写法 | 发往来源 QQ 群的内容 |
| --- | --- |
| `@张三-123456/onebot 你好` | 原生 @123456 加上 ` 你好` |
| `张三-123456/onebot: 你好`（消息开头） | 原生 @123456 加上 `: 你好` |

昵称映射来自实际转发的 QQ 消息，支持格式化昵称及空格替换后的昵称；包含
`{USERID}` 可以区分重名用户。ASCII 模式的 IRC 服务器还会替换昵称中的中文，
可用 `RemoteNickFormat="qq-{USERID}/{PROTOCOL}"` 获得稳定昵称。映射按 IRC 频道及来源 OneBot 账号、QQ 群隔离，
最多缓存 4096 个昵称，重启清空。同名冲突、未见过的昵称保持文字；虚拟昵称
映射只在来源 QQ 群生成提及，并遵守 `AllowMention`。原有 `@QQ号`、`@群名片`
和 QQ → IRC 的 mention 设置继续有效。链接、CQ 代码、引用和附件说明不参与映射。

RELAYMSG 的自身回显沿用上游过滤逻辑，避免循环转发。开启 `PreserveThreading` 时支持
IRCv3 原生回复。RELAYMSG 长消息由桥接按 UTF-8 边界拆分，即使设置了 `MessageSplit=false`，
因为 IRC 库只能拆分普通 PRIVMSG/NOTICE。

## 验证与维护

在仓库根目录运行：

```sh
go test -race ./bridge/onebot/... ./bridge/irc
go test -tags goolm ./...
go test -tags goolm,noonebot ./gateway/bridgemap
go vet -tags goolm ./...
bash bridge/onebot/test-e2e.sh
```

端到端测试统一使用 **Ergo 2.19.1**。脚本会下载并校验固定版本的发行包，也可指定已有二进制：

```sh
E2E_ERGO=/absolute/path/to/ergo bash bridge/onebot/test-e2e.sh
```

每个用例根据 `ergo defaultconfig` 生成独立配置，在回环地址的临时端口启动服务，使用临时数据库；退出时关闭进程。RELAYMSG 测试沿用 Ergo 默认开关并给机器人频道 `+o`，分别覆盖 ASCII 与 PRECIS Unicode 昵称，不依赖已有 IRC 实例。无需 Python；下载需要 curl、tar 和 sha256sum（macOS 可用 shasum），Go 竞态检测需要 C 编译环境。

测试构建仓库根目录的实际 matterbridge 程序，检查 IRC/API ↔ QQ、群间互通、方向、网关隔离、多网关订阅、去重、自身过滤、重连、重启及 SIGTERM 退出。原生回复在普通 PRIVMSG、RELAYMSG 和两种 `MessageSplit` 设置下验证，包含回复桥接副本、拆分片段的 ID 映射、未知父消息回退。另有关闭原生回复时的文字引用，以及 RELAYMSG 动作、昵称提及、UTF-8 拆分、自身回显过滤测试。QQ 端使用 OneBot 11 模拟服务，真实 NapCat/QQ 账号仍需部署联调。

下载及构建缓存放在 `bridge/onebot/.cache/e2e`，可用 `E2E_CACHE_DIR` 覆盖。默认对测试驱动启用 `-race`，`E2E_RACE=1` 会同时检测整个程序。完整程序竞态检查可能报告上游 IRC 适配器已有的竞态。

合并转发测试覆盖官方/NapCat 返回格式、嵌套 API 引用与内联内容、多人重播、改名、PART/QUIT 清理、bot 被 KICK 后不重建，以及无人进入时超时清理。设置 `E2E_LIVE_FORWARD_FILE=/path/to/response-data.json` 可把真实 `get_forward_msg` 响应的 `data` 对象送入本地 Ergo 测试，并检查内联嵌套各层的正文；测试不会向真实 QQ 发消息。

协议代码集中在 `bridge/onebot/`，注册入口为 `gateway/bridgemap/bonebot.go`；WebSocket 客户端维持当前独立实现，无须重写公共桥接接口。新增消息元数据类型在 `bridge/config/message.go`，上游 `Message` 仅增加所需字段。引用映射与回退集中在 `gateway/replies.go`，保留上游已有的其他协议线程处理：新增映射必须按账号、频道和消息 ID 隔离，并能从桥接副本找回原消息，不能直接复用上游仅按协议和 ID 索引的缓存。IRC 的回复、提及、昵称映射和拆分逻辑分别在独立文件中，复用上游 RELAYMSG 发送逻辑，避免继续扩大 `irc.go` 与 `gateway.go` 的改动范围。
