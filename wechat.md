# 从 `@tencent-weixin/openclaw-weixin` 看微信 Bot 接入：一份面向开发者与产品人的协议解析

如果你最近关注过 OpenClaw 生态，可能已经注意到一个很有意思的变化：微信正在通过插件的方式，把 Bot 能力接入到 OpenClaw 这类智能代理框架中。

而这篇文章要讨论的主角，就是 `@tencent-weixin/openclaw-weixin`。

更准确地说，这个包并不是一个“民间逆向项目”，而是来自微信官方发布链路中的一部分：**微信官方发布了 `@tencent-weixin/openclaw-weixin-cli`，这个 CLI 的作用是安装并启用 `@tencent-weixin/openclaw-weixin` 插件，从而让 OpenClaw 获得微信渠道能力。** 换句话说，`@tencent-weixin/openclaw-weixin` 是微信官方通过插件方式为 OpenClaw 提供的第三方包，而 `@tencent-weixin/openclaw-weixin-cli` 则是它的安装入口。

结合官方 npm 包、插件 README，以及我们对源码的梳理，可以看到：这套能力并不是直接暴露“微信底层私有协议”，而是抽象成了一组更适合工程接入的 HTTP JSON API，加上配套的 CDN 媒体上传下载机制。对于开发者来说，这意味着两件事：

1. 它已经不是传统意义上的“自己从零摸索微信消息协议”；
2. 它更像是一套“面向 OpenClaw 生态开放的微信 Bot 接入层”。

本文将围绕 `openclaw-weixin` 协议本身展开，尝试回答三个问题：

- 这个包到底来自哪里？
- 它是如何把微信能力暴露给 OpenClaw 的？
- 如果你想做兼容实现，真正需要理解哪些关键环节？

---

## 一、这个包的来历：不是野生适配，而是官方插件化接入

先说结论：`@tencent-weixin/openclaw-weixin` 的定位，不是“社区里某个开发者写的适配层”，而是**微信官方发布的 OpenClaw 渠道插件**。

从安装链路就能看出来：

- `@tencent-weixin/openclaw-weixin-cli` 的描述是“Lightweight installer for the OpenClaw Weixin channel plugin”；
- 这个 CLI 的核心行为，是调用 `openclaw plugins install "@tencent-weixin/openclaw-weixin"`；
- 安装完成后，它进一步引导用户执行 `openclaw channels login --channel openclaw-weixin` 完成扫码登录。

也就是说，微信官方并不是直接给出一个“万能微信 SDK”，而是把它包装成了 **OpenClaw 的渠道插件**：

- 对 OpenClaw 来说，它是一个标准的 channel plugin；
- 对开发者来说，它是一条进入微信 Bot 能力的正式入口；
- 对产品团队来说，它意味着“把微信接入智能代理系统”这件事，已经从实验阶段，走向了更产品化、更工程化的阶段。

如果结合你提供的两篇背景材料来看，也可以把这件事理解为：**微信正在把自己的 Bot 能力，以插件化、平台化的方式接入外部智能代理框架。** 这和过去那种“围绕微信做消息自动化”的灰色或半灰色探索，性质已经非常不一样了。

---

## 二、为什么这个协议值得认真看？

很多人看到“微信 Bot”四个字，第一反应往往是：

- 能不能自动回复？
- 能不能发图片、文件？
- 能不能接入 AI 助手？

这些当然都重要，但从工程视角看，更值得关注的是：**它定义了一套明确的会话模型和消息传输模型。**

`openclaw-weixin` 协议最有价值的地方，不在于“能发消息”本身，而在于它把微信这条渠道抽象成了一个稳定的、可以被 OpenClaw 消费的协议层。这个协议层至少解决了以下问题：

1. **身份建立**：通过二维码完成账号授权；
2. **消息接收**：通过长轮询获取入站消息；
3. **上下文绑定**：通过 `context_token` 保证回复能落在正确的上下文里；
4. **状态续传**：通过 `get_updates_buf` 持续同步，不依赖简单 offset；
5. **媒体传输**：通过 `getuploadurl + CDN + AES-128-ECB` 完成图片、视频、文件传输；
6. **交互细节**：通过 typing ticket 与 `sendtyping` 补足“正在输入中”这类产品体验。

如果说传统“机器人接口”强调的是收发文本，那么 `openclaw-weixin` 更像是在定义一个**完整的会话通道协议**。

---

## 三、整体架构：业务 API 走 HTTP JSON，媒体走 CDN

