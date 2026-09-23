# NetChan v3: UDP/BLE mesh, сквозное шифрование и DPI-маскировка

> Статус: проектирование / pre-alpha roadmap  
> Модуль: `github.com/matveynator/netchan/v3`  
> Цель: сохранить `main` и v2 стабильными до прохождения acceptance-тестов v3.

## Содержание

- [Цели v3](#цели-v3)
- [Архитектура](#архитектура)
- [Публичный API](#публичный-api)
- [Concurrency model](#concurrency-model)
- [Codecs и TinyGo](#codecs-и-tinygo)
- [Криптография и identity](#криптография-и-identity)
- [UDP transport](#udp-transport)
- [BLE transport](#ble-transport)
- [Mesh routing](#mesh-routing)
- [DPI и cover transports](#dpi-и-cover-transports)
- [Целевые платформы](#целевые-платформы)
- [Milestones](#milestones)
- [Release policy](#release-policy)
- [Связанная страница](#связанная-страница)

## Цели v3

Первый prerelease NetChan v3 должен поддерживать:

- UDP;
- TCP;
- BLE;
- автоматическое обнаружение соседей;
- multi-hop mesh до 32 узлов;
- сообщения до 4 КиБ;
- сохранение пользовательской сессии при смене маршрута;
- сквозное шифрование между конечными узлами;
- независимые cover transports для DPI-маскировки.

API проектируется с расчётом на качество стандартной библиотеки Go и с учётом [Go 1 compatibility promise](https://go.dev/doc/go1compat) и официального [Go modules release workflow](https://go.dev/doc/modules/release-workflow).

Целевой embedded-стенд — Raspberry Pi Pico W, TinyGo 0.41+ и Go 1.24+. Совместимость TinyGo отслеживается по официальной [Go compatibility matrix](https://tinygo.org/docs/reference/go-compat-matrix/).

## Архитектура

NetChan v3 разделяется на независимые слои:

```text
+-------------------------------------------------------+
|                 Application / Codec[T]                |
+-------------------------------------------------------+
|             End-to-end NetChan session                |
|       encryption, sequencing, retransmission          |
+-------------------------------------------------------+
|                    Mesh routing                       |
|    topology, Dijkstra, failover, duplicate control    |
+-------------------------------------------------------+
|               Secure neighbor channel                 |
|          Noise XXpsk2 / authenticated peer            |
+-------------------------------------------------------+
|                Physical transport                     |
|        UDP / TCP / BLE / cover transport              |
+-------------------------------------------------------+
```

Каждый слой должен иметь минимальный контракт и не зависеть от деталей более нижних уровней, кроме явно переданного интерфейса.

Wi‑Fi и Ethernet рассматриваются как разные экземпляры UDP-транспорта, а не как отдельные протоколы NetChan.

## Публичный API

Корневой пакет содержит только минимальный стабильный API:

```go
type Node struct { /* ... */ }
type NodeID [/* ... */]byte
type Identity struct { /* ... */ }
type NetworkKey [/* ... */]byte
type Limits struct { /* ... */ }
type Event struct { /* ... */ }

type Codec[T any] interface {
    Encode(dst []byte, value T) ([]byte, error)
    Decode(src []byte) (T, error)
}

type Transport interface {
    // narrow transport contract
}

func NewNode(/* ... */) (*Node, error)
func Listen[T any](/* ... */) (/* ... */, error)
func Dial[T any](/* ... */) (/* ... */, error)
func (n *Node) Close() error
```

Точные сигнатуры фиксируются только после интеграционных прототипов.

Ошибки должны быть типизированными и корректно работать с `errors.Is` / `errors.As`.

Приложение передаёт библиотеке:

- готовые сетевые адаптеры;
- `net.PacketConn`;
- identity;
- общий network key;
- при необходимости сертификаты и HTTP handler для cover transports.

NetChan **не управляет**:

- Wi‑Fi credentials;
- DHCP;
- flash-хранилищем ключей;
- сертификатным хранилищем приложения.

## Concurrency model

Внутренние подсистемы реализуются как независимые goroutine-actors.

Каждый actor:

- имеет собственный входной канал;
- владеет своим состоянием единолично;
- обрабатывает сообщения через `select`;
- не разделяет mutable state с другими actors.

Отдельные actors владеют:

- neighbor table;
- routing table;
- retransmit state;
- replay cache;
- session state.

Глобальное изменяемое состояние и mutex в core не используются.

Задача передаётся как данные плюс принадлежащий потребителю `reply` / `session` channel. Закрытие этого канала означает отмену ожидания и прекращение дальнейшей обработки без обязательного распространения `context.Context` через весь стек.

## Codecs и TinyGo

Для TinyGo используются статически генерируемые codecs.

`cmd/netchan-gen`:

- генерирует обычный Go-код;
- не использует reflection;
- присваивает wire schema стабильный идентификатор;
- обеспечивает обнаружение несовместимой схемы при открытии сессии.

Reflection-based codec остаётся отдельной desktop-only опцией и не должен попадать в TinyGo core по транзитивным зависимостям.

## Криптография и identity

Криптографическая модель следует подходу WireGuard / Noise:

- X25519;
- ChaCha20-Poly1305;
- BLAKE2s;
- короткоживущие session keys;
- replay protection.

WireGuard использует близкий набор примитивов в Noise IK: <https://www.wireguard.com/protocol/>.

### Neighbor security

Соседние mesh-узлы используют `Noise XXpsk2`, поскольку peer заранее может быть неизвестен.

### End-to-end security

Сквозные пользовательские сессии используют `IKpsk2`.

Промежуточный mesh-узел может:

- видеть routing metadata;
- выбирать следующий hop;
- пересылать packet frame;

но не должен иметь возможность прочитать application payload.

### Identity

`Identity` содержит отдельные ключи:

- X25519 — key agreement;
- Ed25519 — подпись topology announcements.

`NodeID` вычисляется из публичной identity.

Общий `NetworkKey` разрешает участие в mesh, но никогда не публикуется в discovery packets.

Noise-реализация первоначально скрывается за внутренним интерфейсом и строится на проверенной библиотеке. Совместимость проверяется официальными test vectors. До stable v3 проводится независимый криптографический аудит.

## UDP transport

UDP transport принимает предоставленный приложением `net.PacketConn`.

Discovery:

- multicast;
- local broadcast;
- защищённый network tag;
- version negotiation.

Data plane:

- MTU-safe framing;
- selective-repeat ARQ;
- cumulative ACK;
- ACK bitmap;
- adaptive RTO;
- bounded retransmit window;
- congestion backoff;
- отсутствие IP fragmentation.

## BLE transport

BLE discovery использует advertisement с:

- версией протокола;
- защищённым network tag;
- сокращённым `NodeID`.

Data channel использует GATT write / notify с:

- fragmentation;
- credits;
- ARQ;
- bounded buffers.

Scan и advertise чередуются.

Инициатор соединения определяется сравнением `NodeID`, чтобы два узла одновременно не создавали два встречных соединения.

Платформы:

- Linux — `tinygo.org/x/bluetooth`;
- Windows — `tinygo.org/x/bluetooth`;
- Pico W — TinyGo Bluetooth API;
- macOS — изолированный native CoreBluetooth peripheral adapter с build tags и отдельным CI.

## Mesh routing

Mesh использует подписанный link-state gossip.

Pico W profile:

- до 32 topology entries;
- до 8 непосредственных соседей;
- до 4 активных пользовательских сессий.

Route metric учитывает:

- ETX;
- RTT;
- доступную пропускную способность.

Основной и резервный next hop вычисляются локальным Dijkstra.

Для предотвращения route flapping применяются:

- hysteresis;
- hold-down timer.

Для предотвращения петель и повторной доставки:

- hop limit;
- packet sequence number;
- bounded duplicate cache.

End-to-end `SessionID` и последовательность пакетов не зависят от next hop. Поэтому пользовательская сессия должна переживать переключение:

```text
UDP -> BLE
BLE -> UDP
neighbor A -> neighbor B
```

без повторного открытия API-level session.

## DPI и cover transports

Обфускация является отдельным transport layer и **не изменяет Noise-криптографию**. Такой принцип близок к архитектурному подходу [AmneziaWG](https://github.com/amnezia-vpn/amneziawg-go).

Cover transport включается отдельно для каждого IP transport и по умолчанию выключен для BLE и доверенной LAN.

### Публичные профили

| Profile | Назначение | TinyGo |
|---|---|---:|
| `None` | минимальный overhead | да |
| `Morph` | padding, изменяемые framing patterns, ограниченные junk packets | да |
| `HTTPS` | настоящий TLS 1.3 + долгоживущий HTTP/2 stream | нет |
| `HTTP3` | настоящий QUIC / HTTP/3 | нет |
| `DNS` | аварийный DNS-over-UDP fallback | частично / gateway-first |

### Morph

`Morph` может изменять:

- размеры кадров;
- padding;
- допустимые framing headers;
- timing в ограниченных пределах;
- ограниченное количество junk packets.

Цель Morph — снизить устойчивость простых сигнатурных DPI-классификаторов без попытки имитировать конкретный прикладной протокол.

### HTTPS

HTTPS cover использует:

- настоящий TLS 1.3;
- собственный домен;
- сертификаты, переданные приложением;
- долгоживущий HTTP/2 stream;
- скрытый endpoint.

Endpoint подтверждается nonce + HMAC от `NetworkKey`.

Replay cache принадлежит отдельному actor.

Неавторизованные запросы передаются приложению через decoy HTTP handler и должны выглядеть как обычный веб-сайт.

### HTTP/3

HTTP/3 cover использует настоящий QUIC / HTTP/3 поверх UDP.

HTTP/3-зависимости должны быть полностью изолированы от TinyGo core.

### DNS

DNS-cover работает только через подконтрольный authoritative subdomain.

Он формирует настоящие:

- DNS query;
- DNS response;
- EDNS records;
- TXT payload.

Поддерживаются:

- прямое обращение к authoritative server;
- прохождение через recursive resolver.

DNS-cover — медленный аварийный fallback, а не основной mesh transport.

К нему **не применяется** требование 85% throughput.

Ограничения документируются явно: классический DNS/UDP имеет небольшие сообщения и request/response model — [RFC 1035](https://www.rfc-editor.org/info/rfc1035/). EDNS расширяет допустимый UDP payload — [RFC 6891](https://www.rfc-editor.org/info/rfc6891/), но длительный высокоэнтропийный DNS tunnel нельзя обещать как статистически неотличимый для современного DPI.

## Целевые платформы

### Desktop / server

- Linux;
- macOS;
- Windows;
- Go 1.24 / 1.25 / 1.26.

### Embedded

Основная acceptance platform:

- Raspberry Pi Pico W;
- TinyGo 0.41+.

Pico W поддерживает:

- UDP;
- BLE;
- Morph;
- общий encrypted mesh core.

Сильные HTTPS / HTTP3 cover profiles выполняются desktop/server peers или gateways.

## Milestones

### M0 — API skeleton

- отдельная ветка v3;
- модуль `github.com/matveynator/netchan/v3`;
- базовые типы;
- actor model;
- wire framing;
- generated codec prototype;
- Linux Go integration.

### M1 — Secure direct transport

- UDP;
- neighbor discovery;
- Noise neighbor handshake;
- end-to-end session;
- replay protection;
- ARQ;
- 4 КиБ messages.

### M2 — Mesh

- link-state gossip;
- signed topology;
- Dijkstra;
- primary / backup next hop;
- multi-hop до 32 nodes;
- route failover без разрыва session.

### M3 — BLE + Pico W

- BLE discovery;
- GATT fragmentation;
- credits;
- BLE ARQ;
- UDP ↔ BLE failover;
- TinyGo firmware build;
- Pico W memory limits.

### M4 — DPI cover transports

- Morph;
- HTTPS;
- HTTP/3;
- decoy handler;
- active probing protection;
- DNS fallback.

### M5 — API freeze candidate

- минимум 3 независимые интеграции;
- включая Pico W;
- fuzzing;
- interoperability matrix;
- published benchmarks;
- security review.

### M6 — Stable v3

Условия:

- API freeze;
- независимый crypto audit;
- acceptance tests зелёные;
- отсутствие несовместимых API changes в течение 12 месяцев;
- golden interoperability tests между поддерживаемыми wire versions.

После этого выпускается `v3.0.0`.

## Release policy

Prerelease versions:

```text
v3.0.0-alpha.1
v3.0.0-alpha.2
...
v3.0.0-beta.1
...
v3.0.0-rc.1
v3.0.0
```

Каждый prerelease должен иметь:

- wire-version negotiation;
- golden interoperability tests;
- опубликованные latency / throughput / RAM / flash benchmarks;
- changelog несовместимостей wire protocol;
- regression gate.

Upstream proposals готовятся только после минимум трёх независимых интеграций, включая Pico W, и 12 месяцев без несовместимых изменений публичного API.

Само принятие upstream зависит от maintainers и не является гарантируемым результатом.

TinyGo при этом продолжает использовать NetChan как обычный сторонний Go module. Такой подход соответствует официальной документации TinyGo по [package organization](https://tinygo.org/docs/concepts/package-organization/).

## Связанная страница

Подробные acceptance criteria, test matrix и performance gates вынесены отдельно:

➡️ [NetChan v3 — Acceptance, Security & Performance](./NetChan-v3-Acceptance.md)
