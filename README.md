# Welcome to the netchan Wiki!

<p align="right">
    <img align="right" property="og:image" src="https://repository-images.githubusercontent.com/710838463/86ad7361-2608-4a70-9197-e66883eb9914" width="30%">
</p>


## Overview
`netchan` stands as a robust library for the Go programming language, offering convenient and secure abstractions for network channel interactions. Inspired by [Rob Pike’s initial concept](https://github.com/matveynator/netchan-old), it aims to deliver an interface that resonates with the simplicity and familiarity of Go’s native channels.

## General Goals and Principles
1. **Ease of Use**: [The library’s interface should be intuitively understandable and reflect standard channel operations in Go.*(Focuses on the user-friendly interface and intuitive operation aligned with Go's standard channel operations.)*](/wiki/EaseofUse.md)
2. **Secure by Default**: [The library should employ modern encryption techniques, as well as reliable practices for authentication and authorization.*(Emphasizes the importance of modern encryption and robust authentication and authorization practices.)*](/wiki/SecurebyDefault.md)
3. **Scalability and High Performance**: [Designed for distributed systems, ensuring high throughput, scalability, and optimized for low overhead and rapid data transfer.*(Addresses the library's capability to handle large-scale distributed systems efficiently.)*](/wiki/ScalabilityandHighPerformance.md)
4. **Adherence to CSP Principles**: [Full compliance with the Communicating Sequential Processes (CSP) model.*(Ensures compliance with the Communicating Sequential Processes model, a key aspect of concurrent programming.)*](/wiki/AdherencetoCSPPrinciples.md)
5. **Principles of Pure Go Programming**: [Adherence to the principles of pure Go programming.*(Highlights adherence to the core principles and idioms of Go programming.)*](/wiki/PrinciplesofPureGoProgramming.md)
6. **Redundancy, Failover and Recovery**: [Design the system to automatically detect network channel failures and switch to a backup channel. Create several network channels for each go channel to provide redundancy.*(Specific to network reliability, focusing on handling channel failures and maintaining network stability through redundancy and failover mechanisms.)*](/wiki/RedundancyFailoverandRecovery.md)
7. **Bidirectional Client-Server Role**: [Each client in the network will also function as a server, and vice versa. This dual role enhances network resilience and decentralizes communication. *(Unique in its emphasis on each network node's dual role as both client and server, contributing to the network's decentralized structure.)*](/wiki/BidirectionalClient-ServerRole.md)
8. **Unified Application Architecture**: [By using netchan you are designing the network application in such a way that each client/server becomes part of a cohesive, unified application, enhancing collaboration and data flow. *(This is about the overarching architectural principle where using "netchan" leads to the creation of a unified network application, enhancing collaboration and data flow.)*](/wiki/UnifiedApplicationArchitecture.md)



## Getting Started
To embark on your journey with `netchan`, install the library using `go get`:
```
go get -u github.com/matveynator/netchan
```
## Usage Example:

```
```

## Documentation
- [v1.0 Plan](wiki/v1-plan.md)
- Usage Examples
- API References
- Secure by default

## Community and Support
Should you have inquiries or suggestions, feel free to open an [issue](https://github.com/matveynator/netchan/issues) in our GitHub repository.

## License
`netchan` is distributed under the BSD-style License. For detailed information, please refer to the [LICENSE](https://github.com/matveynator/netchan/blob/master/LICENSE) file.

