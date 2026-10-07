# Privacy Filter (Go)

**English** | [简体中文](README.zh-CN.md)

Strip sensitive user data (PII / secrets) from text before it reaches an LLM.
Pure Go, no model, no GPU, no CGO — a single static binary, millisecond latency on text of any length.

🌐 Running in production at [PackyCode](https://www.packyapi.com) — the privacy-compliance component of an API relay service.

---

## Three ways to use it

1. **Core package**: `import "privacyfilter/filter"` straight into your gateway — redaction is one function call, no HTTP hop.
2. **HTTP service**: `cmd/http`, REST API.
3. **gRPC service**: `cmd/grpc`, interface in `proto/filter.proto`.

The latter two are thin wrappers around the `filter` core package.

---

## Two detection layers

| Layer | Covers | Technique |
|---|---|---|
| Structured PII | Email, phone, national ID, bank card (Luhn-checked), IP | Regex |
| Secrets / credentials | API keys, tokens, private keys, passwords written in prose, unknown high-entropy strings | gitleaks ruleset (keyword pre-filter) + contextual regex + Shannon-entropy fallback |

Each layer emits `(start, end, type ID, default label)` spans → spans are merged and de-overlapped → text and entities are rebuilt in a single pass.
Default labels are `[EMAIL]`, `[PHONE]`, `[ID]`, `[BANK_CARD]`, `[IP]`, and `[SECRET]`. Stable type IDs are independent of those labels.

> No person / place / organization name recognition — that needs an NER model, which costs seconds of CPU time on long text and was removed per requirements.
> High-risk identity data (national ID, bank card, secrets, etc.) is fully covered by regex.

---

## Layout

```
privacy-filter/
├── go.mod / go.sum
├── filter/                  core package (import directly from a gateway)
│   ├── filter.go            Filter / New / Redact
│   ├── pii.go               structured PII
│   ├── secrets.go           gitleaks + context + entropy
│   └── filter_test.go
├── cmd/
│   ├── http/main.go         HTTP service
│   └── grpc/main.go         gRPC service
├── proto/filter.proto       gRPC interface definition
├── gen/filterpb/            protoc-generated code
├── rules/gitleaks.toml      gitleaks ruleset
├── scripts/fetch_rules.sh   ruleset update script
└── Dockerfile
```

---

## Build

```bash
go build -o bin/server-http ./cmd/http
go build -o bin/server-grpc ./cmd/grpc
go test ./...                          # run all tests
```

---

## Usage

### 1. Core package (recommended for gateways)

```go
import "privacyfilter/filter"

// Create once at startup; concurrency-safe, reuse globally.
f, err := filter.New("rules/gitleaks.toml", filter.Config{}) // pass "" for built-in fallback rules

// Per request
res := f.Redact(userPrompt)
forwardToLLM(res.Redacted)                    // forward the redacted text to the LLM
```

`filter.Result`: `Redacted` (redacted text), `Hit`, `Count`, `Entities` (hit details).
`Entity.Type` is a stable type ID, not a display label. `Start`/`End` refer to the
original UTF-8 byte offsets; `Text` contains the original sensitive value, so do
not log entities indiscriminately. No-hit JSON retains `"entities":[]`.

#### Configurable replacement text

```go
f, err := filter.New("rules/gitleaks.toml", filter.Config{
    Replacement: "<REDACTED>",
    ReplacementLabels: map[string]string{
        "email": "<REDACTED_EMAIL>",
        "secret": "<REDACTED_SECRET>",
    },
})
```

Precedence: matching type entry > global replacement > detector default.
`Config{}` uses these English defaults; an empty global replacement is unset.
Current type IDs are `email`, `phone`, `id`, `bank_card`, `ip`, and `secret`.
IDs are open, case-sensitive strings: new detector types need no renderer or
gateway mapping changes. Unused keys have no effect and do not create detectors.

`New` rejects empty/edge-whitespace keys and empty/whitespace-only map values;
a nonempty global replacement cannot be whitespace-only. Valid labels retain
their whitespace. Configuration maps are copied once at construction and remain
read-only during requests.

Replacement uses detected spans, not a search-and-replace of literal labels.
Repeated redaction is not guaranteed to be idempotent, even with defaults:
`password=4111111111111111` becomes `password=[BANK_CARD]`, which becomes
`password=[SECRET]` on a second call. The original sensitive value remains
removed. Prefer `<REDACTED_EMAIL>`-style custom labels, and validate them in the
contexts used by your application.

**API migration:** `New(path)` becomes `New(path, Config{})`; `Entity.Type`,
including HTTP/gRPC `Entity.type`, now returns IDs such as `email` instead of
labels such as `[邮箱]`. Entity fields and protobuf wire types are otherwise
unchanged. Standalone service commands continue using default labels; custom
replacement is configured through the Go API, not new environment variables.

Default redacted output now uses English labels instead of the previous Chinese
labels. Set `ReplacementLabels` explicitly if an integration requires the older
text; the default-label change itself does not search-and-replace input text.

> To consume this package from your own gateway module: put it in the same monorepo, or add
> `replace privacyfilter => ../privacy-filter` to the gateway's go.mod. The `filter` package
> depends only on `BurntSushi/toml`.

### 2. HTTP service

```bash
./bin/server-http                    # default :8088
```

```bash
curl http://127.0.0.1:8088/health
curl -X POST http://127.0.0.1:8088/redact -H 'Content-Type: application/json' \
  -d '{"text":"我的邮箱是 a@b.com，密码是 Hunter2xy"}'
# {"redacted":"我的邮箱是 [EMAIL]，密码是 [SECRET]","hit":true,"count":2,"entities":[...],"elapsed_ms":0.08}
```

Endpoints: `GET /health`, `POST /redact`, `POST /redact/batch` (`{"texts":[...]}`).

### 3. gRPC service

```bash
./bin/server-grpc                    # default :8089
```

Service `filter.v1.PrivacyFilter`, methods `Redact` / `RedactBatch`, defined in `proto/filter.proto`.
Generate a client from that proto on the gateway side. To regenerate the code in this repo:

```bash
protoc -I. --go_out=. --go_opt=module=privacyfilter \
       --go-grpc_out=. --go-grpc_opt=module=privacyfilter proto/filter.proto
```

---

## Configuration (environment variables)

| Variable | Default | Description |
|---|---|---|
| `PF_PORT` | `8088` | HTTP listen port |
| `PF_GRPC_PORT` | `8089` | gRPC listen port |
| `PF_GITLEAKS_TOML` | `rules/gitleaks.toml` | path to the gitleaks rules file |

---

## Performance

Local default-path comparison on Apple M1 Pro, darwin/arm64, `GOMAXPROCS=1`,
five runs of 500ms per case; values are medians. "Before" uses the pre-configuration
core, not an emulated custom-label implementation.
These measurements precede the English-default label change; label length can
affect output allocations, so rerun the benchmarks for the current defaults.

| Synthetic input | Before latency | After latency | Before B/op | After B/op | Before allocs/op | After allocs/op |
|---|---:|---:|---:|---:|---:|---:|
| Short, no hit | 2.511 µs | 2.550 µs | 16 | 0 | 1 | 0 |
| Short, sparse hits | 13.127 µs | 13.018 µs | 1104 | 1040 | 16 | 13 |
| ~32 KiB, no hit | 9.840 ms | 10.018 ms | 32770 | 2 | 1 | 0 |
| ~32 KiB, sparse hits | 9.989 ms | 9.988 ms | 82956 | 82892 | 16 | 13 |
| ~7 KiB, dense overlapping hits | 3.499 ms | 3.522 ms | 221783 | 221543 | 579 | 568 |

These measurements are not a production latency guarantee.

Configurable labels do not add detection or a second output reconstruction.
Default/global-only configurations skip label-map lookup. No-hit output reuses
the input string. Typed internal spans are larger, but in-place filtering and
overlap compaction avoid copying candidate arrays; selection rules are unchanged.
Longer replacements still increase output size.

Reproduce the configuration benchmarks with:

```bash
GOMAXPROCS=1 go test ./filter -run '^$' -bench '^BenchmarkRedactConfig$' -benchmem -count=5 -benchtime=500ms
```

---

## Integration notes (gateway side)

- With the core-package import there is no HTTP/gRPC hop, hence no timeout and no fail-open/closed concerns.
- If you use the HTTP/gRPC service: set a 150–300ms timeout; on failure, prefer fail-closed (reject the request rather than forwarding the raw text).

---

## Notes

- All **222 gitleaks rules compile natively** in Go (Go's `regexp` is RE2, the same engine gitleaks uses;
  an earlier Python port lost 26 rules to RE2-incompatible syntax).
- Go `regexp` runs in linear time — no catastrophic backtracking (ReDoS) risk.
- gitleaks does not support look-around assertions, so digit boundaries for phone / national ID etc. are
  enforced by manual post-match validation.
- No person / place / organization recognition. If added later, prefer rules (a Chinese-address regex is
  feasible; names are better anchored by context).
- The entropy fallback can mis-flag git SHAs, long base64 strings, etc. — tune the threshold or add an allowlist.
```
