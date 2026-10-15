# TPeek

**See exactly what goes through any TCP connection. One small Go binary. No dependencies, no privileges.**

TPeek is a transparent TCP proxy for debugging. You put it between a client and a server, it forwards every byte untouched and prints each read in your terminal: which connection, which direction, when, how many bytes, and where they sit in the stream. It works with any protocol over TCP — HTTP, MySQL, PostgreSQL, Redis, SMTP, MQTT, a homemade binary protocol — because it doesn't try to understand it: it shows you the bytes.

```
14:32:01.478920 ✅ #1 New connection from 127.0.0.1:52144
14:32:01.479310 🔗 #1 127.0.0.1:52144 → [127.0.0.1:52145] → 127.0.0.1:8080

┌── #1 🔵 CLIENT >> ── 14:32:01.479602 ── +682µs ── 70 bytes @ 0
│ GET /api/users HTTP/1.1
│ Host: localhost
│ Accept: application/json
│
└────────────────────────────────────────

┌── #1 🟢 SERVER << ── 14:32:01.483115 ── +4.195ms ── 107 bytes @ 0
│ HTTP/1.1 200 OK
│ Content-Type: application/json
│ Content-Length: 36
│
│ {"users":[{"id":1,"name":"michel"}]}
└────────────────────────────────────────
14:32:01.483907 📭 #1 🔵 CLIENT >> finished sending (EOF) after 5ms
14:32:01.484102 📭 #1 🟢 SERVER << finished sending (EOF) after 5ms
14:32:01.484240 🔌 #1 Connection closed after 5ms ── client sent 70 bytes, server sent 107 bytes
```

---

## Quick start

```bash
# MySQL listens on 3306. TPeek listens on 8000 and forwards to it.
tpeek -l 127.0.0.1:8000 -t 127.0.0.1:3306
```

Then point your application at `127.0.0.1:8000` instead of `127.0.0.1:3306`. Nothing else changes for the app or the server: every byte is forwarded as is, and you see all of it.

---

## Reading the output

### Data blocks

One block = one `Read()` on the socket, exactly as your application would receive it.

```
┌── #1 🔵 CLIENT >> ── 14:32:01.479602 ── +682µs ── 70 bytes @ 0
     │   │              │                  │         │         │
     │   │              │                  │         │         └ offset of the first byte in this
     │   │              │                  │         │           direction's stream (0 = very first byte)
     │   │              │                  │         └ size of this read
     │   │              │                  └ time since the connection was accepted
     │   │              └ wall clock, microseconds
     │   └ direction: 🔵 CLIENT >> (client → server) or 🟢 SERVER << (server → client)
     └ connection number
```

- **`+682µs` / `+4.195ms`**: the gap between a request and its answer is the difference between the two (here ~3.5 ms of server time).
- **`@ offset`**: binary protocols often announce a length ("the next 89 bytes are one packet"). The offset tells you where each message starts and ends, even when it is split across several reads:

  ```
  ┌── #4 🟢 SERVER << ── ... ── 16384 bytes @ 0
  ┌── #4 🟢 SERVER << ── ... ── 16384 bytes @ 16384
  ┌── #4 🟢 SERVER << ── ... ── 2741 bytes @ 32768     ← 35509 bytes in total
  ```

### Connection events

| Event | Meaning |
|-------|---------|
| `✅ #1 New connection from 127.0.0.1:52144` | A client connected to TPeek |
| `🔗 #1 client → [127.0.0.1:52145] → target` | TPeek reached the target. The port in brackets is the one **the target sees**: use it to find this connection in the target's logs (e.g. MySQL `SHOW PROCESSLIST`) |
| `❌ #1 Target connection failed` | The target is down or unreachable (10 s timeout); the client is disconnected |
| `📭 #1 🔵 CLIENT >> finished sending (EOF) after 5ms` | That side closed **cleanly**. The other direction keeps running until it is done too (half-close is passed through) |
| `💥 #1 🔵 CLIENT >> read error: connection reset by peer` | That side ended **abruptly** (crash, kill, network failure) |
| `⚠️ #1 ... write failed` | Data couldn't be delivered: the receiving side is gone |
| `🔌 #1 Connection closed after 1.523s ── client sent 47 bytes, server sent 1290 bytes` | Summary of the connection |

