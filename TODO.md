# TODO: универсальные транспорты NetChan

## Статус

Идея исследована и отложена. Текущая реализация NetChan protocol v2
поддерживает только TCP/TLS. Описанные ниже QUIC/UDP, Bluetooth, BLE, nearby и
QR-видеотранспорты пока не реализованы и не имеют назначенного срока.

Этот документ сохраняет принятые архитектурные решения, чтобы при возвращении
к задаче не начинать проектирование заново. Наличие транспорта в этом списке не
означает поддержку в публичном API.

## Цель

NetChan должен уметь сохранять одну модель каналов Go при смене физического
способа связи. Приложение по-прежнему вызывает только `netchan.Listen` и
`netchan.Dial`, а protocol v2 продолжает отвечать за sessions, delivery,
reconnect, channel capabilities и передачу владения.

Планируемые адреса:

```text
tcp://127.0.0.1:9876
quic://127.0.0.1:9876
rfcomm://<device-id>/<service>
ble://<device-id>/<service>
qr://<peer-id>/<service>
nearby://<peer-id>/<service>
nearby:///<service>
```

Адрес без URI scheme должен остаться совместимой TCP-формой.

## Общие архитектурные решения

- Package-level entrypoints остаются только `Listen` и `Dial`.
- Transport-independent ядро остаётся в `netchan.go`.
- OS-specific adapters размещаются в `pkg/transport/<name>` и отделяются build
  tags.
- Каждый transport предоставляет ядру надёжный упорядоченный `net.Conn` либо
  строит эквивалентный virtual stream поверх packet link.
- Protocol остаётся v2. Внутренние transport headers имеют собственные версии.
- Transport implementations общаются с ядром через channels; mutable global
  registry, shared maps и mutex в архитектуре не используются.
- TLS identity остаётся общей границей безопасности для разных transports.
- Peer ID вычисляется из публичного ключа TLS-сертификата.
- `nearby:///<service>` разрешается только при единственном найденном peer. При
  нескольких результатах возвращается явная ambiguous-peer error.
- Reconnect заново обнаруживает доступные endpoints и может восстановить ту же
  logical session через другой transport.

## QUIC и UDP

Raw UDP не должен ослаблять гарантии NetChan: datagrams могут теряться,
дублироваться и приходить не по порядку. Для передачи channel values выбран
QUIC, который предоставляет надёжные ordered streams, retransmission, flow
control и congestion control поверх UDP.

Принятые решения:

- использовать один bidirectional QUIC stream как physical NetChan connection;
- использовать ALPN `netchan/2`;
- не добавлять второй TLS-слой поверх QUIC;
- использовать raw UDP только для link-local discovery;
- для IP discovery исследовать mDNS/DNS-SD services `_netchan._udp.local` и
  `_netchan._tcp.local`;
- при возобновлении задачи поднять минимальную версию модуля с Go 1.19 до Go
  1.24 и использовать актуальный `quic-go`, а не закреплять устаревшую версию.

## Bluetooth Classic RFCOMM

RFCOMM ближе всего к TCP, потому что предоставляет двусторонний stream и
service discovery через SDP. RFCOMM connection должен оборачиваться TLS 1.3 и
передаваться существующему protocol v2 без нового application framing.

Планируемые adapters:

- Linux: BlueZ/AF_BLUETOOTH;
- Windows: WinRT RFCOMM StreamSocket;
- macOS: IOBluetooth RFCOMM;
- FreeBSD: Bluetooth socket layer и SDP;
- остальные ОС: компилируемый stub с явной unsupported-transport error.

## Bluetooth Low Energy

Для BLE `Listen` соответствует peripheral/GATT server, а `Dial` —
central/GATT client. Service UUID должен детерминированно выводиться из NetChan
namespace и имени service.

Из-за ограниченного MTU и callback-based API поверх BLE необходим отдельный
reliable packet layer:

- fragmentation и ordered reassembly;
- sequence и acknowledgement;
- duplicate suppression;
- bounded sliding window и backpressure;
- disconnect detection и reconnect;
- virtual stream, поверх которого выполняется TLS и protocol v2.

Реализацию следует начинать с Linux и Windows. Поддержку macOS нужно строить
отдельным native adapter: существующие Go BLE-библиотеки пока не дают одинаковой
central/peripheral функциональности на всех desktop-платформах.

