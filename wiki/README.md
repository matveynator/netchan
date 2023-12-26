# Updated Development Roadmap for the "netchan" Library in Golang

## General Goals and Principles:
1. **Redundancy, Failover and Recovery**: [Design the system to automatically detect network channel failures and switch to a backup network channel. Create several network channels for each go channel to provide redundancy.*(Specific to network reliability, focusing on handling channel failures and maintaining network stability through redundancy and failover mechanisms.)*](/wiki/RedundancyFailoverandRecovery.md)

2. **Bidirectional Client-Server Role**: [Each client in the network will also function as a server, and vice versa. This dual role enhances network resilience and decentralizes communication. *(Unique in its emphasis on each network node's dual role as both client and server, contributing to the network's decentralized structure.)*](/wiki/BidirectionalClient-ServerRole.md)

3. **Unified Application Architecture**: [By using netchan you are designing the network application in such a way that each client/server becomes part of a cohesive, unified application (cluster), enhancing collaboration and data flow. *(This is about the overarching architectural principle where using "netchan" leads to the creation of a unified network application, enhancing collaboration and data flow.)*](/wiki/UnifiedApplicationArchitecture.md)

4. **Ease of Use**: [netchan's interface is developer-friendly and mimics standard Go channel operations, simplifying network interactions  as the underlying complexities of the network interactions are abstracted away.](/wiki/EaseofUse.md)

5. **Secure by Default**: [The library should employ modern encryption techniques, as well as reliable practices for authentication and authorization.*(Emphasizes the importance of modern encryption and robust authentication and authorization practices.)*](/wiki/SecurebyDefault.md)

6. **Scalability and High Performance**: [Designed for distributed systems, ensuring high throughput, scalability, and optimized for low overhead and rapid data transfer.*(Addresses the library's capability to handle large-scale distributed systems efficiently.)*](/wiki/ScalabilityandHighPerformance.md)

7. **Adherence to CSP Principles**: [Full compliance with the Communicating Sequential Processes (CSP) model.*(Ensures compliance with the Communicating Sequential Processes model, a key aspect of concurrent programming.)*](/wiki/AdherencetoCSPPrinciples.md)

8. **Adherence to Principles of Pure Go Programming**: [Adherence to the principles of pure Go programming.*(Highlights adherence to the core principles and idioms of Go programming.)*](/wiki/PrinciplesofPureGoProgramming.md)


## Package Structure
1. **Network Interaction Functions**
   - `Listen`: Handling incoming connections.
   - `Dial`: Initiating outgoing connections.
   - Both methods should utilize interfaces to facilitate future modifications and enhance functionality.
   - Implementation of a mechanism for file descriptor transmission to ensure graceful restart without losing current connections.

2. **Connection Management**
   - Automatic tracking of connected and disconnected clients.
   - Re-establishing connections in case of loss.
   - Identifying clients and corresponding channels using unique and secure keys.

3. **Buffer and Broadcast**
   - Implementing a network-level equivalent of Go’s channel buffer.
   - A handshake-broadcast mechanism to distribute initial tasks to all clients, with only those who respond first receiving tasks according to the queue size (buffer).
   - A “first come, first served” system to determine who receives a task.

4. **Channels and Encryption**
   - Initialization functions return a channel through which serviced channels are subsequently transmitted and received.
   - Using unique keys for each channel.
   - All network channels utilize maximum TLS encryption by default.
   - Optionally using shared encryption keys, blocking connection to the port in their absence.

5. **Storing Shared Encryption Keys**
   - Encryption keys, used for connecting to the server, are stored inside the binary file.
   - Utilizing obfuscation and key splitting, as well as the `go:embed’ format, to prevent decompilation from binary code.

6. **Interactive Connectivity Establishment (ICE) Integration and Network Channel RAID System**

   A. **ICE Integration**
      - Implement ICE for optimal pathfinding in data streams, ensuring robust and flexible network communication.
      - **Bidirectional Client-Server Role**: Each client in the network will also function as a server, and vice versa. This dual role enhances network resilience and decentralizes communication.
      - Gather local and public IP addresses for each client/server.
      - Perform connectivity checks and prioritize best connection paths.
      - Ensure security during the ICE process.
      - Implement distance vector routing

   B. **RAID-like Network Channel System (Focused on Channel Management and Resilience)**
      - **Multiple Channel Establishment**: Create several network channels for each connection to provide redundancy.
      - **Failover and Recovery**: Design the system to automatically detect channel failures and switch to a backup channel.

   C. **QUIC-like or QUIC integrated network via UDP**
      - **Integrate QUIC** inside netchan or create analogue network.


## Implementation
1. **Interfaces and Abstractions**: 
   - Defining interfaces for network operations, allowing for easy expansion and modification of functionality.

2. **Security and Encryption**: 
   - Integrating modern encryption and security libraries.
   - Implementing authentication and authorization mechanisms.

3. **State Management and Error Handling**: 
   - Monitoring connection status and managing errors.
   - Automatically recovering after connection loss.

--

>
> Here is a preliminary outline of the plan that I have prepared. I would be grateful if you could take the time to review it and enrich it with your suggestions, should it pique your interest.
>
> Looking ahead, I am considering the possibility of integrating drivers for proxy protocols, which would allow us to provide our longstanding protocols like SMTP/MQTT/gRPC/HTTP with access to the latest netchan data.
>
> Additionally, I am experiencing certain difficulties in understanding how to ensure the security of keys and optimize the client signing process at the time of compilation.
>
> Kind regards, 
> Matvey Gladkikh
