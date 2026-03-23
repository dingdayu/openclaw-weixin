# `@tencent-weixin/openclaw-weixin` 通信协议逆向说明

> 结论来源：我直接分析了 `@tencent-weixin/openclaw-weixin@1.0.2` npm 包中的源码，重点包括 `src/api/api.ts`、`src/api/types.ts`、`src/auth/login-qr.ts`、`src/monitor/monitor.ts`、`src/messaging/send.ts`、`src/cdn/upload.ts`、`src/media/media-download.ts` 等文件。

本文把这个插件与微信 iLink Bot 服务之间的协议拆成 5 部分：

1. 二维码登录协议
2. 长轮询收消息协议
3. 文本/媒体发送协议
4. CDN 媒体上传下载协议
5. 会话约束、上下文 token、错误码与状态机

---

## 1. 总体架构

该插件并不是直接和微信客户端私有协议通信，而是通过 **HTTPS + JSON** 调用一组 `ilink/bot/*` 接口；媒体内容则走 **微信 CDN 二进制上传/下载**。

### 1.1 服务端基地址

默认 API 基地址：

- `https://ilinkai.weixin.qq.com`

默认 CDN 基地址：

- `https://novac2c.cdn.weixin.qq.com/c2c`

### 1.2 API 风格

- 绝大部分 CGI 接口是 **HTTP POST + JSON body**。
- 二维码登录接口是 **HTTP GET + query string**。
- CDN 上传是 **HTTP POST + application/octet-stream**。
- CDN 下载是 **HTTP GET**。

### 1.3 关键 endpoint

#### 登录相关
- `GET /ilink/bot/get_bot_qrcode?bot_type=3`
- `GET /ilink/bot/get_qrcode_status?qrcode=<qrcode>`

#### 业务 API
- `POST /ilink/bot/getupdates`
- `POST /ilink/bot/getuploadurl`
- `POST /ilink/bot/sendmessage`
- `POST /ilink/bot/getconfig`
- `POST /ilink/bot/sendtyping`

#### CDN
- `POST {cdnBaseUrl}/upload?encrypted_query_param=<upload_param>&filekey=<filekey>`
- `GET  {cdnBaseUrl}/download?encrypted_query_param=<download_param>`

---

## 2. 通用请求头与基础字段

插件对 `getupdates` / `sendmessage` / `getuploadurl` / `getconfig` / `sendtyping` 统一使用如下请求头：

```http
Content-Type: application/json
AuthorizationType: ilink_bot_token
Content-Length: <byte length>
X-WECHAT-UIN: <base64(decimal-random-uint32)>
Authorization: Bearer <bot_token>   # token 存在时才带
SKRouteTag: <routeTag>              # 配置存在时才带
```

### 2.1 `X-WECHAT-UIN` 生成规则

源码里不是固定值，而是：

1. 生成 4 字节随机数
2. 读成 `uint32`
3. 转十进制字符串
4. 再做 base64 编码

因此它更像一个伪随机请求标识头，而不是稳定的账号标识。

### 2.2 `base_info`

插件会给每个 POST CGI 请求附带统一字段：

```json
{
  "base_info": {
    "channel_version": "1.0.2"
  }
}
```

`channel_version` 取自包自己的 `package.json` 版本号。

---

## 3. 二维码登录协议

登录流程分两步：

1. 获取二维码
2. 轮询二维码状态直到拿到 bot token

### 3.1 获取二维码

请求：

```http
GET /ilink/bot/get_bot_qrcode?bot_type=3
```

可选请求头：

```http
SKRouteTag: <routeTag>
```

源码中 `bot_type` 默认固定为字符串 `"3"`。

#### 响应结构

```json
{
  "qrcode": "server-side-qrcode-id",
  "qrcode_img_content": "https://... 或 data/url 形式的二维码内容"
}
```

其中：

- `qrcode`：后续查询状态要用的二维码 ID
- `qrcode_img_content`：展示给用户扫码的二维码链接/内容

### 3.2 查询二维码状态

请求：

```http
GET /ilink/bot/get_qrcode_status?qrcode=<qrcode>
```

请求头：

