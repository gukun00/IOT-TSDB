<h1 align="center">IOT-TSDB</h1>

<p align="center">面向工业物联网场景的<b>单机时序数据库</b>，基于 Prometheus v2.45.0 LTS 的 TSDB 存储内核二次开发，提供<b>以测点为中心</b>的可视化管理工具。</p>

---

## 特性

**存储内核（完整保留 Prometheus TSDB）**

- 本地持久化时序存储：WAL 预写日志、Gorilla XOR 块压缩、自动 compaction、TTL 保留期清理
- 强杀进程后 WAL 自动回放，数据零丢失
- PromQL 查询引擎（即时查询 / 范围查询），与写入路径共享同一份存储

**测点管理工具（自研 Web UI，`/manage/`）**

- **测点台账**：自动发现 series 并合并业务元数据（描述/单位/量程/分组），支持搜索、分组筛选、分页、在线状态标识
- **实时值**：勾选测点生成大数字卡片，2 秒轮询刷新，超量程红色告警、数据超时黄色提示，选择自动记忆
- **历史分析**：多测点对比曲线，raw/avg/max/min 四种降采样（每序列自动限流 ≤2000 点），按单位自动分双 Y 轴，支持区间缩放与 CSV 导出
- **点表导入导出**：CSV（带 BOM，Excel 直接打开中文不乱码）/ JSON 双格式，导入前预览、按 metric+labels 匹配 upsert、支持预登记离线测点
- **备份还原**：一键快照为自包含 zip（含全部数据块 + 点表），支持下载归档与上传还原；还原采用**进程内热切换**，服务不重启、失败自动回滚
- **总览看板**：序列数/存储占用/备份数/保留期统计 + 测点分组分布图

**原生 Prometheus UI（`/graph`）**

- 完整 PromQL 查询界面与 TSDB 状态页，与管理 UI 共存，经热切换自动跟随最新数据库实例

## 快速开始

### Windows 一键启动

双击 `start-iot-tsdb.bat`（首次运行自动编译），浏览器自动打开 `http://localhost:8080/graph`。

- 测点管理工具：`http://localhost:8080/manage/`
- 强制重新编译：`start-iot-tsdb.bat /build`

### 手动启动

```bash
go build -o iot-tsdb.exe ./cmd/iot-tsdb
./iot-tsdb.exe -listen :8080 -data-dir ./data -retention 360h -block-range 2h
```

### 启动参数

| 参数 | 默认值 | 说明 |
|---|---|---|
| `-listen` | `:8080` | HTTP 监听地址 |
| `-data-dir` | `./data` | TSDB 数据目录 |
| `-retention` | `360h`（15 天） | 数据保留期，超期自动清理 |
| `-block-range` | `2h` | 块压缩时间跨度 |
| `-backup-dir` | `<data-dir>/../backups` | 备份 zip 存放目录 |
| `-meta-file` | `<data-dir>/../pointmeta.json` | 测点元数据文件 |
| `-page-title` | `IOT-TSDB` | Web UI 页面标题 |

## HTTP API

### 数据写入 / 查询（自定义）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/write` | 写入样本：`{"samples":[{"metric","labels","timestamp"(ms),"value"}]}` |
| GET | `/query` | 指定时间点查询：`?matchers=[{"name","op","value"}]&start&end` |
| GET | `/query_agg` | 多序列聚合：`?agg=sum\|avg\|max\|min&...` |
| GET | `/series` | 序列列举 |
| GET | `/health` | 健康检查 |

### 测点管理（`/api/manage/`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/points?search=&group=&page=&size=` | 测点列表（series 与元数据合并） |
| PUT | `/points/meta` | 单条元数据 upsert |
| DELETE | `/points` | 按匹配器删除数据（`{"matchers","drop_meta"}`） |
| GET | `/points/last?keys=` | 每测点最新样本 |
| GET | `/history?keys=&start=&end=&step=&agg=` | 历史查询（桶化降采样） |
| GET | `/dashboard` | 总览统计 |
| GET | `/export?format=csv\|json` | 点表导出 |
| POST | `/import` | 点表导入（multipart `file`） |
| POST | `/backup` | 创建备份（`{"name"}` 可选） |
| GET/DELETE | `/backups`、`/backups/{name}` | 备份列表 / 删除 |
| GET | `/backups/{name}/download` | 下载备份 |
| POST | `/restore` | 还原（JSON `{"name"}` 或 multipart 上传 zip） |

### 原生接口

`/api/v1/query`、`/api/v1/query_range`、`/api/v1/status/tsdb` 等 Prometheus 兼容接口由原生 UI 提供。

## 数据布局

```
<工作目录>/
├── data/              # TSDB 数据目录（head + blocks + WAL）
├── pointmeta.json     # 测点元数据（原子写，key = metric|k1=v1|k2=v2）
└── backups/           # 备份 zip（内含数据块快照 + pointmeta.json 副本）
```

还原时旧数据目录自动保留为 `data.bak-<时间戳>` 作为回滚副本。

## 从源码构建

要求 Go 1.21+。原生 UI 与测点管理 UI 的前端资源已预构建并随仓库分发（`web/ui/static/react/`、`web/manageui/`），克隆后直接编译即可运行，无需 Node.js。

如需修改原生 React UI：

```bash
cd web/ui
npm install
npm run build -w @prometheus-io/app   # 产物移动到 web/ui/static/react/
```

测点管理 UI（`web/manageui/`）为 Vue 3 + ECharts 零构建单页应用，直接编辑源码刷新即生效。

## 目录结构

```
├── cmd/iot-tsdb/           # 服务入口（组装 tsdb 内核 + HTTP 层）
├── internal/iot-tsdb/
│   ├── api/                # 自定义写入/查询 API + 测点管理 API
│   ├── meta/               # 测点元数据存储（JSON 原子持久化）
│   ├── backup/             # 备份快照 + 进程内热切换还原
│   └── dbholder/           # 数据库实例持有器（热切换核心）
├── web/manageui/           # 测点管理 Web UI（Vue3 + ECharts，零构建）
├── web/ui/                 # 原生 Prometheus React UI（预构建于 static/react）
├── tsdb/                   # Prometheus TSDB 存储内核（完整保留，可修改）
├── promql/                 # PromQL 查询引擎
└── start-iot-tsdb.bat      # Windows 一键启动脚本
```

## 与上游 Prometheus 的关系

本仓库 fork 自 [prometheus/prometheus](https://github.com/prometheus/prometheus) v2.45.0 LTS：

- TSDB 存储内核（`tsdb/`、`model/`、`storage/`）完整保留且可自由修改，掌握存储层控制权
- 移除了 scrape / rules / notifier 等监控采集组件的运行时依赖，聚焦纯时序数据库场景
- Go module 路径保留 `github.com/prometheus/prometheus` 以维持内核内部引用

## 许可证

基于 [Apache License 2.0](LICENSE) 发布，继承上游 Prometheus 的版权声明。