从协议结构上看，这套实现非常清晰，可以概括成两条链路：

### 1. 控制链路：HTTP JSON API

插件通过一组 `ilink/bot/*` 接口与后端通信，核心接口包括：

- `get_bot_qrcode`：获取登录二维码；
- `get_qrcode_status`：查询二维码状态；
- `getupdates`：长轮询收消息；
- `sendmessage`：发送文本或媒体引用；
- `getuploadurl`：为媒体上传换取预签名参数；
- `getconfig`：获取 typing ticket 等配置；
- `sendtyping`：发送输入中 / 取消输入状态。

这些接口绝大多数都是 **HTTP POST + JSON body**，请求头里会带上：

- `AuthorizationType: ilink_bot_token`
- `Authorization: Bearer <bot_token>`
- `X-WECHAT-UIN`

其中 `X-WECHAT-UIN` 并不是固定账号，而是客户端生成的一个随机 uint32 再做 base64 编码，更像是请求级别的随机标识。

### 2. 媒体链路：CDN 上传下载

文本消息可以直接通过 `sendmessage` 发送，但图片、文件、视频不是直接把二进制塞进接口，而是走另一条链路：

1. 先调用 `getuploadurl`；
2. 再把文件加密后上传到 CDN；
3. 最后通过 `sendmessage` 发送一个媒体引用。

因此，从协议设计来看，`sendmessage` 发的并不是“文件本体”，而是一个 **CDN 媒体描述符**。

这是一种非常典型的平台化设计：

- 控制面与数据面分离；
- API 负责业务语义；
- CDN 负责大文件传输；
- 客户端负责本地加密与引用封装。

---

## 四、登录流程：二维码不是附属功能，而是整套通道的起点

`openclaw-weixin` 的登录流程相当直接，但里面包含了几个重要信号。

### 第一步：获取二维码

客户端调用：

```text
GET /ilink/bot/get_bot_qrcode?bot_type=3
```

返回值里最关键的是两个字段：

- `qrcode`：服务端生成的二维码标识；
- `qrcode_img_content`：用于展示扫码内容的链接或内容。

### 第二步：轮询二维码状态

客户端持续调用：

```text
GET /ilink/bot/get_qrcode_status?qrcode=<qrcode>
```

服务端状态包括：

- `wait`：等待扫码；
- `scaned`：已扫码，但手机端还未确认；
- `confirmed`：确认完成；
- `expired`：二维码过期，需要重新获取。

### 第三步：拿到 Bot 身份

当状态进入 `confirmed` 时，服务端会下发：

- `bot_token`
- `ilink_bot_id`
- `ilink_user_id`
- `baseurl`

这几个字段共同定义了后续通信身份：

- `bot_token` 是所有业务请求的 Bearer Token；
- `ilink_bot_id` 是机器人账号标识；
- `ilink_user_id` 代表扫码用户；
- `baseurl` 则说明服务端甚至允许在登录后动态切换 API 基地址。

从产品角度看，这个登录方式非常符合微信生态的安全哲学：**授权从扫码开始，而不是直接发放长期静态密钥。**

---

## 五、收消息：`getupdates` 不是简单轮询，而是“带状态”的长轮询

要理解 `openclaw-weixin` 协议，最核心的接口其实不是 `sendmessage`，而是 `getupdates`。

它的请求体很简单：

```json
{
  "get_updates_buf": "...",
  "base_info": {
    "channel_version": "1.0.2"
  }
}
```

但这里隐藏着两个非常关键的设计。

### 1. `get_updates_buf`：状态续传的关键

它不是简单的“偏移量”或“最后一条消息 ID”，而是一个 **opaque cursor / 同步上下文**。

客户端只需要做两件事：

- 第一次没有值时传空字符串；
- 服务端返回新值后，本地持久化，下次原样带回。

这意味着服务端把更多的同步状态封装进了这个字段里。对客户端来说，最重要的不是理解它的内部编码，而是**绝不能丢**。

如果你不持久化 `get_updates_buf`，就很可能出现：

- 消息重复消费；
- 会话上下文断裂；
- 重启后同步异常。

### 2. 长轮询超时是一种“正常返回”

客户端默认会给 `getupdates` 设置大约 35 秒超时。如果超时了，并不代表接口异常，而是意味着：

> 当前没有新消息。

也就是说，`getupdates` 的正确消费方式不是“失败重试”，而是“超时即重试”。

这使它更像一个消息通道，而不是普通查询接口。

---

