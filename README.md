# openclaw-weixin

这个仓库现在包含三部分内容：

1. `protocol.md`：基于 `@tencent-weixin/openclaw-weixin` 包源码逆向整理出的协议说明。
2. `examples/go`：Go 版完整协议示例。
3. `examples/ts`：TypeScript 版完整协议示例。

## 包含内容

### 1. 协议文档

`protocol.md` 汇总了 `@tencent-weixin/openclaw-weixin` 插件与微信 iLink Bot 服务之间的主要通信流程，包括：

- 二维码登录：`get_bot_qrcode`、`get_qrcode_status`
- 长轮询收消息：`getupdates`
- 文本发送：`sendmessage`
- typing 状态：`getconfig`、`sendtyping`
- 媒体上传：`getuploadurl` + CDN `upload`
- 媒体下载：CDN `download` + AES-128-ECB 解密
- `context_token` 与 `get_updates_buf` 的语义与持久化要求
- `-14` session expired 错误处理

如果你要自己实现一个兼容客户端，建议先完整阅读 `protocol.md`。

### 2. Go 示例

Go 示例位于 `examples/go`，实现了一个可直接运行的 CLI，包含：

- 拉取二维码并等待扫码登录
- 持续 long-poll `getupdates`
- 本地保存 `get_updates_buf`
- 本地缓存 `context_token`
- 发送文本消息
- 上传图片并发送图片消息
- 获取 typing ticket 并发送输入中/取消输入状态
- 下载并解密 CDN 媒体

常用命令：

```bash
cd examples/go

go run . start-qr
go run . wait-qr <qrcode>
go run . poll
go run . send-text <to_user_id> <text>
go run . send-image <to_user_id> <file_path> [caption]
go run . typing-on <to_user_id>
go run . typing-off <to_user_id>
go run . download-image <encrypt_query_param> <aes_key_base64_or_hex>
```

### 3. TypeScript 示例

TypeScript 示例位于 `examples/ts`，实现内容与 Go 版本对应，同样覆盖：

- 二维码登录
- 长轮询收消息
- `get_updates_buf` / `context_token` 本地状态保存
- 文本发送
- 图片上传与发送
- typing 状态发送
- CDN 下载与 AES-128-ECB 解密

安装依赖并校验：

```bash
cd examples/ts
npm ci
npm run check
```

常用命令：

```bash
node weixin-protocol-example.ts start-qr
node weixin-protocol-example.ts wait-qr <qrcode>
node weixin-protocol-example.ts poll
node weixin-protocol-example.ts send-text <to_user_id> <text>
node weixin-protocol-example.ts send-image <to_user_id> <file_path> [caption]
node weixin-protocol-example.ts typing-on <to_user_id>
node weixin-protocol-example.ts typing-off <to_user_id>
node weixin-protocol-example.ts download-image <encrypt_query_param> <aes_key>
```

## 环境变量

Go 和 TypeScript 示例都支持通过环境变量覆盖默认配置：

- `WEIXIN_BASE_URL`：API 基地址，默认 `https://ilinkai.weixin.qq.com`
- `WEIXIN_CDN_BASE_URL`：CDN 基地址，默认 `https://novac2c.cdn.weixin.qq.com/c2c`
- `WEIXIN_BOT_TOKEN`：登录成功后拿到的 bot token
- `WEIXIN_ROUTE_TAG`：可选的路由标签
- `WEIXIN_BOT_TYPE`：二维码登录时使用的 bot type，默认 `3`
- `WEIXIN_STATE_FILE`：本地状态文件路径

## 推荐阅读顺序

如果你是第一次接触这个协议，推荐按下面顺序阅读：

1. 先看 `README.md` 了解仓库结构
2. 再看 `protocol.md` 理解协议本身
3. 最后参考 `examples/go/main.go` 或 `examples/ts/weixin-protocol-example.ts` 落地实现