```http
iLink-App-ClientVersion: 1
SKRouteTag: <routeTag>   # 可选
```

客户端侧为该请求设置 **35 秒超时**，如果超时，插件把它视为“正常无变化”，并返回本地状态 `wait` 继续轮询。

#### 响应结构

```json
{
  "status": "wait | scaned | confirmed | expired",
  "bot_token": "...",
  "ilink_bot_id": "xxxx@im.bot",
  "baseurl": "https://...",
  "ilink_user_id": "xxxx@im.wechat"
}
```

#### 状态机

- `wait`：未扫码，继续轮询
- `scaned`：已扫码，用户还未确认
- `confirmed`：已确认，登录成功
- `expired`：二维码过期，需要重新拉取二维码

#### 登录成功时关键字段

当状态为 `confirmed` 时：

- `bot_token`：之后所有业务接口 Bearer Token
- `ilink_bot_id`：机器人账号 ID，格式通常是 `xxx@im.bot`
- `ilink_user_id`：扫码用户 ID，格式通常是 `xxx@im.wechat`
- `baseurl`：服务端可能下发新的 API base URL

### 3.3 插件登录后的本地动作

插件在成功后会：

1. 将 `ilink_bot_id` 归一化后作为 accountId
2. 保存 `bot_token`
3. 保存 `baseurl`
4. 保存最近一次扫码用户 `ilink_user_id`
5. 将 accountId 写入账户索引

---

## 4. 长轮询收消息协议 `getupdates`

这是插件最核心的上游入站协议。

### 4.1 请求

```http
POST /ilink/bot/getupdates
Content-Type: application/json
AuthorizationType: ilink_bot_token
Authorization: Bearer <bot_token>
```

请求体：

```json
{
  "get_updates_buf": "<opaque cursor>",
  "base_info": {
    "channel_version": "1.0.2"
  }
}
```

### 4.2 `get_updates_buf` 的意义

这是一个 **服务端游标 / 上下文快照**，插件把它当作 opaque string：

- 第一次请求时为空字符串 `""`
- 收到响应后，如果响应里带新值，就持久化到本地
- 下一次原样回传

它取代了传统 offset/seq 机制，是插件恢复上下文和避免漏消息的关键。

### 4.3 超时行为

客户端默认超时 **35 秒**。如果客户端自己超时（Abort），插件不会当成错误，而是构造一个本地空响应：

```json
{
  "ret": 0,
  "msgs": [],
  "get_updates_buf": "<old buf>"
}
```

也就是说：**没有消息 + 长轮询超时 = 正常情况，立即重试即可**。

### 4.4 响应结构

```json
{
  "ret": 0,
  "errcode": 0,
  "errmsg": "",
  "msgs": [ ...WeixinMessage... ],
  "get_updates_buf": "<new opaque cursor>",
  "longpolling_timeout_ms": 35000
}
```

字段说明：

- `ret`：返回码，0 表示成功
- `errcode`：错误码；有些失败会同时通过它表达
- `errmsg`：错误说明
- `msgs`：消息数组
- `get_updates_buf`：下一次轮询要回传的新游标
- `longpolling_timeout_ms`：服务端建议的下次 long poll 超时时间

### 4.5 特殊错误码 `-14`

插件把 `-14` 识别为 **session expired**：

- `SESSION_EXPIRED_ERRCODE = -14`
- 一旦收到，插件会把当前 account 暂停 1 小时
- 暂停期间所有 API 请求都会抛出 `session paused` 错误

说明该协议存在服务端会话有效期概念，而不是 token 永不过期。

---

## 5. 消息结构 `WeixinMessage`

插件把 `getupdates` 和 `sendmessage` 都统一到同一个消息结构 `WeixinMessage`。

### 5.1 顶层结构

```json
{
  "seq": 123,
  "message_id": 456,
  "from_user_id": "user@im.wechat",
  "to_user_id": "bot@im.bot",
  "client_id": "client-generated-id",
  "create_time_ms": 1710000000000,
  "update_time_ms": 1710000001000,
  "delete_time_ms": 0,
  "session_id": "session-id",
  "group_id": "",
  "message_type": 1,
  "message_state": 2,
  "item_list": [ ... ],
  "context_token": "opaque-context-token"
}
```

