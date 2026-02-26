编写一个go连接playwright服务器并网页截图的服务
使用环境变量设置playwright服务器
用/health检查服务是否正常
使用 Gin

# GET 请求示例
```
# 基本截图
curl "http://localhost:8080/screenshot?url=https://www.google.com"

# 指定尺寸和格式
curl "http://localhost:8080/screenshot?url=https://www.google.com&width=1280&height=720&format=webp&quality=85"

# 完整参数
curl "http://localhost:8080/screenshot?url=https://www.baidu.com&width=1920&height=1080&format=jpeg&quality=90&wait_time=2000&full_page=true&mobile=false"

# 带请求头
curl "http://localhost:8080/screenshot?url=https://example.com&headers=%7B%22Authorization%22%3A%22Bearer%20token123%22%7D"

# 保存到文件
curl -o screenshot.png "http://localhost:8080/screenshot?url=https://www.google.com&format=png"
```
# POST 请求示例
```
# 基本 POST 请求
curl -X POST http://localhost:8080/screenshot \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://www.google.com",
    "width": 1920,
    "height": 1080,
    "format": "png"
  }' --output screenshot.png

# 完整参数 POST 请求
curl -X POST http://localhost:8080/screenshot \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://www.example.com",
    "width": 1440,
    "height": 900,
    "format": "webp",
    "quality": 85,
    "wait_time": 3000,
    "wait_for": "#main-content",
    "full_page": false,
    "headers": {
      "Authorization": "Bearer your-token",
      "Cookie": "session=abc123"
    },
    "user_agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
    "device_scale": 2.0,
    "mobile": false,
    "landscape": false,
    "timeout": 60
  }' --output screenshot.webp

# 裁剪区域截图
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
  }' --output cropped.jpg
```

# 参数说明

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `url` | string | 必填 | 目标网页 URL |
| `width` | int | 1920 | 视口宽度 (100-4096) |
| `height` | int | 1080 | 视口高度 (100-10000) |
| `format` | string | png | 输出格式: png/jpeg/webp |
| `quality` | int | 90 | 压缩质量 (1-100) |
| `wait_time` | int | 0 | 额外等待时间(毫秒) |
| `wait_for` | string | - | 等待元素出现(CSS选择器) |
| `full_page` | bool | false | 是否全页面截图 |
| `headers` | object | - | 自定义请求头 |
| `user_agent` | string | - | 自定义 User-Agent |
| `device_scale` | float | 1.0 | 设备像素比 |
| `mobile` | bool | false | 移动端模拟 |
| `landscape` | bool | false | 横屏模式 |
| `timeout` | int | 30 | 超时时间(秒, 最大120) |
| `clip` | object | - | 裁剪区域 {x,y,width,height} |
