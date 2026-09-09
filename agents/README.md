# agents — Python Behavior Agent SDK and runtime

ADR-0005 puts NPC and quest behavior in Python Behavior Agents that run *outside* the tick and speak
the same gRPC service as every other client. They are a fleet to operate, not a library the server
imports.

Outside the Go module deliberately: a Behavior Agent must not be able to reach into simulation state
except through the protocol.

First story to put source here: `AW-SRV-016`.