### 5.2 枚举值

#### `message_type`
- `0 = NONE`
- `1 = USER`
- `2 = BOT`

#### `message_state`
- `0 = NEW`
- `1 = GENERATING`
- `2 = FINISH`

#### `item.type`
- `1 = TEXT`
- `2 = IMAGE`
- `3 = VOICE`
- `4 = FILE`
- `5 = VIDEO`

### 5.3 `context_token`

这是整个协议里最重要的会话字段之一。

插件中的结论非常明确：

- `context_token` 由上游 `getupdates` 的消息下发
- 后续所有回给该用户的 `sendmessage` 都必须原样回填这个 token
- 如果没有这个 token，插件会直接拒绝发送

也就是说它不是“可选上下文”，而是 **回复链路的硬约束**。从行为上看，它更像“当前会话上下文 ticket / reply token”。

插件的缓存策略：

- key: `accountId:userId`
- value: 最新一条入站消息带来的 `context_token`

---

## 6. 文本发送协议 `sendmessage`

### 6.1 请求

```http
POST /ilink/bot/sendmessage
Content-Type: application/json
AuthorizationType: ilink_bot_token
Authorization: Bearer <bot_token>
```

请求体结构：

```json
{
  "msg": {
    "from_user_id": "",
    "to_user_id": "target@im.wechat",
    "client_id": "openclaw-weixin-xxxx",
    "message_type": 2,
    "message_state": 2,
    "item_list": [
      {
        "type": 1,
        "text_item": {
          "text": "hello"
        }
      }
    ],
    "context_token": "<must echo inbound token>"
  },
  "base_info": {
    "channel_version": "1.0.2"
  }
}
```

### 6.2 字段约束

- `from_user_id`：插件发送时固定为空串
- `to_user_id`：目标用户，一般是 `xxx@im.wechat`
- `client_id`：客户端生成的唯一 ID
- `message_type`：机器人发消息固定为 `2`
- `message_state`：发送完成态固定为 `2`
- `item_list`：单条或多条 `MessageItem`
- `context_token`：必须带

### 6.3 `client_id`

插件用本地随机 ID 作为客户端消息 ID，并把它作为返回给 OpenClaw 的 `messageId`。从插件实现看，服务端没有额外返回新的 message id，因此客户端侧需要自己维护去重/追踪。

---

## 7. 媒体发送协议：先取上传地址，再上传 CDN，再发消息引用

发送图片/视频/文件不是一步完成，而是 3 段式：

1. `getuploadurl`
2. 把文件二进制上传到 CDN
3. `sendmessage` 发送媒体引用

---

## 8. `getuploadurl` 协议

### 8.1 请求

```http
POST /ilink/bot/getuploadurl
```

请求体：

```json
{
  "filekey": "16-byte-random-hex",
  "media_type": 1,
  "to_user_id": "target@im.wechat",
  "rawsize": 12345,
  "rawfilemd5": "md5-of-plaintext",
  "filesize": 12352,
  "thumb_rawsize": 0,
  "thumb_rawfilemd5": "",
  "thumb_filesize": 0,
  "no_need_thumb": true,
  "aeskey": "16-byte-aes-key-hex",
  "base_info": {
    "channel_version": "1.0.2"
  }
}
```

### 8.2 `media_type`
- `1 = IMAGE`
- `2 = VIDEO`
- `3 = FILE`
- `4 = VOICE`

### 8.3 文件大小计算规则

插件在上传前对文件做 AES-128-ECB 加密，所以：

- `rawsize` = 明文大小
- `rawfilemd5` = 明文 MD5
- `filesize` = AES-128-ECB + PKCS7 padding 后密文大小

插件里的计算公式是：

```text
filesize = ceil((plaintextSize + 1) / 16) * 16
```

这与 PKCS7 padding 的 16 字节分组行为一致。

### 8.4 响应

```json
{
  "upload_param": "opaque-upload-param",
  "thumb_upload_param": "opaque-thumb-upload-param"
}
```

当前插件在普通图片/视频/文件发送路径里统一传 `no_need_thumb: true`，所以只关心 `upload_param`。

