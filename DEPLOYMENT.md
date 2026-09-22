# QoderWork2API — 部署总结

> 2026-09-22 | 在免费 Clawdi 实例上通过 Cloudflare Tunnel 暴露服务

## 📋 项目简介

QoderWork2API 是 QoderWork CN 的 **OpenAI 兼容反向代理**，通过 OAuth 设备授权获取凭证，支持多账号轮转、工具调用和流式响应。

**核心特性：**
- 🔐 OAuth 设备授权（无需 PAT）
- 🔄 多账号自动轮转 + 防雪崩设计
- 🛠 完整 OpenAI tools/tool_choice 支持
- 📡 流式 + 非流式响应
- ⏰ 定时签到 + 积分监控

---

## 🚀 部署步骤

### 1. 环境准备

```bash
# 安装 Go（如果没有）
mkdir -p ~/.local && cd ~/.local
curl -sL "https://go.dev/dl/go1.23.3.linux-amd64.tar.gz" -o go.tar.gz
tar -xzf go.tar.gz && rm go.tar.gz
export PATH=$HOME/.local/go/bin:$PATH

# 克隆项目
cd ~
git clone https://github.com/Sliverkiss/qoderwork2api.git
cd qoderwork2api
```

### 2. 配置服务

```bash
# 创建配置文件
cat > config.json << 'EOF'
{
  "listen": ":7864",
  "api_key": "your-api-key-here",
  "auth_dir": "./auths",
  "state_file": "./data/state.json",
  "region": "cn",
  "cooldown": {
    "hard_credit": "12h",
    "soft_rate": "60s",
    "err_threshold": 5,
    "err_cooldown": "10m"
  },
  "schedule": {
    "checkin_hours": [9, 21],
    "keepalive_hours": [0, 12]
  },
  "upstream": {
    "timeout_seconds": 180
  }
}
EOF

# 创建必要目录
mkdir -p auths data logs
```

### 3. 编译并启动

```bash
# 编译
go build -o qoderwork2api ./cmd/server/

# 启动服务
./qoderwork2api > logs/server.log 2>&1 &
```

### 4. 绑定 QoderWork 账号

```bash
# 生成授权链接
./login url

# 在浏览器打开链接，登录 QoderWork 并授权
# 然后回到终端执行：
./login poll

# 保存凭证到 auths/ 目录（一步完成）
./login poll 2>&1 | python3 -c "
import sys, json, time
raw = sys.stdin.read().strip()
d = json.loads(raw)
uid = d['user_id']
data = {
    'auth': {
        'accessToken': d['token'],
        'refreshToken': d['refresh_token'],
        'expiresAt': int(time.time() + d['expires_in']/1000),
        'domain': 'qoder.com.cn'
    },
    'account': {'uid': uid, 'nickname': ''}
}
with open(f'auths/qoderwork-{uid}.json', 'w') as f:
    json.dump(data, f, indent=1)
print('✅ 凭证已保存')
"

# 重启服务加载账号
pkill -f qoderwork2api
./qoderwork2api > logs/server.log 2>&1 &
```

### 5. 通过 Cloudflare Tunnel 暴露（可选）

```bash
# 下载 cloudflared
cd ~/.local
curl -sL "https://github.com/cloudflare/cloudflared/releases/download/2024.12.0/cloudflared-linux-amd64" -o cloudflared
chmod +x cloudflared

# 启动隧道（替换为你的 token）
./cloudflared tunnel run --token "YOUR_CF_TUNNEL_TOKEN"
```

**Cloudflare DNS 配置：**
| 类型 | 名称 | 目标 | 代理状态 |
|------|------|------|----------|
| CNAME | `你的子域名` | `<tunnel-id>.cfargotunnel.com` | 已代理 🟠 |

---

## ⚠️ 常见问题

### 1. `no_healthy_account` 错误
**原因：** 没有绑定 QoderWork 账号或凭证文件损坏  
**解决：** 运行 `./login.sh` 重新绑定，确保 `auths/` 目录下有有效的 `qoderwork-*.json` 文件

### 2. `invalid_api_key` 错误
**原因：** API Key 不匹配  
**解决：** 检查 `config.json` 中的 `api_key` 和请求头中的 `Authorization: Bearer <key>`

### 3. 浏览器访问根路径显示 404
**原因：** 服务只在 `/v1/` 路径下提供 API  
**解决：** 使用正确的 API 地址：`http://域名/v1/chat/completions`

### 4. `Method Not Allowed`
**原因：** 用 GET 访问了只支持 POST 的接口  
**解决：** 使用 POST 请求调用 `/v1/chat/completions`

### 5. Token 过期
**原因：** dt- 凭证有效期约 30 天  
**解决：** 重新运行 `./login.sh` 获取新凭证

---

## 🔧 验证命令

```bash
# 检查服务状态
curl -s http://localhost:7864/v1/models -H "Authorization: Bearer your-api-key"

# 测试对话
curl -s -X POST http://localhost:7864/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}'
```

---

## 📊 支持的模型

| 模型名 | 上游 key |
|--------|----------|
| `auto` | 自动选择 |
| `deepseek-v4-flash` | `dfmodel` |
| `deepseek-v4-pro` | `dmodel` |
| `qwen3.8-max` | `qmodel_38max` |
| `qwen3.7-max` | `qmodel_latest` |
| `qwen3.7-plus` | `qmodel` |
| `qwen3.6-flash` | `q36fmodel` |
| `glm-5.2` | `gm51model` |
| `kimi-k2.7-code` | `kmodel` |
| `minimax-m2.7` | `mmodel` |

动态模型列表每小时从上游拉取，失败回退静态表。

---

## 📝 维护命令

```bash
# 查看积分
./credit.sh

# 批量签到
./signin.sh

# 查看日志
tail -f logs/server.log

# 重启服务
pkill -f qoderwork2api && ./qoderwork2api > logs/server.log 2>&1 &
```

---

## 🔗 参考链接

- 原项目：https://github.com/Sliverkiss/qoderwork2api
- Cloudflare Tunnel：https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/
- Go 下载：https://go.dev/dl/
