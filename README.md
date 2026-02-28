# screenshot-server

基于 **Go + Gin + chromedp(+cdproto)** 的网页截图服务，支持通过 HTTP API 截图，并连接外部 **browserless/Chrome DevTools** 执行浏览器操作。

## 功能特性

- 支持 `GET /screenshot` 与 `POST /screenshot`
- 支持 `png / jpeg / webp` 输出格式
- 支持全页截图、裁剪截图、自定义视口尺寸
- 支持等待选择器、额外等待时间
- 支持自定义 Header、User-Agent、移动端参数
- 提供 `GET /health` 健康检查接口

---

## 项目结构

```text
.
├── Dockerfile
├── docker-compose.yml
├── go.mod
├── main.go
├── requirements.md
└── screenshot-server
```

---

## 运行要求

- Go `1.23+`
- 可访问的 browserless（或其他提供 Chrome DevTools Protocol 的远程 Chrome）

> 本服务默认通过 `BROWSERLESS_HTTP_URL`（默认 `http://localhost:25004`）访问 `${BROWSERLESS_HTTP_URL}/json/version` 解析 `webSocketDebuggerUrl` 并连接。未配置/不可用时，`/health` 会返回降级状态，`/screenshot` 将不可用。

---

## 环境变量

| 变量名 | 必填 | 默认值 | 说明 |
|---|---|---|---|
| `PORT` | 否 | `8080` | HTTP 服务端口 |
| `BROWSERLESS_HTTP_URL` | 否（建议配置） | `http://localhost:25004` | browserless 的 HTTP 地址；程序会请求 `/json/version` 获取 `webSocketDebuggerUrl` |
| `CHROME_WS_ENDPOINT` | 否 | - | 直接指定 DevTools WS（优先级高于 `BROWSERLESS_HTTP_URL`） |

---

## 本地运行

```bash
go mod download
PORT=8080 BROWSERLESS_HTTP_URL=http://127.0.0.1:25004 go run .
```

服务启动后默认监听：`http://localhost:8080`

---

## Docker 运行

### 从 Docker Hub 拉取部署

```bash
docker pull xiaocaoooo/screenshot-server:latest

docker run -d --name screenshot-server \
	-p 8080:8080 \
	-e PORT=8080 \
	-e BROWSERLESS_HTTP_URL=http://host.docker.internal:25004 \
	--restart unless-stopped \
	xiaocaoooo/screenshot-server:latest
```

> 如需固定版本，请将 `latest` 替换为具体 tag。

### 使用 Docker Compose

```bash
docker compose up -d --build
```

当前 `docker-compose.yml` 已包含：

- 端口映射：`8080:8080`
- 环境变量：`PORT=8080`
- `BROWSERLESS_HTTP_URL=http://host.docker.internal:25004`（请按实际环境修改）

### 使用 Docker 单独运行

```bash
docker build -t screenshot-server:local .
docker run --rm -p 8080:8080 \
	-e PORT=8080 \
	-e BROWSERLESS_HTTP_URL=http://host.docker.internal:25004 \
	screenshot-server:local
```

---

## API

### 1) 健康检查

`GET /health`

示例返回：

```json
{
	"status": "ok",
	"time": "2026-02-27T00:00:00Z",
	"chrome_ws_available": true
}
```

当 `BROWSERLESS_HTTP_URL` / `CHROME_WS_ENDPOINT` 未配置或不可用时：

- HTTP 状态码为 `503`
- `status` 为 `degraded`

---

### 2) 截图接口

- `GET /screenshot`
- `POST /screenshot`

成功时直接返回图片二进制，`Content-Type` 根据 `format` 设置为：

- `image/png`
- `image/jpeg`
- `image/webp`

#### 参数说明

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `url` | string | 必填 | 目标网页 URL（仅支持 `http/https`） |
| `width` | int | 1920 | 视口宽度，范围 `100-4096` |
| `height` | int | 1080 | 视口高度，范围 `100-10000` |
| `format` | string | `png` | 输出格式：`png` / `jpeg` / `webp` |
| `quality` | int | 90 | 图片质量，范围 `1-100`（`jpeg/webp` 生效） |
| `wait_time` | int | 0 | 额外等待时间（毫秒） |
| `wait_for` | string | 空 | 等待元素出现（CSS 选择器） |
| `selector` | string | 空 | 指定元素截图（CSS 选择器）；为空时截取页面 |
| `full_page` | bool | false | 是否截取整页 |
| `headers` | object | 空 | 自定义请求头 |
| `user_agent` | string | 空 | 自定义 UA |
| `device_scale` | float | 1.0 | 设备像素比，范围 `(0,4]` |
| `mobile` | bool | false | 移动端模式 |
| `landscape` | bool | false | 横屏模式（与 mobile 联动） |
| `timeout` | int | 30 | 超时秒数，范围 `1-120` |
| `clip` | object | 空 | 裁剪区域：`{x,y,width,height}` |

---

## 调用示例

### GET 示例

```bash
# 基本截图
curl "http://localhost:8080/screenshot?url=https://www.google.com" --output screenshot.png

# 指定尺寸和格式
curl "http://localhost:8080/screenshot?url=https://www.google.com&width=1280&height=720&format=webp&quality=85" --output screenshot.webp

# 指定元素截图（selector 为空则为页面截图）
curl "http://localhost:8080/screenshot?url=https://example.com&selector=%23main" --output element.png

# 自定义请求头（headers 需是 JSON 字符串）
curl "http://localhost:8080/screenshot?url=https://example.com&headers=%7B%22Authorization%22%3A%22Bearer%20token123%22%7D" --output screenshot.png
```

### POST 示例

```bash
curl -X POST http://localhost:8080/screenshot \
	-H "Content-Type: application/json" \
	-d '{
		"url": "https://www.example.com",
		"selector": "#main-content",
		"width": 1440,
		"height": 900,
		"format": "webp",
		"quality": 85,
		"wait_time": 3000,
		"wait_for": "#main-content",
		"full_page": false,
		"headers": {
			"Authorization": "Bearer your-token"
		},
		"user_agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
		"device_scale": 2.0,
		"mobile": false,
		"landscape": false,
		"timeout": 60
	}' \
	--output screenshot.webp
```

### 裁剪截图示例

```bash
curl -X POST http://localhost:8080/screenshot \
	-H "Content-Type: application/json" \
	-d '{
		"url": "https://www.google.com",
		"clip": {
			"x": 100,
			"y": 100,
			"width": 500,
			"height": 300
		},
		"format": "jpeg",
		"quality": 95
	}' \
	--output cropped.jpg
```

---

## 错误说明

常见错误状态码：

- `400`：参数校验失败（如 URL 非法、width 超范围）
- `503`：未配置/不可用的 browserless/chrome endpoint
- `502`：无法连接 browserless/chrome endpoint
- `504`：页面加载超时 / `wait_for` 等待超时
- `500`：截图执行失败或内部错误

---

## 说明

- 当前服务连接远程 browserless/Chrome DevTools，不在本进程内直接启动浏览器。
- 生产环境建议在入口网关限制目标 URL，避免被用于 SSRF 等风险场景。