Who stops first is often the answer to a bug:
`📭 #1 🟢 SERVER << finished sending (EOF) after 30.002s` → the server closed the connection by itself, probably an idle timeout.

### Plain text mode (default)

Made for text protocols (HTTP, SMTP, Redis, IRC, JSON lines...). Rules, byte by byte:

- printable ASCII is shown as is
- `\n` is a line break, `\r\n` is a single line break
- **every other byte is shown as one `.`** — an `é` (2 bytes in UTF-8) shows as `..`, so the display always matches the byte count

Control characters, including terminal escape codes, are never sent to your terminal: a server can't mess up or hijack your shell.

### Hex mode (`-hex`)

Works for **any** protocol: it shows the raw bytes, whatever they mean. Use it for binary protocols (MySQL, PostgreSQL, MQTT, gRPC, homemade formats) or when the exact bytes matter (CRLF vs LF, length prefixes, flags). Example with Redis, where `$4` means "the next 4 bytes are the value":

```
┌── #1 🔵 CLIENT >> ── 18:07:32.861274 ── +92.91ms ── 14 bytes @ 0
│ 00000000  2a 31 0d 0a 24 34 0d 0a  50 49 4e 47 0d 0a        |*1..$4..PING..|
└────────────────────────────────────────

┌── #1 🟢 SERVER << ── 18:07:32.861274 ── +92.91ms ── 7 bytes @ 0
│ 00000000  2b 50 4f 4e 47 0d 0a                              |+PONG..|
└────────────────────────────────────────
```

---

## Usage examples

```bash
# Database (MySQL, PostgreSQL...): binary protocol → hex
tpeek -l 127.0.0.1:8000 -t 127.0.0.1:3306 -hex

# Redis: text protocol → plain text is usually enough
tpeek -l 127.0.0.1:8000 -t 127.0.0.1:6379

# Plain HTTP API (no TLS)
tpeek -l 127.0.0.1:8080 -t api.internal:80

# Save a full trace (events + data, one timeline) to a file
tpeek -l 127.0.0.1:8000 -t 127.0.0.1:3306 > trace.log

# Only keep what matters
tpeek -l 127.0.0.1:8000 -t 127.0.0.1:3306 | grep -E "#3|💥"
```

Colors are turned off automatically when the output is not a terminal (file, pipe) or when `NO_COLOR` is set.

> ⚠️ **Listening on `0.0.0.0` exposes the target to your whole network.**
> `tpeek -l 0.0.0.0:8000 -t 127.0.0.1:6379` makes a Redis that was only reachable from the machine itself (often without a password) reachable by anyone who can reach port 8000. Use `127.0.0.1` unless the client really runs on another machine.

---

## Command line flags

| Flag       | Description                                                                 | Default |
|------------|-----------------------------------------------------------------------------|---------|
| `-l`       | Address and port TPeek listens on (required)                               |         |
| `-t`       | Target address and port (required)                                          |         |
| `-hex`     | Show data as a hexadecimal dump instead of plain text                      | `false` |
| `-nocolor` | Disable colors                                                              | `false` |
| `-version` | Print the version and exit |         |

Stop with `Ctrl+C`.

---

## Installation

### Precompiled binaries