---

## 9. CDN 上传协议

### 9.1 上传 URL

插件构造上传 URL 的方式：

```text
{cdnBaseUrl}/upload?encrypted_query_param=<upload_param>&filekey=<filekey>
```

### 9.2 上传内容

请求：

```http
POST /upload?... 
Content-Type: application/octet-stream
```

body 为：

- 原始文件明文
- 使用 `aeskey` 做 **AES-128-ECB** 加密
- 保留 PKCS7 padding
- 直接发送密文二进制

### 9.3 成功响应

CDN 成功时，插件不解析 JSON，而是读取响应头：

```http
x-encrypted-param: <download_param>
```

这个 `download_param` 后续要放进媒体消息的 `media.encrypt_query_param` 里。

### 9.4 重试策略

插件重试上限为 3 次：

- `4xx`：立即失败，不重试
- 非 `200` 的其他错误：最多重试 3 次
- 如果响应没有 `x-encrypted-param`：视为失败

---

## 10. 媒体消息如何通过 `sendmessage` 表达

### 10.1 图片

```json
{
  "msg": {
    "to_user_id": "target@im.wechat",
    "client_id": "...",
    "message_type": 2,
    "message_state": 2,
    "context_token": "...",
    "item_list": [
      {
        "type": 2,
        "image_item": {
          "media": {
            "encrypt_query_param": "<download_param>",
            "aes_key": "<base64(raw 16-byte key)>",
            "encrypt_type": 1
          },
          "mid_size": 12352
        }
      }
    ]
  }
}
```

注意：

- `aes_key` 这里不是 hex，而是 **base64(raw 16-byte key)**
- `mid_size` 使用密文大小

### 10.2 视频

```json
{
  "type": 5,
  "video_item": {
    "media": {
      "encrypt_query_param": "<download_param>",
      "aes_key": "<base64(raw 16-byte key)>",
      "encrypt_type": 1
    },
    "video_size": 12352
  }
}
```

### 10.3 文件

```json
{
  "type": 4,
  "file_item": {
    "media": {
      "encrypt_query_param": "<download_param>",
      "aes_key": "<base64(raw 16-byte key)>",
      "encrypt_type": 1
    },
    "file_name": "report.pdf",
    "len": "12345"
  }
}
```

这里 `len` 是 **明文大小字符串**，不是数字。

### 10.4 文本 + 媒体的发送方式

插件没有把文案和媒体放在一个 `item_list` 一次发出去，而是：

1. 若有文本 caption，先发一个 TEXT item
2. 再发一个 IMAGE / VIDEO / FILE item

也就是说媒体发送在协议层是 **两次 `sendmessage`**。

---

## 11. CDN 下载与媒体解密协议

收到媒体消息时，`item_list` 里会带 `encrypt_query_param` 和 `aes_key`，插件下载方式为：

### 11.1 下载 URL

```text
{cdnBaseUrl}/download?encrypted_query_param=<encrypt_query_param>
```

### 11.2 图片 / 文件 / 视频 / 语音的解密方式

下载后对密文做 **AES-128-ECB 解密**。

### 11.3 `aes_key` 的两种编码形式

源码显示服务端返回的 `CDNMedia.aes_key` 存在两种编码：

1. `base64(raw 16 bytes)`
2. `base64(hex-string-of-16-bytes)`，即 base64 解码后得到 32 个 ASCII hex 字符，再转成 16 字节 key

插件的兼容逻辑：

- 解码后长度是 16：直接当 key 用
- 解码后长度是 32 且内容是 hex：再 hex decode 一次
- 否则报错

### 11.4 图片特殊兼容

对于图片，插件优先使用：

- `image_item.aeskey`（hex）

如果没有，再回退到：

- `image_item.media.aes_key`

---

## 12. 获取 typing ticket 与发送输入态

### 12.1 `getconfig`

请求：

```json
{
  "ilink_user_id": "target@im.wechat",
  "context_token": "...",
  "base_info": {
    "channel_version": "1.0.2"
  }
}
```

响应：

```json
{
  "ret": 0,
  "errmsg": "",
  "typing_ticket": "base64-encoded-ticket"
}
```

