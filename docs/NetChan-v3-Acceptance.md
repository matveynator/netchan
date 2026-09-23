# NetChan v3 — Acceptance, Security & Performance

> Связанная страница: [NetChan v3 Roadmap](./NetChan-v3-Roadmap.md)

## Назначение

Эта страница определяет измеримые критерии, при которых NetChan v3 может переходить между prerelease-этапами и в stable release.

Критерии разделены на:

- functional acceptance;
- security;
- interoperability;
- resource limits;
- performance;
- CI / release gates.

## Functional acceptance

### Core

- сообщения до 4 КиБ доставляются без повреждения;
- закрытие reply/session channel прекращает дальнейшую обработку;
- session ID и end-to-end sequencing не зависят от текущего next hop;
- route failover не требует повторного открытия пользовательской сессии;
- все очереди bounded;
- malformed input не вызывает unbounded allocation.

### UDP

Проверяется:

- multicast discovery;
- local broadcast discovery;
- MTU-safe framing;
- отсутствие IP fragmentation;
- selective-repeat ARQ;
- cumulative ACK + bitmap;
- adaptive RTO;
- bounded retransmit window;
- congestion backoff.

### BLE

Проверяется:

- advertisement discovery;
- version + protected network tag + short NodeID;
- deterministic connection initiator;
- GATT write/notify;
- fragmentation/reassembly;
- credits;
- ARQ;
- bounded buffers;
- scan/advertise alternation;
- UDP ↔ BLE failover без разрыва API session.

### Mesh

Проверяется сеть до 32 узлов:

- signed link-state gossip;
- максимум 8 непосредственных соседей в Pico profile;
- primary и backup next hop;
- Dijkstra routing;
- ETX/RTT/bandwidth metrics;
- hysteresis;
- hold-down;
- hop limit;
- duplicate suppression;
- partition/recovery;
- route change under traffic.

## Security acceptance

### Cryptography

- X25519;
- ChaCha20-Poly1305;
- BLAKE2s;
- short-lived session keys;
- replay protection;
- neighbor handshake через XXpsk2;
- end-to-end session через IKpsk2;
- Noise implementation спрятана за внутренним интерфейсом;
- официальные test vectors проходят;
- перед stable v3 выполнен независимый crypto audit.

### Identity

- X25519 и Ed25519 разделены;
- topology announcements подписываются Ed25519;
- NodeID вычисляется из публичной identity;
- NetworkKey не раскрывается в discovery packets.

### Attack tests

Security tests обязаны покрывать:

- forged topology announcement;
- unknown network key;
- replay;
- malformed frames;
- protocol downgrade;
- duplicate injection;
- oversized length fields;
- active probing cover endpoint;
- plaintext inspection на relay node;
- replay hidden-endpoint nonce/HMAC;
- invalid certificate / endpoint setup;
- fuzz-generated malformed DNS packets.

Relay не должен иметь доступа к application payload end-to-end session.

## Cover transport acceptance

### None

- минимальный overhead;
- reference baseline для остальных профилей.

### Morph

Проверяется:

- variable framing;
- padding;
- ограниченные junk packets;
- отсутствие нарушения delivery semantics;
- TinyGo build;
- bounded overhead.

Morph не обещает статистическую неотличимость от конкретного стандартного протокола.

### HTTPS

Требования:

- настоящий TLS 1.3;
- долгоживущий HTTP/2 stream;
- application-provided domain/certificate;
- hidden endpoint;
- nonce + HMAC(NetworkKey);
- replay cache actor;
- unauthorized traffic передаётся decoy handler;
- обычный decoy HTTP traffic остаётся функциональным.

### HTTP/3

Требования:

- настоящий QUIC/HTTP/3;
- зависимости не попадают в TinyGo core;
- hidden endpoint authentication аналогична HTTPS profile;
- корректная работа поверх UDP.

### DNS

Требования:

- только controlled authoritative subdomain;
- настоящие DNS query/response;
- EDNS;
- TXT payload;
- MTU-safe transactions;
- ACK/retry;
- корректные ответы на посторонние DNS-запросы;
- direct authoritative mode;
- recursive resolver mode.

DNS измеряется отдельно и не обязан выполнять 85% throughput gate.

## Network simulator

Simulator должен воспроизводимо моделировать:

