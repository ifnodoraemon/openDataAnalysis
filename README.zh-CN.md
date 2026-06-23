# 数据分析智能体

[English](README.md)

面向表格数据的交互式智能分析工具。用户上传 CSV/Excel 或导入 PostgreSQL snapshot 后，Agent 通过 SQL、Python 和报告工具完成探索、分析、图表和研报生成。

![数据分析智能体界面](docs/images/screenshot.png)

## 当前能力

- Agent 自主决定工具调用顺序，不使用隐藏 DAG。
- 支持鉴权、工作区、会话、run、文件归属和报告恢复。
- 支持 CSV/Excel 上传，以及 PostgreSQL 工作区数据源的 bounded snapshot import。
- 开发模式下使用 Go 后端、Vue 3 前端、SQLite 元数据库和 session-scoped SQLite 分析工作库。
- 文件和报告通过存储抽象管理，并对 S3-compatible 迁移设定生产模式守卫。

## 快速开始

本地调试统一使用 Docker Compose。

```bash
cp server/.env.example server/.env
# 填写 LLM_PROVIDER / LLM_API_KEY / LLM_MODEL / AUTH_SECRET 等配置
docker compose up -d --build
```

浏览器访问 `http://localhost`。

常用命令：

```bash
docker compose up -d --build --force-recreate
docker compose logs -f server
docker compose logs -f client
docker compose logs -f python-executor
docker compose down
```

## 部署模式

默认 Docker Compose 是开发 profile。它使用本地 SQLite 元数据、本地对象存储、进程内 run 执行、session SQLite 分析工作库，以及 executor 本地 Python 产物。

生产部署必须显式设置 `DEPLOYMENT_MODE=production`。在该模式下，只要仍选择以下本地或单进程后端，服务会拒绝启动：

- `METADATA_STORE=sqlite`
- `STORAGE_PROVIDER=local`
- `RUN_BACKEND=inprocess`
- `ANALYSIS_STORE=session_sqlite`
- `PYTHON_ARTIFACT_STORE=executor_local`
- wildcard 或 localhost `CORS_ALLOWED_ORIGINS`

MaaS 目标架构契约见 [`docs/maas-production-architecture.md`](docs/maas-production-architecture.md)。共享本地卷和 WebSocket sticky session 不被视为生产扩展方案。

## 架构概览

| 层 | 说明 |
|---|---|
| Frontend | Vue 3 + Vite + Pinia |
| Backend | Go + Chi + Gorilla WebSocket |
| Agent Runtime | Tool-calling ReAct loop，状态和工具由 runtime 暴露，路径由模型判断 |
| Analysis DB | 开发模式：每个 session 一个 SQLite 分析工作库。生产目标：worker 可重建 scratch state，事实来自 durable snapshot manifest |
| Metadata DB | 开发模式：SQLite。生产目标：通过 repository 接口接入 PostgreSQL |
| Python Executor | 独立服务，用于 SQL 不适合的高级分析 |
| Storage | 开发模式：本地对象存储。生产目标：S3-compatible 对象存储 |

开发模式运行期目录都收敛到 `data/`：

- `data/metadata/`：元数据库
- `data/cache/`：session SQLite
- `data/storage/`：源文件和报告对象
- `data/tmp/`：临时文件
- `data/llm-debug/`：LLM trace

## 数据源边界

| 类型 | 当前状态 |
|---|---|
| CSV | 推荐用于大文件；流式批量导入，无行数硬上限 |
| Excel | 单 sheet 100,000 行硬上限 |
| PostgreSQL | 工作区级只读连接，导入为 session SQLite snapshot；默认受 `POSTGRES_IMPORT_ROW_LIMIT=1000000` 限制 |
| Live upstream query | 暂不支持；后续应作为独立能力设计 |

数据规模分层：

| 规模 | 行数 | 默认画像模式 |
|---|---:|---|
| small | < 10,000 | exact |
| medium | 10,000 - 99,999 | mixed |
| large | 100,000 - 999,999 | sampled |
| xlarge | >= 1,000,000 | sampled；数据库导入默认使用受限快照 |

## 主要 API

- `POST /api/auth/login`
- `POST /api/auth/switch-workspace`
- `GET /api/bootstrap`
- `GET /api/sessions`
- `GET /api/runs`
- `POST /api/upload?session_id=...`
- `GET /api/sessions/{sessionID}/sources`
- `POST /api/data-sources`
- `GET /api/data-sources`
- `POST /api/data-sources/{sourceID}/test`
- `GET /api/data-sources/{sourceID}/catalog`
- `POST /api/data-sources/{sourceID}/import`
- `GET /ws?token=...&session_id=...`

创建 connector-backed source 使用公开 `config` 加加密 `credential`：

```json
{
  "name": "Analytics PG",
  "source_type": "postgres_connection",
  "config": {
    "host": "db.example.com",
    "port": 5432,
    "database_name": "analytics",
    "default_schema": "public",
    "username": "reader",
    "allowlist": [{ "name": "orders", "kind": "table" }]
  },
  "credential": { "password": "secret" }
}
```

除 `/api/auth/login` 和 `/api/health` 外，API 默认需要 token。

## 开发检查

```bash
cd server && go test ./...
cd client && npm run test
cd client && npm run build
cd client && npm run format:check
cd python-executor && python -m pytest test_sandbox.py
```

## 文档索引

- `AGENTS.md`：仓库级 agent 约束和提示分层规则。
- `docs/product-first-principles.zh-CN.md`：产品第一性原则。
- `docs/agentic-principles.md`：agent runtime 设计原则。
- `docs/maas-production-architecture.md`：生产 MaaS 后端契约和迁移顺序。
- `docs/building-agent-lessons.zh-CN.md`：Agent runtime 实践经验。
- `docs/database-source-mvp.md`：数据库数据源当前实现和边界。
- `docs/benchmark.md`：回归评测方案。
- `samples/coverage_scenarios/README.zh-CN.md`：覆盖场景说明。

## 许可证

MIT，详见 [LICENSE](LICENSE)。
