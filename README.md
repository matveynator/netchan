[![GoDoc](https://godoc.org/github.com/matveynator/netchan?status.svg)](https://godoc.org/github.com/matveynator/netchan?flush=1)

# Welcome to the netchan Wiki!

<p align="right">
    <img align="right" property="og:image" src="https://repository-images.githubusercontent.com/710838463/86ad7361-2608-4a70-9197-e66883eb9914" width="30%">
</p>


## Overview
`netchan` stands as a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

## General Goals and Principles:
1. **Redundancy, Failover and Recovery**: [Design the system to automatically detect network channel failures and switch to a backup network channel. Create several network channels for each go channel to provide redundancy.*(Specific to network reliability, focusing on handling channel failures and maintaining network stability through redundancy and failover mechanisms.)*](/wiki/RedundancyFailoverandRecovery.md)

2. **Bidirectional Client-Server Role**: [Each client in the network will also function as a server, and vice versa. This dual role enhances network resilience and decentralizes communication. *(Unique in its emphasis on each network node's dual role as both client and server, contributing to the network's decentralized structure.)*](/wiki/BidirectionalClient-ServerRole.md)

3. **Unified Application Architecture**: [By using netchan you are designing the network application in such a way that each client/server becomes part of a cohesive, unified application (cluster), enhancing collaboration and data flow. *(This is about the overarching architectural principle where using "netchan" leads to the creation of a unified network application, enhancing collaboration and data flow.)*](/wiki/UnifiedApplicationArchitecture.md)

4. **Ease of Use**: [netchan's interface is developer-friendly and mimics standard Go channel operations, simplifying network interactions  as the underlying complexities of the network interactions are abstracted away.](/wiki/EaseofUse.md)

5. **Secure by Default**: [The library should employ modern encryption techniques, as well as reliable practices for authentication and authorization.*(Emphasizes the importance of modern encryption and robust authentication and authorization practices.)*](/wiki/SecurebyDefault.md)

6. **Scalability and High Performance**: [Designed for distributed systems, ensuring high throughput, scalability, and optimized for low overhead and rapid data transfer.*(Addresses the library's capability to handle large-scale distributed systems efficiently.)*](/wiki/ScalabilityandHighPerformance.md)

7. **Adherence to CSP Principles**: [Full compliance with the Communicating Sequential Processes (CSP) model.*(Ensures compliance with the Communicating Sequential Processes model, a key aspect of concurrent programming.)*](/wiki/AdherencetoCSPPrinciples.md)

8. **Adherence to Principles of Pure Go Programming**: [Adherence to the principles of pure Go programming.*(Highlights adherence to the core principles and idioms of Go programming.)*](/wiki/PrinciplesofPureGoProgramming.md)



## Getting Started
To embark on your journey with `netchan`, install the library using `go get`:

`go get -u github.com/matveynator/netchan`

## Usage Example:

```
package main

import (
  "fmt"
  "github.com/matveynator/netchan"
  "time"
)

func main() {
  go server()

  go client()
  go client()

  for {
    time.Sleep(1 * time.Second)
  }
}

// server
func server() {
  send, receive, err := netchan.Listen("127.0.0.1:9999")
  if err != nil {
    fmt.Println(err)
  }

  //receiving and sending goroutine
  for {
    select {
    case message := <-receive:
      fmt.Printf("Server received: %s\n", message)
      //Returning received message to any ready client.
      send <- message
    }
  }
}

func client() {
  send, receive, err := netchan.Dial("127.0.0.1:9999")
  if err != nil {
    fmt.Println(err)
    return
  }

  //sending goroutine
  go func() {
    for {
      message := fmt.Sprintf("Current unixtime: %d", time.Now().UnixNano())
      send <- message
      fmt.Printf("Client sent: %s\n", message)
      time.Sleep(3 * time.Second)
    }
  }()

  //receiving goroutine
  for {
    select {
    case message := <-receive:
      fmt.Printf("Client received: %v\n", message)
    }
  }
}
```

## Documentation
- [v1.0 Plan](wiki/v1-plan.md)
- Usage Examples

## Community and Support
Should you have inquiries or suggestions, feel free to open an [issue](https://github.com/matveynator/netchan/issues) in our GitHub repository.

## License
`netchan` is distributed under the BSD-style License. For detailed information, please refer to the [LICENSE](https://github.com/matveynator/netchan/blob/master/LICENSE) file.