## 六、回复为什么离不开 `context_token`？

在所有协议细节里，`context_token` 可能是最值得强调的字段。

很多人第一次看这个字段，会把它理解成“可选上下文参数”。但从插件实现来看，它几乎是**必填**的：

- 入站消息由 `getupdates` 下发 `context_token`；
- 回复时，客户端必须把它原样回填到 `sendmessage`；
- 如果没有这个 token，插件会直接拒绝发送。

这说明什么？

说明微信这套 Bot 通道，并不是一个“只认用户 ID”的简单消息接口，而是一个**显式依赖上下文票据的会话系统**。

换句话说，`to_user_id` 只说明“发给谁”，而 `context_token` 说明“这条回复属于哪段上下文”。

这件事对于做 AI 产品的人非常重要。

因为它意味着：

- 同一个用户的不同消息，可能需要被绑定到不同会话语义；
- 回复时不能只靠 `user_id` 定位；
- 如果你的应用层想做转发、延迟发送、工作流处理，就必须考虑 `context_token` 的生命周期与缓存策略。

从工程实现上，最稳妥的做法就是像插件一样：

- 用 `(accountId, userId)` 作为 key；
- 缓存最近一次入站消息携带的 `context_token`；
- 回复时优先取缓存值。

---

## 七、发送文本消息：表面简单，实则受上下文严格约束

文本发送接口是：

```text
POST /ilink/bot/sendmessage
```

典型请求体包含：

- `to_user_id`
- `client_id`
- `message_type = 2`
- `message_state = 2`
- `item_list`
- `context_token`

其中 `item_list` 是统一消息结构，文本场景下一般放一个 `TEXT` item。

这里有两个值得特别注意的点：

### 1. 发送方 ID 可以为空

插件在发送时，`from_user_id` 实际上填的是空字符串。这说明发送身份不是通过 body 里的 `from_user_id` 表达的，而是通过认证态与 token 确定的。

### 2. `client_id` 很重要

服务端没有额外返回一个新的下行消息 ID，因此插件本地会生成 `client_id`，并将它作为可追踪的消息 ID 使用。

这其实也提示了一个工程实践：

> 如果你自己实现客户端，最好不要把消息去重、审计、链路追踪完全寄托在服务端返回值上。

---

## 八、媒体消息：真正的难点不在“发送”，而在“上传 + 引用 + 解密”

对大多数开发者来说，`openclaw-weixin` 协议最容易踩坑的部分，就是媒体。

因为媒体发送并不是一个接口，而是一条链路。

### 第一步：为上传申请参数

客户端先调用 `getuploadurl`，提交：

- 文件明文大小 `rawsize`
- 明文 MD5 `rawfilemd5`
- 密文大小 `filesize`
- 媒体类型 `media_type`
- AES key（hex）

服务端返回：

- `upload_param`
- `thumb_upload_param`

通常图片、视频、文件发送流程只要 `upload_param`。

### 第二步：本地加密后上传 CDN

这里是这套协议最有技术味的部分之一：

- 文件明文先在客户端本地做 **AES-128-ECB** 加密；
- 使用 PKCS7 padding；
- 然后把密文二进制上传到 CDN；
- 成功后，CDN 通过响应头 `x-encrypted-param` 返回后续下载所需参数。

这意味着媒体安全不是“上传后服务器帮你加密”，而是**上传前客户端自己负责加密**。

### 第三步：通过 `sendmessage` 发送媒体引用

上传完成后，客户端再发送一个 `IMAGE` / `VIDEO` / `FILE` item，其中最关键的字段是：

- `media.encrypt_query_param`
- `media.aes_key`
- 文件大小字段（如 `mid_size`、`video_size`、`len`）

也就是说，消息里传递的不是媒体本体，而是一个“如何取回该媒体”的引用描述。

---

## 九、为什么说它更像“工程协议”而不是“业务 API”？

当你把登录、收消息、发文本、发媒体几个部分串起来看，会发现 `openclaw-weixin` 协议体现出了非常明显的工程化特征。

### 1. 会话强绑定

`context_token` 强制要求客户端维护上下文，避免“只按 user_id 回消息”的粗糙实现。

### 2. 状态可恢复

`get_updates_buf` 让客户端可以在重启后继续同步，而不是从头开始。

### 3. 数据面与控制面分离

控制指令走 API，文件内容走 CDN，符合大规模消息系统的一般设计思路。

### 4. 客户端承担部分协议责任

例如：

