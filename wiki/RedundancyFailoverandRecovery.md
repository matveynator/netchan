[Go back](https://github.com/matveynator/netchan#general-goals-and-principles)

"Redundancy, Failover and Recovery" refers to specific network resilience strategies. Let's break down each term:

1. **Redundancy**: This involves creating multiple network channels for each Go channel within your library. The idea here is to have backup channels in place, so if one channel encounters problems (like a network disruption), others are readily available to take over. This redundancy ensures that the network communication is less likely to be interrupted, as there are multiple pathways for data transfer.

2. **Failover**: This is the process by which the system automatically detects a failure in one of the network channels and then switches to a backup channel. The key aspect of failover is its automatic nature; it quickly identifies when a channel becomes unreliable or fails and then seamlessly transitions to a functioning backup channel without requiring manual intervention. This ensures continuous network operation even in the event of certain failures.

3. **Recovery**: Recovery refers to the system's ability to restore normal operations after a failure. In the context of your network channels, this would mean not only switching to a backup channel during a failure (failover) but also bringing the failed channel back online if possible, or reallocating resources to maintain the desired level of redundancy.

In summary, "Redundancy, Failover and Recovery" in our "netchan" library points to a robust design that aims to maintain continuous and reliable network communication. By implementing multiple backup channels (redundancy), ensuring the system can automatically switch to these backups during failures (failover), and having mechanisms to restore normal operations post-failure (recovery), your library is designed to be resilient against network disruptions.
