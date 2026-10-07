# Privacy Filter — 隐私过滤（Go）

[English](README.md) | **简体中文**

在文本进入 LLM 之前，过滤掉用户的敏感信息（PII / 密钥）。
纯 Go、无模型、无 GPU、无 CGO —— 单个静态二进制，任何长度文本都是毫秒级。

🌐 官网使用：[PackyCode](https://www.packyapi.com) —— API 中转服务的隐私合规组件

---

## 三种用法

1. **核心包**：网关直接 `import "privacyfilter/filter"`，过滤是一次函数调用，无 HTTP 跳转。
2. **HTTP 服务**：`cmd/http`，REST 接口。
3. **gRPC 服务**：`cmd/grpc`，接口见 `proto/filter.proto`。

后两个都只是核心包 `filter` 的薄封装。

---

## 两层检测

| 层 | 负责 | 技术 |
|---|---|---|
| 结构化 PII | 邮箱、手机号、身份证、银行卡（Luhn 校验）、IP | 正则 |
| 密钥 / 凭证 | API key、token、私钥、句子里的口令、未知高熵随机串 | gitleaks 规则集（关键词预筛）+ 上下文正则 + 香农熵兜底 |

各层产出 `(起点, 终点, 类型 ID, 默认标签)` 区间 → 合并去重叠 → 单遍重建文本与实体。
默认标签为 `[EMAIL] [PHONE] [ID] [BANK_CARD] [IP] [SECRET]`，稳定类型 ID 与展示标签独立。

> 不含人名/地名/机构识别 —— 那类需要 NER 模型，CPU 上对长文本要数秒，已按需求移除。
> 高危身份信息（身份证/银行卡/密钥等）全部由正则覆盖。

---

## 目录结构

```
privacy-filter/
├── go.mod / go.sum
├── filter/                  核心包（可被网关直接 import）
│   ├── filter.go            Filter / New / Redact
│   ├── pii.go               结构化 PII
│   ├── secrets.go           gitleaks + 上下文 + 熵
│   └── filter_test.go
├── cmd/
│   ├── http/main.go         HTTP 服务
│   └── grpc/main.go         gRPC 服务
├── proto/filter.proto       gRPC 接口定义
├── gen/filterpb/            protoc 生成的代码
├── rules/gitleaks.toml      gitleaks 规则集
├── scripts/fetch_rules.sh   规则更新脚本
└── Dockerfile
```

---

## 构建

```bash
go build -o bin/server-http ./cmd/http
go build -o bin/server-grpc ./cmd/grpc
go test ./...                          # 跑全部测试
```

---

## 用法

### 1. 核心包（推荐网关用这个）

```go
import "privacyfilter/filter"

// 启动时创建一次，并发安全，可全局复用
f, err := filter.New("rules/gitleaks.toml", filter.Config{}) // 传 "" 使用内置兜底规则

// 每个请求
res := f.Redact(userPrompt)
forwardToLLM(res.Redacted)                    // 用脱敏后的文本转发给 LLM
```

`filter.Result`：`Redacted`（脱敏后文本）、`Hit`、`Count`、`Entities`（命中明细）。
`Entity.Type` 是稳定类型 ID，不是展示标签；`Start`/`End` 始终对应原文的
UTF-8 字节偏移。`Text` 含原始敏感值，不应随意记录实体日志。无命中 JSON
保持 `"entities":[]`。

#### 自定义替换文本

```go
f, err := filter.New("rules/gitleaks.toml", filter.Config{
    Replacement: "<REDACTED>",
    ReplacementLabels: map[string]string{
        "email": "<REDACTED_EMAIL>",
        "secret": "<REDACTED_SECRET>",
    },
})
```

优先级：匹配类型项 > 全局替换 > 检测器默认标签。`Config{}` 使用上述英文默认标签，
全局空字符串表示未设置。当前类型 ID 为 `email`、`phone`、`id`、`bank_card`、
`ip`、`secret`。ID 是开放、大小写敏感的字符串：检测器新增类型无需修改渲染器
或网关映射。未使用的键不产生效果，也不会创建检测器。

`New` 拒绝空键、带首尾空白的键，以及空或仅含空白的映射值；非空全局标签
不能仅含空白。有效标签保留空格。配置 map 在构造时复制一次，请求期间只读。

替换基于检测命中区间，不是对标签字符串做全局替换。默认标签也不保证重复过滤
幂等：`password=4111111111111111` 首次变为 `password=[BANK_CARD]`，
再次调用会变为 `password=[SECRET]`；原始敏感值仍已移除。推荐
`<REDACTED_EMAIL>` 这类自定义标签，并在应用实际上下文中验证。

**API 迁移：** `New(path)` 改为 `New(path, Config{})`；`Entity.Type` 以及
HTTP/gRPC 的 `Entity.type` 从 `[邮箱]` 等标签改为 `email` 等 ID。实体字段数量
和 protobuf 线类型不变。独立服务命令仍使用默认标签；替换通过 Go API 配置，
本次未新增环境变量。

默认脱敏输出已由中文标签改为英文标签。依赖旧文本的集成需显式配置
`ReplacementLabels`；默认标签切换本身不会对输入文本做标签字符串替换。

> 从你自己的网关模块引用本包：放进同一个 monorepo，或在网关的 go.mod 里加
> `replace privacyfilter => ../privacy-filter`。`filter` 包只依赖 `BurntSushi/toml`。

### 2. HTTP 服务

```bash
./bin/server-http                    # 默认 :8088
```

```bash
curl http://127.0.0.1:8088/health
curl -X POST http://127.0.0.1:8088/redact -H 'Content-Type: application/json' \
  -d '{"text":"我的邮箱是 a@b.com，密码是 Hunter2xy"}'
# {"redacted":"我的邮箱是 [EMAIL]，密码是 [SECRET]","hit":true,"count":2,"entities":[...],"elapsed_ms":0.08}
```

接口：`GET /health`、`POST /redact`、`POST /redact/batch`（`{"texts":[...]}`）。

### 3. gRPC 服务

```bash
./bin/server-grpc                    # 默认 :8089
```

服务 `filter.v1.PrivacyFilter`，方法 `Redact` / `RedactBatch`，定义见 `proto/filter.proto`。
网关侧用该 proto 生成客户端即可。重新生成本仓代码：

```bash
protoc -I. --go_out=. --go_opt=module=privacyfilter \
       --go-grpc_out=. --go-grpc_opt=module=privacyfilter proto/filter.proto
```

---

## 配置（环境变量）

| 变量 | 默认 | 说明 |
|---|---|---|
| `PF_PORT` | `8088` | HTTP 监听端口 |
| `PF_GRPC_PORT` | `8089` | gRPC 监听端口 |
| `PF_GITLEAKS_TOML` | `rules/gitleaks.toml` | gitleaks 规则文件路径 |

---

## 性能

本机默认路径对比：Apple M1 Pro、darwin/arm64、`GOMAXPROCS=1`，每个场景
运行五轮、每轮 500ms，表中为中位数。“改造前”直接使用原核心包，
不包含模拟的自定义标签实现。
这些测量发生在默认标签改为英文之前；标签长度可能影响输出分配，
当前默认标签的性能应通过下述基准重新测量。

| 合成输入 | 改造前耗时 | 改造后耗时 | 改造前 B/op | 改造后 B/op | 改造前分配次数/op | 改造后分配次数/op |
|---|---:|---:|---:|---:|---:|---:|
| 短文本，无命中 | 2.511 µs | 2.550 µs | 16 | 0 | 1 | 0 |
| 短文本，少量命中 | 13.127 µs | 13.018 µs | 1104 | 1040 | 16 | 13 |
| 约 32 KiB，无命中 | 9.840 ms | 10.018 ms | 32770 | 2 | 1 | 0 |
| 约 32 KiB，少量命中 | 9.989 ms | 9.988 ms | 82956 | 82892 | 16 | 13 |
| 约 7 KiB，密集重叠命中 | 3.499 ms | 3.522 ms | 221783 | 221543 | 579 | 568 |

这些测量不代表生产环境延迟保证。

自定义标签不会增加检测或二次重建输出。默认配置和仅全局配置不查询标签 map。
无命中时复用原文。内部类型化区间结构变大，但筛选与重叠压缩改为原地处理，
避免复制候选数组，选择规则不变。更长的替换文本仍会增加输出大小。

配置基准运行方式：

```bash
GOMAXPROCS=1 go test ./filter -run '^$' -bench '^BenchmarkRedactConfig$' -benchmem -count=5 -benchtime=500ms
```

---

## 对接建议（网关侧）

- 用核心包 import 的方式，没有 HTTP/gRPC 跳转，也就没有超时与 fail-open/closed 问题。
- 若用 HTTP/gRPC 服务：设 150–300ms 超时；失败时建议 fail-closed（拒绝请求而非放行原文）。

---

## 备注

- gitleaks 规则在 Go 下 **222 条全部原生编译**（Go 的 `regexp` 即 RE2，与 gitleaks 同源；
  早先 Python 版因 RE2 专有语法丢了 26 条）。
- Go `regexp` 线性时间，无灾难性回溯（ReDoS）风险。
- gitleaks 不支持前后向断言，手机号/身份证等的数字边界用匹配后手工校验实现。
- 不识别人名/地名/机构。若日后要补，建议走规则（中文地址正则可行；人名宜上下文锚定）。
- 高熵兜底会误伤 git SHA、长 base64 串等 —— 可调阈值或加白名单。