## Nearby discovery и выбор пути

Один discovery actor должен объединять candidates из:

- mDNS/DNS-SD;
- Bluetooth SDP;
- BLE advertisements;
- QR HELLO frames.

Actor владеет таблицей `peer ID + service + transport`, удаляет записи после TTL
и передаёт dial actor только актуальные candidates. Default preference:

```text
QUIC → TCP → RFCOMM → BLE → QR
```

Candidates запускаются с небольшой задержкой друг относительно друга. Первый
transport, прошедший проверку peer identity, TLS и NetChan handshake, становится
единственным physical connection; остальные attempts отменяются через channels.

## Duplex QR-видеотранспорт

QR считается полноценным transport только тогда, когда обе стороны имеют
camera input и display output. Одностороннее видеонаблюдение не может
подтвердить `Deliver`, закрытие и отмену, поэтому не должно выдаваться как
обычный двусторонний `Channel`.

NetChan не должен самостоятельно открывать камеры, RTSP streams или окна.
Приложение передаёт captured images и получает готовые display images через
каналы в optional optical configuration.

Принятый первый вариант:

- QR Version 10, correction level M, изображение 512×512;
- binary link frame кодируется в Base45;
- не более 128 байт transport payload в одном QR frame;
- display interval по умолчанию 200 мс;
- один active optical peer на один набор camera/display channels;
- QR error correction восстанавливает повреждение внутри изображения;
- sequence, selective ACK и cyclic replay восстанавливают полностью
  пропущенные camera frames;
- после сборки ordered virtual stream выполняются TLS 1.3 и обычный handshake
  protocol v2.

Optical link frame должен содержать magic, link version, source и destination
peer IDs, connection ID, sequence, cumulative ACK, selective ACK bitmap,
payload length и CRC32C.

## Этапы будущей реализации

1. Выделить transport factory, URI parser и fake stream/packet transports,
   сохранив TCP compatibility.
2. Добавить QUIC и IP discovery через UDP/mDNS.
3. Построить duplex QR prototype на in-memory camera/display channels.
4. Добавить RFCOMM adapters для desktop-систем.
5. Добавить BLE reliable packet link и multipath reconnect.

Каждый этап должен быть самостоятельно проверяемым и не менять семантику
`Send`, `Receive`, `Deliver`, `Done`, nested channels и ownership protocol.

## Проверки при возобновлении

- contract tests для каждого `net.Conn`/`net.Listener` adapter;
- packet loss, duplication, reordering и fragmentation;
- exact-once receive и `Deliver` после reconnect;
- wrong-peer certificate rejection;
- ambiguous nearby peer;
- QR fixtures с blur, rotation, scaling и пропущенными frames;
- bounded memory и backpressure;
- fuzz URI, discovery, packet и QR frame decoders;
- `go test -count=10 ./...`, `go test -race ./...` и `go vet ./...`;
- cross-compilation для macOS, Linux, Windows, FreeBSD и OpenBSD на `amd64` и
  `arm64`;
- отдельная hardware matrix, не блокирующая обычные unit tests.

## Материалы исследования

- [Go network interfaces](https://pkg.go.dev/net)
- [QUIC RFC 9000](https://www.rfc-editor.org/info/rfc9000/)
- [quic-go documentation](https://quic-go.net/docs/)
- [Multicast DNS RFC 6762](https://datatracker.ietf.org/doc/html/rfc6762)
- [DNS-Based Service Discovery RFC 6763](https://datatracker.ietf.org/doc/html/rfc6763)
- [Windows Bluetooth RFCOMM](https://learn.microsoft.com/en-us/windows/apps/develop/devices-sensors/send-or-receive-files-with-rfcomm)
- [Apple IOBluetooth RFCOMM](https://developer.apple.com/documentation/iobluetooth/iobluetoothrfcommchannel)
- [FreeBSD Bluetooth](https://docs.freebsd.org/en/books/handbook/advanced-networking/)
- [Go Bluetooth support matrix](https://pkg.go.dev/tinygo.org/x/bluetooth)
- [QR Code capacity](https://www.qrcode.com/en/about/version.html)
- [QR Code error correction](https://www.qrcode.com/en/about/error_correction.html)