- 计算明文 MD5；
- 计算密文大小；
- 本地 AES 加密；
- 持久化同步状态；
- 缓存上下文 token。

这说明客户端不是一个“薄壳前端”，而是协议执行的重要一环。

---

## 十、对产品、开发与生态意味着什么？

这部分内容，或许比接口细节更值得关注。

### 对产品经理来说

这意味着“微信接 AI”正在从 demo 走向产品形态。

过去很多人讨论的是“能不能把大模型接进微信”。现在更实际的问题变成了：

- 如何管理多账号？
- 如何保持会话上下文？
- 如何传图片、文件、语音？
- 如何让交互体验更像真人，比如 typing 状态？

`openclaw-weixin` 协议本身已经给出了这些问题的工程答案。

### 对开发者来说

这套协议最有价值的地方，是它让你知道一个兼容实现真正需要做什么，而不是只盯着“收一条文本、发一条文本”。

如果你要做自研服务端、自定义客户端或调试工具，最少要支持：

- 二维码登录流程；
- `getupdates` 长轮询；
- `get_updates_buf` 持久化；
- `context_token` 缓存与回填；
- `getuploadurl` + CDN 上传；
- CDN 下载 + AES-128-ECB 解密；
- `getconfig` + `sendtyping`；
- `-14` session expired 处理。

### 对生态观察者来说

这也释放了一个非常明确的信号：

> 微信正在尝试以更正式、可管理、可工程化的方式，把 Bot 能力接入智能代理体系。

而 OpenClaw，正是这个阶段的一个重要接口层。

---

## 十一、如果你要自己实现兼容客户端，应该怎么开始？

最推荐的路径其实很简单：

1. 先读 `README.md`，搞清楚仓库结构；
2. 再读 `protocol.md`，建立整体协议模型；
3. 最后对照 Go 或 TypeScript 示例，把完整链路跑起来。

如果你只看文章，很容易觉得“原理懂了”；但一旦真正落地，最容易出问题的还是那几个点：

- 忘记保存 `get_updates_buf`；
- 没有处理 `context_token`；
- 直接把公网 URL 当图片消息发，而不是走 CDN 上传；
- 没有按要求做 AES-128-ECB 加密；
- 没有处理 session 过期错误码 `-14`。

从这个意义上说，`openclaw-weixin` 并不是一个“看完文档就能随便调通”的接口，而是一套需要认真尊重协议约束的系统。

---

## 十二、结语：真正值得关注的，不只是“能不能做微信 Bot”

很多技术人看到这类项目，第一反应往往是“终于能做微信机器人了”。

但如果只停留在这个层面，可能会错过它更重要的意义。

`@tencent-weixin/openclaw-weixin` 的价值，不只是提供了一个微信入口，更在于它展示了一种趋势：

- 微信能力正在被平台化；
- Bot 能力正在被插件化；
- 智能代理框架与即时通信渠道正在逐渐打通；
- 开发者终于可以围绕一套更正式、更可维护的协议来做系统集成。

而这，才是 `openclaw-weixin` 真正值得研究的地方。

如果你关心的不是“一个 demo 能不能跑”，而是“下一代 AI 助手如何真正进入高频沟通场景”，那么这套协议非常值得继续跟进。

---

## 十三、再往前看：围绕这个包已经出现了哪些开源项目？

如果把视角从协议本身再往外拉一步，会发现围绕 `@tencent-weixin/openclaw-weixin` 以及同一 ClawBot / iLink API 生态，已经开始出现一批值得关注的开源项目。

这里特别值得强调两点：

1. **有些项目是直接基于 `@tencent-weixin/openclaw-weixin` 或 `@tencent-weixin/openclaw-weixin-cli` 来做集成；**
2. **也有一些项目不一定直接依赖这个 npm 包，但明显建立在同一套 WeChat ClawBot / iLink API 能力之上。**

这说明 `openclaw-weixin` 已经不只是一个“单独插件”，而是在形成一个可被复用、二次封装和场景化落地的小生态。

### 1. 直接围绕该包做集成的项目

以下信息为我在 **2026 年 3 月 23 日** 检索 GitHub 时看到的公开数据，star 数后续可能继续变化：

- **`Johnixr/claude-code-wechat-channel`（121 stars）**
  这是目前我看到热度相对更高、且方向也非常清晰的一个项目：它把微信消息桥接进 Claude Code 会话，项目描述里明确提到了基于官方 ClawBot iLink API。对很多关注“让编码 Agent 进入微信”的开发者来说，这是一个很有代表性的方向。