插件会把 `typing_ticket` 按用户缓存，最长 24 小时，并做指数退避刷新。

### 12.2 `sendtyping`

请求：

```json
{
  "ilink_user_id": "target@im.wechat",
  "typing_ticket": "...",
  "status": 1,
  "base_info": {
    "channel_version": "1.0.2"
  }
}
```

枚举值：

- `1 = TYPING`
- `2 = CANCEL`

该插件会在 AI 回复生成期间每 5 秒续发一次 typing，并在结束时发送 cancel。

---

## 13. 入站消息到出站回复的完整时序

下面是插件真实实现对应的完整过程。

### 13.1 登录阶段

1. 客户端调用 `get_bot_qrcode`
2. 用户扫码
3. 客户端循环调用 `get_qrcode_status`
4. 收到 `confirmed`
5. 保存 `bot_token` / `ilink_bot_id` / `baseurl`

### 13.2 启动监听阶段

1. 读取本地持久化的 `get_updates_buf`
2. 调用 `getupdates`
3. 若返回新的 `get_updates_buf`，立即覆盖保存
4. 遍历 `msgs`

### 13.3 收到一条消息后

1. 取 `from_user_id`
2. 取 `context_token`
3. 调用 `getconfig` 获取/刷新 typing ticket
4. 如果消息里有媒体，走 CDN 下载 + AES-ECB 解密
5. 把 `context_token` 缓存到 `(accountId, from_user_id)`
6. 进入上层业务逻辑

### 13.4 回复文本

1. 从缓存取 `(accountId, userId)` 对应的 `context_token`
2. 构造 `sendmessage`
3. `item_list` 放一条 TEXT item
4. 发送成功

### 13.5 回复媒体

1. 本地文件计算 MD5/大小/AES key
2. `getuploadurl`
3. 用 AES-128-ECB 加密明文
4. 上传到 CDN
5. 从 CDN 响应头取 `x-encrypted-param`
6. 构造 IMAGE/VIDEO/FILE item
7. `sendmessage`
8. 如带 caption，则先单独发文本，再发媒体

---

## 14. 插件推断出的协议约束与注意事项

### 14.1 `context_token` 基本可视作必填

虽然类型定义里它看起来是可选字段，但插件发送消息时明确要求：

- 没有 `context_token` 就拒绝发送

这说明服务端至少在当前实现下，把它作为回复上下文绑定字段。

### 14.2 `get_updates_buf` 必须持久化

如果不持久化，重启后大概率会：

- 丢失会话上下文
- 重复消费消息
- 破坏 long-poll 状态恢复

### 14.3 媒体消息中的 `aes_key` 编码不统一

尤其在不同媒体类型、不同上游实现里，`aes_key` 可能有 raw-base64 和 hex-string-base64 两种形式，客户端必须兼容。

### 14.4 `sendmessage` 里媒体引用发送的是“CDN 凭证”，不是外链 URL

也就是说不能直接把一个公网图片 URL 放到 `image_item.url` 就期待服务端代拉。正确流程一定是：

- 先上传 CDN
- 再发送 CDN 引用

### 14.5 session 过期错误码是 `-14`

出现后应进入冷却或重新登录逻辑，而不是无脑重试。

---

## 15. 最小可实现客户端清单

如果你要自己完整实现一个兼容客户端，最低需要支持：

1. `get_bot_qrcode`
2. `get_qrcode_status`
3. `getupdates` + `get_updates_buf` 持久化
4. `sendmessage` 文本发送
5. `getuploadurl`
6. CDN AES-128-ECB 上传
7. CDN 下载 + AES-128-ECB 解密
8. `getconfig` + `sendtyping`
9. `context_token` 缓存与回填
10. `-14` session 过期处理

---

## 16. 本仓库附带的示例

为了方便直接验证，我另外实现了两份完整示例：

- Go：`examples/go/main.go`
- TypeScript：`examples/ts/weixin-protocol-example.ts`

两份示例都包含：

- 二维码登录
- 长轮询收消息
- 文本发送
- 图片上传与发送
- CDN 下载与解密
- typing 指示
- `context_token` 与 `get_updates_buf` 本地持久化

