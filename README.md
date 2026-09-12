# 沟通牌（talkcards）

沟通牌是一款从华为内部传出的扑克玩法（"跑得快"类团队变种）。本项目将其线上化：Go 单二进制（前端已嵌入），SQLite 持久化账号与战绩，支持断线重连与历史战绩，适合团队团建。

项目地址：https://github.com/peng-mj/talkcards

## 游戏规则

8 人一桌 4 v 4 两队，6 副牌 324 张，先出完（并带走对方剩分）或先抓满 300 分者胜。牌型仅有单张、对子、三条、炸弹、王炸（同张数大王炸 > 小王炸）；出完手牌由队友接风。招牌玩法是"沟通"：交流完全公开——队友可明着商量，但发言全场广播，对手也听得到，可以放烟雾弹。

完整规则见 [game-rules.md](game-rules.md)。

## 快速开始

从 [Releases](https://github.com/peng-mj/talkcards/releases) 下载单文件二进制（`talkcards-linux-amd64` / `talkcards-linux-arm64`）：

```sh
chmod +x talkcards-linux-amd64 && ./talkcards-linux-amd64
```

浏览器打开 `http://localhost:8000`，输入用户名即玩。

## 开发

```sh
cd backend && go run ./cmd/server    # 开发模式
./build.sh                           # 交叉编译单二进制
./build.sh test                      # 构建 + 完整对局审计
cd backend && go test ./...          # 单元测试
node backend/scripts/e2e-bot.js      # e2e 协议验收（先 npm install）
```

| 参数 | 默认值 | 说明 |
|---|---|---|
| `--port, -p` | 8000 | 监听端口（或 `PORT` 环境变量） |
| `--host` | 所有接口 | 监听地址 |
| `--reconnect-timeout` | 600 | 对局中断线保留座位秒数，0 为无限 |

战绩经 SQLite 持久化（固定 `./talkcards.db`，始终开启）。

## License

MIT