- packet loss;
- duplication;
- reordering;
- latency/jitter;
- MTU changes;
- network partition;
- recovery;
- neighbor disappearance;
- neighbor return;
- link degradation;
- UDP → BLE switch;
- BLE → UDP switch;
- simultaneous route candidates.

Обязательный сценарий:

```text
A --UDP-- B --UDP-- C

1. A открывает session к C.
2. Идёт continuous application traffic.
3. UDP B<->C деградирует/исчезает.
4. Появляется BLE path B<->C.
5. Routing перестраивается.
6. Та же API session продолжает работу.
7. Duplicate delivery и plaintext relay payload отсутствуют.
```

## Fuzzing

Fuzz targets:

- wire frame decoder;
- handshake framing;
- routing announcement decoder;
- ACK bitmap parser;
- DNS parser;
- generated codecs;
- schema negotiation;
- fragmentation/reassembly.

Правило: размеры и length fields проверяются **до** allocation.

## CI matrix

CI должен включать:

| Area | Matrix |
|---|---|
| Go | 1.24 / 1.25 / 1.26 |
| OS | Linux / macOS / Windows |
| Race | desktop packages |
| Fuzz | smoke fuzz targets |
| TinyGo | 0.41+ |
| Embedded | реальная сборка firmware для Pico W |
| Interop | golden tests между поддерживаемыми wire versions |

macOS BLE adapter проверяется отдельной CI-конфигурацией с build tags.

## Pico W acceptance limits

Полный BLE+UDP profile:

- не более 32 topology entries;
- не более 8 direct neighbors;
- не более 4 active user sessions;
- bounded queues независимо от входящего потока;
- не более **128 КиБ динамической памяти NetChan**;
- не более **512 КиБ добавочного flash-кода**.

Превышение лимита блокирует release соответствующего milestone.

## Performance gates

Для сообщений 4 КиБ на одном и том же оборудовании:

- direct UDP ≥ 85% полезной пропускной способности raw link;
- Morph ≥ 85%;
- HTTPS/H3 ≥ 85% соответствующего raw transport;
- BLE ≥ 85%;
- стандартный Morph/strong-cover profile теряет не более 15%.

DNS исключён из этого требования.

### Методика

Сравнение должно выполняться:

- на одинаковом hardware;
- на одинаковой ОС;
- с одинаковым MTU;
- с одинаковым размером payload;
- с фиксированной методикой прогрева;
- с несколькими прогонами;
- с публикацией median и tail latency.

Для каждого prerelease публикуются:

- throughput;
- latency;
- p95 / p99;
- RAM;
- flash;
- failover time;
- BLE energy usage.

### WireGuard comparison

Сравнение с `wireguard-go` выполняется на одинаковом userspace hardware.

Цель сравнения — дать объективный baseline, а не обещать идентичную производительность при иной mesh/cover-функциональности.

## Release gates

Prerelease не публикуется, если есть хотя бы одно из следующего:

- failing unit/property test;
- failing interoperability test;
- reproducible session break on route failover;
- unbounded queue;
- resource regression сверх Pico limits;
- security regression;
- plaintext relay exposure;
- performance regression ниже зафиксированного gate;
- wire incompatibility без version negotiation;
- fuzz crash на поддерживаемом input domain.

## Stable v3 checklist

Перед `v3.0.0`:

- [ ] публичный API заморожен;
- [ ] минимум 3 независимые интеграции;
- [ ] одна из интеграций — Pico W;
- [ ] 12 месяцев без несовместимых изменений API;
- [ ] independent crypto audit завершён;
- [ ] все acceptance suites зелёные;
- [ ] Pico resource limits соблюдаются;
- [ ] benchmarks опубликованы;
- [ ] golden interoperability tests опубликованы;
- [ ] upgrade/downgrade behavior документирован;
- [ ] wire-version negotiation проверен;
- [ ] release notes описывают security assumptions;
- [ ] ограничения cover transports документированы без завышенных обещаний.

## Upstream readiness

Upstream proposal готовится только после достижения stable-quality API и реальных независимых интеграций.

Это означает:

- доказанную переносимость;
- понятные narrow interfaces;
- отсутствие unnecessary dependencies в core;
- совместимость с Go 1 promise;
- подтверждённое использование в Go и TinyGo;
- стабильное поведение API.

Принятие предложения зависит от maintainers и не является частью acceptance criteria NetChan.
