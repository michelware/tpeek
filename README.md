# TPeek

**Debug what your app actually receives over TCP. A lightweight Go binary. No dependencies.**

TPeek is a transparent TCP proxy that shows you, in real time, the exact bytes flowing between two applications. It sits silently between a client and a server, forwarding traffic untouched while printing every read to your terminal — block by block, with timestamps and direction.

```
[14:32:01.482] 🔵 Client → 47 bytes
  GET /api/users HTTP/1.1
  Host: localhost
  Accept: application/json

[14:32:01.489] 🟢 Server → 128 bytes
  HTTP/1.1 200 OK
  Content-Type: application/json
  Content-Length: 42

  {"users":[{"id":1,"name":"michel"}]}
```

---

## Why TPeek

### 1. You see what your app receives, not what the network carries

Sniffers like Wireshark or `tcpdump` show packets — fragmented, reassembled by the OS later. TPeek shows you exactly what arrives in your application's buffer, one `Read()` call at a time. This is what your code actually sees.

### 2. Built for binary protocols, not just HTTP

Toggle between plain text and full hexadecimal dump. Inspect MySQL handshakes, Redis commands, custom binary protocols, or proprietary wire formats without writing a custom parser.

```bash
tpeek -l 0.0.0.0:8000 -t 127.0.0.1:3306 -hex
```

### 3. Fragmentation-aware

Each block of data is printed exactly as received from the syscall — never merged, never reformatted. You see how the protocol fragments its messages, which is critical when debugging streaming protocols, slow clients, or chunked responses.

### 4. Millisecond timestamps

Every read is timestamped. You can immediately measure round-trip latency, identify slow responses, or detect timing bugs between client and server.

### 5. Safe for production debugging

Binary data is forwarded untouched. Only the terminal output is sanitized so unprintable bytes don't crash your shell. You can use TPeek in front of a real service without risk of corrupting the wire format.

### 6. Single binary. Zero runtime. Zero dependencies.

No Python, no Node, no Docker image, no library to install. One Go binary you drop on a server, a laptop, or inside a container.

---

## When to use TPeek

- You're integrating a third-party API and the docs don't match what's actually sent
- You need to reverse-engineer or debug a proprietary binary protocol
- You're comparing the wire behavior of two different database clients
- You want to see if a slow request is the client, the network, or the server
- You're debugging a microservice mesh and need a quick view of what one service sends to another

---

## Installation

### Linux (precompiled)

```bash
sudo wget https://github.com/michelphp/tpeek/releases/download/1.0.0/tpeek-linux -O /usr/local/bin/tpeek
sudo chmod +x /usr/local/bin/tpeek
```

### From source

```bash
git clone https://github.com/michelphp/tpeek
cd tpeek
go build -o tpeek
```

---

## Usage

### Inspect TCP traffic between an app and MySQL

```bash
tpeek -l 0.0.0.0:8000 -t 127.0.0.1:3306
```

Then point your application at `localhost:8000` instead of `localhost:3306`. Every query, every response, every byte — visible.

### Inspect a binary protocol (Redis, custom RPC, proprietary protocols)

```bash
tpeek -l 0.0.0.0:8000 -t 127.0.0.1:6379 -hex
```

### Inspect an HTTP API

```bash
tpeek -l 0.0.0.0:8080 -t api.example.com:80
```

---

## Command line flags

| Flag   | Description                              | Default |
|--------|------------------------------------------|---------|
| `-l`   | Local address and port to listen on      |         |
| `-t`   | Target service address and port          |         |
| `-hex` | Enable full hexadecimal dump mode        | `false` |

---

## Comparison

|                              | Wireshark / tcpdump | TPeek         |
|------------------------------|---------------------|---------------|
| View                         | Network packets     | App-level reads |
| Binary protocol friendly     | Requires dissectors | Hex mode native |
| Proxy mode (man-in-the-middle) | Manual setup      | One command   |
| Runtime dependencies         | libpcap + tools     | None          |
| Installation                 | Package + privileges| Drop one binary |
| Terminal-safe output         | N/A                 | Sanitized     |

TPeek is not a replacement for Wireshark. It's a different lens — closer to what your application sees, simpler to deploy.

---

## License

GNU Affero General Public License v3.0 (AGPL-3.0)