Download the binary for your system from the [releases page](https://github.com/michelware/tpeek/releases/latest):

| System                                | File                      |
|---------------------------------------|---------------------------|
| Linux x86_64                          | `tpeek-linux-amd64`       |
| Linux ARM64 (Raspberry Pi, ARM servers) | `tpeek-linux-arm64`     |
| macOS Intel                           | `tpeek-macos-amd64`       |
| macOS Apple Silicon (M1, M2...)       | `tpeek-macos-arm64`       |
| Windows x86_64                        | `tpeek-windows-amd64.exe` |

Linux:

```bash
sudo curl -L https://github.com/michelware/tpeek/releases/latest/download/tpeek-linux-amd64 -o /usr/local/bin/tpeek
sudo chmod +x /usr/local/bin/tpeek
```

macOS (Apple Silicon):

```bash
sudo curl -L https://github.com/michelware/tpeek/releases/latest/download/tpeek-macos-arm64 -o /usr/local/bin/tpeek
sudo chmod +x /usr/local/bin/tpeek
```

Windows: download `tpeek-windows-amd64.exe` and run it from a terminal (Windows Terminal recommended for colors).

### From source (Go 1.23+)

```bash
git clone https://github.com/michelware/tpeek
cd tpeek
go build -o tpeek
```

To build every platform at once: `./build.sh` (binaries go to `build/`).

---

## TPeek vs other tools

There are many great tools to look at network traffic. They answer different questions.

| | **TPeek** | **Wireshark / tcpdump** | **socat -v** | **mitmproxy / Charles / Fiddler / Burp** | **tcpflow / ngrep** |
|---|---|---|---|---|---|
| How it works | Proxy (client points to it) | Passive packet capture | Proxy | Proxy | Passive packet capture |
| Admin / root rights needed | **No** | Yes (libpcap / Npcap) | No | No | Yes |
| Any TCP protocol | **Yes** | Yes | Yes | Mostly HTTP(S) | Yes |
| What you see | What the app reads (`Read()` calls) | Network packets | What the app reads | Decoded HTTP requests | Reassembled streams / matching packets |
| Connection numbers, offsets, timings per block | **Yes** | Yes (with filters and columns) | Timestamps, sizes, offsets (no connection numbers) | Per request | Partial |
| Shows who closed and how (EOF / reset) | **Yes, in plain words** | Yes (FIN / RST flags, to interpret) | In debug logs (`-d -d`) | No | No |
| Decodes known protocols (MySQL, HTTP/2...) | No (text or hex) | **Yes, hundreds** | No | **HTTP, very well** | No |
| Decrypts TLS / HTTPS | No | With the keys | No | **Yes** | No |
| Terminal-safe output | **Yes** | N/A (GUI) / partial | Partial | N/A (GUI) | Partial |
| Install | **One binary, all OSes** | Package + capture driver | Package (awkward on Windows) | App / Python package + certificate | Package (Linux/macOS) |

### Where TPeek shines

- **No privileges, no driver, no setup.** Drop one binary on a server, a laptop or in a container and run it. No `sudo`, no Npcap, no certificate to install.
- **Any protocol, from the first byte.** No dissector needed: if it goes over TCP, you see it, in text or hex.
- **Reads, not packets.** A packet capture shows how the network cut the data. TPeek shows how your application receives it, one `Read()` at a time — which is what your parsing code has to handle.
- **Built for reading a story.** Numbered connections, time since the connection opened, stream offsets, clean vs abrupt closes and a summary per connection: the important facts are written in plain words, on one timeline, in a file you can grep.
- **Doesn't change what it observes.** Bytes are forwarded before being printed, half-closed connections are passed through, idle connections are never cut.

### When to use something else

- **You can't change where the client connects** → TPeek needs the client to point to it. Use a passive capture (Wireshark, tcpdump).
- **The traffic is TLS (HTTPS, MySQL with SSL...)** → TPeek shows encrypted bytes. Use mitmproxy / Fiddler for HTTPS, or put TPeek on the unencrypted side (e.g. between a TLS-terminating proxy and the app), or disable TLS in a test environment.
- **You want a protocol decoded field by field** → Wireshark's dissectors do it for hundreds of protocols.
- **You care about packets, retransmissions, TCP windows** → that's below the application level: Wireshark / tcpdump.

---

## Limitations

- **No TLS decryption** (see above).
- **One target per instance.** To watch two services, run two TPeek instances on two ports.
- **The target sees TPeek's address, not the client's.** IP allow-lists or MySQL accounts tied to a host (`'user'@'10.0.0.5'`) see the proxy's address instead. The `🔗` line gives the mapping.
- **The terminal sets the pace.** Data is forwarded before being printed, so a message is never delayed by the display, but a very slow terminal (huge hex dumps over SSH) can reduce throughput. Redirect to a file in that case.
- **Colors on Windows.** The legacy console (`conhost`) doesn't render colors: use Windows Terminal or `-nocolor`. In Git Bash (mintty), the output isn't seen as a terminal, so colors are off.
- **Clock precision on Windows.** Timestamps are in microseconds, but the Windows clock ticks in steps: several close events can show the same time. On Linux and macOS the precision is real.

---

## License

GNU Affero General Public License v3.0 (AGPL-3.0)