- **`fendouai/OpenCodeWeChat`（10 stars）**
  从项目命名和说明来看，它的目标非常直接：让 OpenCode 在微信里运行。这类项目的意义在于，它们不再把 `openclaw-weixin` 只当作协议研究对象，而是已经开始把它视为一个实际可落地的接入通道。

- **`SiverKing/weixin-ClawBot-API`（6 stars）**
  该项目在仓库说明中明确提到，是基于腾讯官方开放的 `tencent-weixin/openclaw-weixin` 来实现微信个人账号 Bot，并强调可接入任意 AI 模型。这类项目说明，社区已经开始沿着“官方通道 + 自定义模型接入”的方向做更轻量的封装。

- **`Wscats/wechat-claw`（4 stars）**
  这个项目直接写明“基于 `@tencent-weixin/openclaw-weixin-cli` 实现的 OpenClaw 微信集成项目”。虽然目前热度还不高，但它很有代表性：说明已经有人把官方安装链路直接当成基础设施，再往上叠加自己的集成逻辑。

- **`FFengIll/understand-tencent-weixin-openclaw-weixin`（4 stars）**
  从命名就能看出，这是一个偏研究和理解导向的仓库。它的存在本身就说明，围绕 `openclaw-weixin` 的协议分析、源码理解和二次整理，正在逐渐成为一个独立的小方向。

### 2. 更广义的相关生态项目

除了“直接基于这个包”的项目，我还看到了几类更广义的相关项目。它们未必直接依赖 `@tencent-weixin/openclaw-weixin`，但很明显与同一波微信 Agent / ClawBot 能力演化有关：

- **`fastclaw-ai/weclaw`（189 stars）**
  项目定位是“Connect to any agents with WeChat ClawBot”，更像是在做面向多种 Agent 的微信连接层。这类项目说明，ClawBot 能力正在从“插件”演化成“桥接层”。

- **`photon-hq/qclaw-wechat-client`（787 stars）**
  这是一个热度更高的 TypeScript 客户端项目，强调自己是针对 QClaw / WeChat Access API 的逆向客户端。它未必与 `openclaw-weixin` 完全等同，但反映出同类协议生态已经具备了更强的社区吸引力。

- **`hao-ji-xing/cc-weixin`（62 stars）** 与 **`qufei1993/cc-weixin`（20 stars）**
  这两个项目都体现出一个很明显的趋势：大家开始尝试把 Claude Code 或其他 AI coding agent，通过微信通道接入到实际工作流里。换句话说，微信不再只是聊天入口，也正在变成“Agent 运行界面”。

- **`ImGoodBai/WeClawBot-ex`（49 stars）**
  这个项目聚焦的是官方能力之外的扩展，比如多微信同时扫码、同时登录、同时对话，以及本地 Web 管理界面。这种项目非常值得关注，因为它往往能揭示真实用户在官方插件之外最强烈的需求是什么。

### 3. 这说明了什么？

如果把这些项目放在一起看，会发现一个很有意思的现象：

- 有人在做协议理解；
- 有人在做安装和接入简化；
- 有人在做 AI coding agent 场景；
- 有人在做多账号管理和增强能力；
- 还有人在把它继续抽象成更通用的桥接层。

这恰恰说明，`openclaw-weixin` 的价值并不只是一份“官方插件”，而是它正在成为一个新生态的技术底座之一。

如果未来微信继续沿着这条路线推进，那么我们很可能会看到更多这样的项目出现：

- 面向不同模型的接入层；
- 面向企业场景的多账号治理；
- 面向内容生产、客服、开发助手的垂直产品；
- 甚至面向更通用 Agent Runtime 的微信入口层。

从这个意义上说，研究 `openclaw-weixin`，既是在研究一个协议，也是在观察一个生态如何开始长出来。

---

## 参考与说明

- 本文基于仓库中的 `protocol.md` 与 `README.md` 整理撰写；
- `@tencent-weixin/openclaw-weixin` 与 `@tencent-weixin/openclaw-weixin-cli` 的关系，来自 npm 包元数据与 CLI 安装链路；
- 文中提到的 GitHub 项目与 star 数据，为 **2026 年 3 月 23 日** 的公开检索结果，后续可能变化；
- 关于该能力的背景上下文，可进一步参考你提供的两篇资料：
  - https://zhuanlan.zhihu.com/p/2019047101644949484
  - https://zhuanlan.zhihu.com/p/2019077006235554955
