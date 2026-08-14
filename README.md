# “Quantum” Network Channels in Go — Protocol v2

[![Go Reference](https://pkg.go.dev/badge/github.com/matveynator/netchan/v2.svg)](https://pkg.go.dev/github.com/matveynator/netchan/v2)

## Введение

Каналы Go просты не потому, что только переносят значения. Они соединяют
независимые процессы, синхронизируют их и передают ответственность за данные.
После успешной отправки значение логически исчезает у отправителя и появляется
у получателя. Именно это свойство здесь называется «квантовым».

NetChan переносит ту же модель между процессами и компьютерами:

```go
connection.Send <- message
message = <-connection.Receive
```

В протоколе v2 сетевой канал умеет передавать значения, reply channels,
долгоживущие session channels и каналы отмены. Он сохраняет порядок сообщений,
создаёт обратное давление, восстанавливается после временного обрыва и отличает
приём сетевым узлом от фактического чтения удалённой goroutine.

Все ограничения, перечисленные в README первой версии протокола, устранены в
v2. При этом NetChan не скрывает физику распределённой системы: сеть требует
кодирования и временной копии, а два независимых планировщика Go не могут
выполнить один атомарный `select`. Эти границы выражены явными каналами и
состояниями протокола, а не потерей сообщений или скрытым общим состоянием.

> **Статус проекта:** v2 — текущая стабильная major-версия. Новые выпуски
> следуют SemVer, проходят обязательные security checks и попадают в `main`
> только через pull request.

## Почему «Quantum» Network Channels

Внутри одного процесса runtime Go выступает hypervisor каналов: он знает, какая
goroutine отправляет значение, какая готова его прочитать и в какой момент
передаётся право продолжить работу.

Между компьютерами общего runtime нет. В NetChan v2 роль network hypervisor
выполняют логическая сессия и протокол подтверждений:

1. отправитель передаёт значение локальному actor NetChan;
2. значение кодируется и регистрируется на удалённой стороне;
3. удалённое приложение читает его из обычного Go-канала;
4. подтверждение чтения возвращается отправителю;
5. временная закодированная копия освобождается.

На wire этот переход выражен последовательностью:

```text
DATA → PREPARED → DELIVERED → RELEASE
```

Поэтому «исчезновение» означает переход логического владения. Отправитель после
передачи больше не изменяет отправленные maps, slices и данные по указателям;
получатель становится новым владельцем после чтения. Физическая копия нужна
сети для восстановления после обрыва и живёт только до подтверждения.

## Что изменилось с первой версии

| Ограничение protocol v1 | Реализация в protocol v2 |
|---|---|
| Отправка не синхронизировалась с удалённым чтением | `Deliver` завершается только после чтения удалённым приложением |
| При сетевой ошибке сообщение могло потеряться | Журнал неподтверждённых сообщений, ACK, replay и дедупликация |
| Передавались в основном значения | В сообщениях передаются направленные reply, session и cancellation channels |
| Не было сетевой отмены задачи | Закрытие переданного `Done` распространяется на удалённую сторону |
| Буфер был частью неявного поведения | `Config.Capacity` и ограниченное окно протокола создают явный backpressure |
| Использовался Gob | Собственный ограниченный codec на основе `encoding/binary` |
| Физический обрыв завершал обмен | Логическая сессия сохраняется и переподключает транспорт |
| Не было строгой модели владения | `DATA → PREPARED → DELIVERED → RELEASE` удерживает данные до подтверждения |
| Закрытый вложенный канал мог оставить состояние | Terminal barrier освобождает capability после завершения всех родительских сообщений |

## Обзор API

У библиотеки две точки входа:

```go
listener, err := netchan.Listen[Message](address)
connection, err := netchan.Dial[Message](address)
```

`Dial` возвращает одно логическое соединение. `Listen` возвращает listener, а
каждый подключившийся клиент появляется в его обычном Go-канале `Channels`:

```go
connection := <-listener.Channels
```

У listener также есть `Errors`, `Done` и фактически занятый `Address`. Последний
особенно удобен при `Listen[Message]("127.0.0.1:0")`, когда порт выбирает
операционная система.

Обе стороны получают один и тот же тип `*netchan.Channel[T]`:

```go
connection.Send       // chan<- T
connection.Receive    // <-chan T
connection.Errors     // <-chan error
connection.Done       // <-chan struct{}
```

`Send` и `Receive` разделены намеренно. Если вернуть один двусторонний `chan T`,
локальный отправитель сможет встретиться с локальным получателем напрямую и
значение вообще не попадёт в сеть. Два направленных канала сохраняют обычный
синтаксис Go и однозначную сетевую границу.

## Установка

```bash
go get github.com/matveynator/netchan/v2@latest
```

```go
import "github.com/matveynator/netchan/v2"
```

## Самый простой сервер

Сервер принимает клиентов и запускает для каждого независимую goroutine.

```go
package main

import (
	"log"

	"github.com/matveynator/netchan/v2"
)

func serveConnection(connection *netchan.Channel[string]) {
	defer connection.Close()

	for message := range connection.Receive {
		connection.Send <- "Сервер получил: " + message
	}
}

func main() {
	listener, err := netchan.Listen[string]("127.0.0.1:9876")
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	for connection := range listener.Channels {
		go serveConnection(connection)
	}
}
```

## Самый простой клиент

```go
package main

import (
	"fmt"
	"log"

	"github.com/matveynator/netchan/v2"
)

func main() {
	connection, err := netchan.Dial[string]("127.0.0.1:9876")
	if err != nil {
		log.Fatal(err)
	}

	connection.Send <- "Привет"
	reply := <-connection.Receive
	fmt.Println(reply)

	close(connection.Send)
	<-connection.Done
}
```

Это минимальная форма. В долгоживущем приложении отправку и получение следует
объединять с `Done` через обычный `select`.

## `select`, `close` и `range`

Все публичные направления NetChan являются настоящими каналами Go. Для них не
нужны специальные методы чтения и записи.

```go
func exchange(connection *netchan.Channel[string], outgoing string) (string, bool) {
	select {
	case connection.Send <- outgoing:
	case <-connection.Done:
		return "", false
	}

	select {
	case incoming, open := <-connection.Receive:
		return incoming, open
	case <-connection.Done:
		return "", false
	}
}
```

Для мягкого завершения владелец закрывает отправляющую сторону и ждёт `Done`:

```go
func closeConnection(connection *netchan.Channel[string]) {
	close(connection.Send)
	<-connection.Done
}
```

Эквивалентная сокращённая форма:

```go
func closeConnectionWithMethod(connection *netchan.Channel[string]) error {
	return connection.Close()
}
```

После `Close` действуют обычные правила Go: приложение не должно отправлять в
закрытый канал. Сначала остановите goroutine-отправителей, затем закрывайте
принадлежащее вам направление.

## Какие свойства каналов Go реализованы в v2

### Статическая типизация

`Channel[T]`, `Listen[T]` и `Dial[T]` сохраняют конкретный тип сообщения. Ошибка
типа обнаруживается компилятором, а несовпадающая схема между peers отклоняется
при открытии сетевого канала.

### Направление передачи

Корневые `Send` и `Receive`, а также вложенные `chan<- T` и `<-chan T` явно
описывают, кто имеет право отправлять и получать. Двусторонний вложенный `chan T`
не передаётся, потому что он не определяет, какое право должно перейти другой
стороне.

### Блокировка и обратное давление

Обычная отправка блокируется, пока значение не примет локальный actor NetChan.
Если локальный буфер, сетевое окно или ограниченные очереди заполнены, actor
перестаёт принимать значения и оператор `<-` естественно создаёт backpressure.

### Порядок сообщений

Обычные отправки и `Deliver` входят в одну последовательность. Значения,
принятые от одной goroutine по порядку, выдаются удалённому приложению в том же
порядке. Порядок конкурентных отправителей определяется обычным планированием
каналов Go.

### Закрытие

`close(connection.Send)` сообщает, что локальных значений больше не будет.
После завершения логического канала закрываются `Receive`, `Errors` и `Done`,
поэтому доступны `range`, получение с `open` и ожидание в `select`.

### Буфер

По умолчанию `Send` небуферизирован. Локальную ёмкость можно задать отдельно на
каждой стороне:

```go
func dialBuffered(address string) (*netchan.Channel[string], error) {
	return netchan.Dial[string](address, netchan.Config{Capacity: 32})
}
```

`Capacity` относится только к локальному `Send`. `Receive` всегда остаётся
небуферизированным, чтобы протокол мог точно определить момент чтения для
`Deliver`.

### `select/case`

Одна goroutine может выбирать между локальными направлениями нескольких
сетевых каналов, таймером и каналом завершения тем же оператором `select`, что и
для любых каналов Go. Выбранный `case connection.Send <- message` означает
приём локальным NetChan, а строгий сетевой rendezvous выражается через
`Deliver`.

### Канал внутри канала

Направленный канал в сообщении передаёт не накопленные значения, а право
продолжить общение. Это основной механизм reply channels, session channels и
удалённой отмены без таблицы клиентов и общего mutable state.

## Обычная отправка и строгий rendezvous

Оператор `<-` невозможно переопределить в Go-библиотеке:

```go
connection.Send <- message
```

Эта операция завершается, когда значение принял локальный процесс NetChan. С
этого момента отправитель передал владение и не должен менять значение.

Если нужно дождаться именно чтения удалённой goroutine, используется `Deliver`:

```go
func sendAndWait(connection *netchan.Channel[string], message string) error {
	return connection.Deliver(message)
}
```

`Deliver` возвращает `nil` только после чтения из удалённого `Receive`. При
окончательном закрытии он возвращает `netchan.ErrChannelClosed`. Ошибка
кодирования конкретного значения также возвращается напрямую.

## Канал внутри канала: задача, ответ и отмена

Каждая задача может создать собственный reply channel. Обработчику не нужны
адрес клиента, идентификатор запроса или общая таблица ожидающих результатов.

```go
type Result struct {
	Text string
}

type Task struct {
	Text  string
	Reply chan<- Result
	Done  <-chan struct{}
}
```

Клиент оставляет локальные каналы у себя и передаёт обработчику только
необходимые права:

```go
func requestTask(connection *netchan.Channel[Task], text string, cancel <-chan struct{}) (Result, bool) {
	reply := make(chan Result)
	done := make(chan struct{})
	defer close(done)

	task := Task{Text: text, Reply: reply, Done: done}
	select {
	case connection.Send <- task:
	case <-connection.Done:
		return Result{}, false
	}

	select {
	case result, open := <-reply:
		return result, open
	case <-cancel:
		return Result{}, false
	case <-connection.Done:
		return Result{}, false
	}
}
```

Обработчик отвечает непосредственно в канал задачи и одновременно наблюдает за
её жизненным циклом:

```go
func handleTask(connection *netchan.Channel[Task], task Task) {
	defer close(task.Reply)

	select {
	case <-task.Done:
		return
	default:
	}

	result := Result{Text: "Готово: " + task.Text}
	select {
	case task.Reply <- result:
	case <-task.Done:
	case <-connection.Done:
	}
}

func serveTasks(connection *netchan.Channel[Task]) {
	defer connection.Close()

	for task := range connection.Receive {
		go handleTask(connection, task)
	}
}
```

Если внешний `cancel` закрывается или соединение завершается, функция выходит и
через `defer` закрывает локальный `done`. Закрытие проходит через сеть, и
удалённый обработчик видит закрытый `task.Done`. Для долгой работы обработчик
проверяет его между шагами либо строит асинхронный streaming pipeline.

Вложенный канал активируется только после того, как приложение действительно
прочитало родительскую задачу. Непринятая задача не запускает скрытую работу.
Один живой канал можно передать в нескольких сообщениях: удалённая сторона
получит один и тот же proxy. После закрытия terminal barrier удерживает его до
разрешения всех ранее принятых родительских сообщений, а затем освобождает
capability на обеих сторонах.

## Долгоживущий session channel

Reply channel обычно живёт до одного ответа. Session channel остаётся внутри
отдельной goroutine и передаёт поток сообщений, пока одна из сторон его не
закроет.

```go
type Subscription struct {
	Events chan<- string
	Done   <-chan struct{}
}

func publishEvents(subscription Subscription, events <-chan string) {
	defer close(subscription.Events)

	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			select {
			case subscription.Events <- event:
			case <-subscription.Done:
				return
			}
		case <-subscription.Done:
			return
		}
	}
}
```

Такой процесс не хранит subscriber state: всё право контакта находится в
переданных каналах.

## Переподключение и гарантии доставки

После успешного `Dial` приложение продолжает пользоваться тем же `Channel`,
даже если физический TCP/TLS transport временно заменяется. NetChan v2:

- повторяет неподтверждённые frames после переподключения;
- не выдаёт одно значение приложению повторно;
- различает транспортный ACK и фактическое чтение приложением;
- сохраняет порядок обычных отправок и `Deliver`;
- удерживает значение до `RELEASE`;
- восстанавливает ссылки на вложенные каналы;
- закрывает сессию при несовместимом или потерянном resume-state.

Попытки восстановления используют возрастающую задержку до пяти секунд.
Отключённая логическая сессия хранится пять минут. Если peer уже потерял её
состояние, канал окончательно закрывается, а `Errors` может сообщить
`netchan.ErrSessionExpired`. Отказ принять новую сессию возвращается из `Dial`
как `netchan.ErrSessionRejected`.

Гарантия отсутствия повторной выдачи действует в пределах живой логической
сессии. После полного перезапуска обоих процессов exactly-once требует
прикладной транзакции или журнала в постоянном хранилище.

## Ошибки и аварийное завершение

`connection.Errors` и `listener.Errors` — диагностические best effort каналы.
Надёжным terminal-сигналом является закрытие соответствующего `Done`.

Если обычной отправкой принято значение, которое невозможно закодировать,
NetChan сообщает ошибку и завершает канал: значение не отбрасывается молча.
`Deliver` возвращает такую ошибку напрямую.

Мягкий `Close` дожидается уже принятых значений. Если доставка больше не нужна,
используется явная аварийная остановка:

```go
func abortConnection(connection *netchan.Channel[string]) error {
	return connection.Abort()
}
```

`Abort` отменяет накопленные передачи и не обещает доставить их peer.

## Физические границы сети

У protocol v2 больше нет функциональных ограничений, перечисленных для v1.
Остаются свойства, которые невозможно устранить на уровне Go-библиотеки:

- два компьютера не имеют общего планировщика goroutines, поэтому атомарный
  межмашинный `select` невозможен;
- wire transport требует кодирования и временной физической копии;
- процесс, потерявший оперативную память, не может восстановить exactly-once
  без прикладного постоянного хранилища;
- конечная память требует ограниченных очередей и проверки входных размеров.

NetChan делает эти границы явными через `Deliver`, `Done`, `Errors`, закрытие
каналов, backpressure и состояние логической сессии.

## TLS

Вызовы без `Config` создают самоподписанный TLS-сертификат. Трафик шифруется, но
личность peer не проверяется. Такой режим подходит для localhost и тестирования,
но не защищает production-соединение от подмены.

Production-конфигурация использует обычный `*tls.Config`:

```go
func listenWithTLS(address string, serverTLS *tls.Config) (*netchan.Listener[string], error) {
	return netchan.Listen[string](address, netchan.Config{TLS: serverTLS})
}

func dialWithTLS(address string, clientTLS *tls.Config) (*netchan.Channel[string], error) {
	return netchan.Dial[string](address, netchan.Config{TLS: clientTLS})
}
```

Для этого примера нужны импорты `crypto/tls` и
`github.com/matveynator/netchan/v2`. Если `MinVersion` не задан, NetChan выбирает
TLS 1.3.

## Бинарное кодирование

Protocol v2 использует собственный ограниченный формат на основе
`encoding/binary` и не использует Gob.

Поддерживаются:

- boolean, числа и строки;
- структуры с экспортируемыми полями;
- массивы, slices, maps и указатели;
- направленные `chan<- T` и `<-chan T` внутри сообщений;
- типы с `MarshalBinary` и `UnmarshalBinary`.

Обе стороны корневого канала используют одинаковый конкретный `T`. Направление
вложенного канала входит в схему. Интерфейсы, функции, `unsafe.Pointer`,
циклические указатели и двусторонние вложенные `chan T` не передаются.

Generics используются только для типизированной публичной границы. `reflect`
скрыт внутри codec и нужен, чтобы построить схему произвольного `T` и найти в
структуре направленные каналы. Тип может полностью управлять своим форматом
через `MarshalBinary` и `UnmarshalBinary`.

## Защитные пределы

Лимиты v2 не являются недостающими возможностями. Они не позволяют
недоверенному peer или медленному получателю бесконечно занимать память:

- wire frame — не более 16 МиБ;
- до 16 вложенных каналов в одном значении;
- до 256 одновременно живых capability в логической сессии;
- до 1024 неподтверждённых frames;
- отдельные бюджеты для закодированных, подготовленных и декодированных
  значений;
- ограниченная глубина и размер коллекций при декодировании.

После terminal barrier закрытая capability освобождается, поэтому число
последовательно выполненных задач не ограничено числом одновременно живых
каналов.

## Совместимость

Go module v2 и generic API намеренно несовместимы с v1. Обе стороны
одного соединения должны использовать protocol v2 и совпадающие схемы
сообщений.

| Версия | Import path | Статус |
|---|---|---|
| v1 | `github.com/matveynator/netchan` | Архивная, без новых исправлений |
| v2 | `github.com/matveynator/netchan/v2` | Текущая поддерживаемая версия |

Версии публикуются неизменяемыми Git-тегами в формате SemVer. Patch-версия
исправляет ошибки без изменения API, minor-версия совместимо добавляет API,
а несовместимое изменение требует новой major-версии и import path. Последний
стабильный выпуск всегда доступен по [постоянной ссылке](https://github.com/matveynator/netchan/releases/latest).

Protocol v2 является второй опубликованной версией. Промежуточные реализации,
которые во время разработки получали номера 2, 3 и 4, были попытками одного
перехода от protocol v1 и не считаются отдельными выпущенными протоколами.

## TODO и будущие транспорты

Текущая реализация использует TCP/TLS. Идеи поддержки QUIC поверх UDP,
Bluetooth RFCOMM, BLE, автоматического nearby discovery и duplex QR-обмена
через camera/display отложены и пока не являются частью публичного API.

Согласованные архитектурные решения и этапы сохранены в [TODO.md](TODO.md).

## Проверка проекта

```bash
go test ./...
go test -race ./...
go vet ./...

NETCHAN_NETWORK_TEST=1 go test -run 'TestPublic(ListenAndDial|ConfigAndListenerLifecycle|ClientsRemainIndependent|ListenerCloseTerminatesActiveChannel|ListenerCloseInterruptsIncompleteHandshake)$' -v .
```

## Сообщество и поддержка

Вопросы, предложения и сообщения об ошибках можно публиковать в
[GitHub Issues](https://github.com/matveynator/netchan/issues). Особенно полезны
реальные сценарии с временными обрывами, длинными streaming sessions и каналами
внутри сообщений.

## Похожие проекты

- [Netchan old version](https://github.com/matveynator/netchan-old) — развитие первоначальной идеи Rob Pike;
- [Docker Libchan](https://github.com/docker/libchan) — сетевой message-passing interface;
- [GraftJS/jschan](https://github.com/graftjs/jschan) — похожая модель каналов для JavaScript;
- [Mat Ryer/Vice](https://github.com/matryer/vice) — каналы Go в распределённой среде.

## Лицензия

NetChan распространяется по [BSD 3-Clause License](LICENSE).
