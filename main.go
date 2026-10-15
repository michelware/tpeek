/*
 * TPeek - TCP Proxy Debugger
 * Copyright (C) 2025  Michel.F
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 */

package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// version is shown by "tpeek -version" and in the startup banner.
// Update it here before each release:
//
const version = "1.0.0"

// dialTimeout is the maximum time spent reaching the target. Without it, a target
// that silently drops packets (machine down, firewall) would keep the client
// waiting for the OS default, which can be around 2 minutes.
//
// There is deliberately NO timeout on reads: a MySQL or Redis connection pool keeps
// idle connections open for hours, and a debugging tool must not close them.
const dialTimeout = 10 * time.Second

// Colors are variables, not constants, so main() can empty them when colors are
// turned off. With empty strings the same code prints plain text:
//
//	tpeek -l :8000 -t :3306 > trace.log   →   trace.log has no "\033[34m" codes
var (
	Blue  = "\033[34m"
	Green = "\033[32m"
	Reset = "\033[0m"
)

// outputLock makes sure a block is printed in one piece. Many goroutines print at
// the same time (2 per connection). Without the lock, two blocks could mix:
//
//	┌── #1 🔵 CLIENT >> ── 12:00:01.000000 ── 10 bytes
//	┌── #2 🟢 SERVER << ── 12:00:01.001000 ── 5 bytes     ← block #2 cuts block #1
//	│ SELECT 1;
//	│ OK...
//
// Each block is fully built in memory first, then written while holding the lock.
var outputLock sync.Mutex

func main() {
	// Options are read into plain variables, so the rest of the program only
	// handles values (no *listenAddr everywhere).
	var listenAddr, targetAddr string
	var useHex, noColor, showVersion bool
	flag.StringVar(&listenAddr, "l", "", "Local address and port to listen on (e.g. 0.0.0.0:8080)")
	flag.StringVar(&targetAddr, "t", "", "Target address and port (e.g. MySQL, Redis : 127.0.0.1:3306)")
	flag.BoolVar(&useHex, "hex", false, "Enable full hexadecimal dump mode")
	flag.BoolVar(&noColor, "nocolor", false, "Disable colors in the output")
	flag.BoolVar(&showVersion, "version", false, "Print the version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("tpeek", version)
		return
	}

	if listenAddr == "" || targetAddr == "" {
		flag.Usage()
		os.Exit(1)
	}

	// Connection events (open, close, errors) go to stdout with the data blocks, and
	// use the same microsecond clock. A trace saved with
	//
	//	tpeek -l :8000 -t :3306 > trace.log
	//
	// then contains EVERYTHING, in order, on one timeline:
	//
	//	15:04:05.120034 ✅ #1 New connection from 127.0.0.1:58022
	//	┌── #1 🔵 CLIENT >> ── 15:04:05.120310 ── +276µs ── 47 bytes @ 0
	//
	// Microseconds matter on a local network: a request/response round trip on
	// localhost often takes less than a millisecond.
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// Colors are only useful in a terminal. They are turned off when:
	//   - the user passes -nocolor
	//   - the NO_COLOR environment variable exists (convention: https://no-color.org)
	//   - stdout is not a terminal (file redirection, | grep, | less...)
	// Example: "tpeek -l :8000 -t :3306 | grep SELECT" → plain text, no ANSI codes.
	// Windows note: the legacy console (conhost) doesn't render ANSI codes, Windows
	// Terminal (default on Windows 11) does. With the legacy console, use -nocolor.
	stdoutInfo, err := os.Stdout.Stat()
	isTerminal := err == nil && stdoutInfo.Mode()&os.ModeCharDevice != 0
	if noColor || os.Getenv("NO_COLOR") != "" || !isTerminal {
		Blue, Green, Reset = "", "", ""
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("❌ Fatal: Could not listen on %s: %v", listenAddr, err)
	}

	mode := "PLAIN TEXT"
	if useHex {
		mode = "HEXADECIMAL"
	}
	fmt.Println("🚀 TPeek TCP Proxy Initialized (" + version + ")")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf(" 🔵 LISTEN  : %s\n", listenAddr)
	fmt.Printf(" 🟢 TARGET  : %s\n", targetAddr)
	fmt.Printf(" 🛠️  MODE    : %s\n", mode)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("📡 Waiting for incoming connections...")

	// Clean stop on Ctrl+C (or "kill"). Taking the output lock waits for the block
	// being printed to finish, so we never stop in the middle of a colored block
	// and leave the terminal blue or green.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		outputLock.Lock()
		fmt.Println(Reset + "\n👋 TPeek stopped.")
		os.Exit(0)
	}()

	// Every connection gets a number (#1, #2, #3...) shown on each block and each
	// message. With a pool of 5 MySQL connections, it tells which answer belongs to
	// which query.
	// This counter is only changed in this loop (a single goroutine): no lock needed.
	connID := 0

	for {
		clientConn, err := listener.Accept()
		if err != nil {
			// A repeated error (e.g. "too many open files" when the limit of open
			// files is reached) would make this loop spin at 100% CPU and flood the
			// terminal. A short pause gives the system time to recover.
			log.Printf("⚠️  Accept Error: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		connID++
		go handleProxy(clientConn, targetAddr, connID, useHex)
	}
}

// handleProxy connects a client to the target and copies data in both directions.
//
// The two directions run AT THE SAME TIME and never wait for each other (full
// duplex): one runs in its own goroutine, the other runs right here (handleProxy is
// already a goroutine). A server can push data while the client is silent:
//
//	time   goroutine (server → client)      handleProxy (client → server)
//	  0    Read(server)... waiting           Read(client)... waiting
//	  1    ← "+OK welcome"   forwarded       (still waiting, not blocked)
//	  2    ← "push message"  forwarded       ← "SUBSCRIBE news" forwarded
//
// Only the END is sequential: sockets are closed once BOTH directions are done.
// This way:
//   - no goroutine is left behind when a connection ends,
//   - a direction that finishes early doesn't cut the other one while it still has
//     data to deliver (see "half-close" in pipe()).
//
// How a client leaving is handled:
//   - clean close (FIN): pipe() gets EOF, passes it on to the server, waits for the
//     server to finish too, then everything is closed.
//   - crash / reset (RST): pipe() gets an error and closes both sockets right away,
//     which also unblocks the other direction.
//   - silent disappearance (cable unplugged, laptop asleep): no packet arrives.
//     Go enables TCP keepalive by default on accepted and dialed connections. On an
//     idle connection, the OS probes the peer and reports an error after about
//     2-3 minutes without answer. If data was being sent when the peer vanished,
//     the OS retransmission timeout applies instead, which can be much longer
//     (around 15 minutes on Linux). Either way we then fall back to the "reset" case.
func handleProxy(clientConn net.Conn, target string, id int, useHex bool) {
	// Every block shows the time elapsed since this moment ("+1.234ms"), so the
	// delay between a request and its answer can be read directly.
	start := time.Now()

	log.Printf("✅ #%d New connection from %s", id, clientConn.RemoteAddr())

	serverConn, err := net.DialTimeout("tcp", target, dialTimeout)
	if err != nil {
		log.Printf("❌ #%d Target connection failed (%s): %v", id, target, err)
		clientConn.Close()
		return
	}

	// The full path of the connection, with the local port used to reach the target.
	// The target sees the proxy, not the real client, so this port is what shows in
	// the target's own logs, e.g. MySQL "SHOW PROCESSLIST" → Host: 127.0.0.1:58023.
	//
	//	🔗 #1 127.0.0.1:58022 → [127.0.0.1:58023] → 127.0.0.1:3306
	log.Printf("🔗 #%d %s → [%s] → %s", id, clientConn.RemoteAddr(), serverConn.LocalAddr(), serverConn.RemoteAddr())

	// Each pipe() returns the number of bytes it forwarded. The server → client
	// direction sends its total through the channel when it is done.
	done := make(chan int)
	go func() {
		done <- pipe(serverConn, clientConn, id, "🟢 SERVER <<", Green, useHex, start)
	}()
	clientBytes := pipe(clientConn, serverConn, id, "🔵 CLIENT >>", Blue, useHex, start)
	serverBytes := <-done

	clientConn.Close()
	serverConn.Close()

	// Summary of the whole connection:
	//
	//	🔌 #1 Connection closed after 1.523s ── client sent 47 bytes, server sent 1290 bytes
	log.Printf("🔌 #%d Connection closed after %v ── client sent %d bytes, server sent %d bytes",
		id, time.Since(start).Round(time.Millisecond), clientBytes, serverBytes)
}

// pipe copies everything coming from src to dst, prints each read, and returns the
// total number of bytes forwarded.
func pipe(src, dst net.Conn, id int, label string, color string, useHex bool, start time.Time) int {
	buffer := make([]byte, 16384)

	// offset = position of the next byte in this direction's stream (0 = first byte
	// ever sent in this direction). Each block shows the offset of its first byte:
	//
	//	┌── #1 🟢 SERVER << ── ... ── 78 bytes @ 0
	//	┌── #1 🟢 SERVER << ── ... ── 11 bytes @ 78
	//
	// Binary protocols often announce lengths ("the next 89 bytes are one packet"):
	// the offset tells where a message starts and ends, even when it is split over
	// several blocks.
	offset := 0

	for {
		n, err := src.Read(buffer)
		if n > 0 {
			data := buffer[:n]

			// Forward first, print after: the data never waits for the terminal.
			// With a slow terminal (SSH, big hex dumps), printing first would add
			// delay to every message and the timestamps would partly measure the
			// terminal speed.
			// Note: the next Read still waits for the print to finish, so a very slow
			// terminal can still reduce the throughput (not the delay of a message).
			_, writeErr := dst.Write(data)

			printBlock(id, label, color, data, useHex, start, offset)
			offset += n

			if writeErr != nil {
				// The other side can't receive anymore (closed or crashed).
				// Closing both sockets also unblocks the other direction.
				log.Printf("⚠️  #%d %s write failed: %v", id, label, writeErr)
				src.Close()
				dst.Close()
				return offset
			}
		}

		if err == io.EOF {
			// Half-close: src has finished SENDING, but may still wait for an answer.
			// Typical example:
			//
			//	printf 'PING\n' | nc -N 127.0.0.1 8000
			//
			// nc sends "PING", says "I'm done writing" (EOF), then waits for the
			// server's answer. So we only pass this "done writing" on to dst
			// (CloseWrite) and let the other direction run until its own end.
			// Closing everything here would drop the answer and change the behavior
			// of what we are observing.
			//
			// The event is printed because WHO stops first is often the key of a bug:
			//
			//	📭 #1 🟢 SERVER << finished sending (EOF) after 30.002s
			//	→ the server closed the connection by itself, maybe an idle timeout.
			log.Printf("📭 #%d %s finished sending (EOF) after %v", id, label, time.Since(start).Round(time.Millisecond))
			if tcpConn, ok := dst.(*net.TCPConn); ok {
				tcpConn.CloseWrite()
			}
			return offset
		}

		if err != nil {
			// Real error (connection reset, network down...) or the other direction
			// just closed everything. Either way, close both sockets: this also
			// unblocks the other direction if it is waiting in a Read().
			//
			// A real error is printed, it explains an abrupt end:
			//
			//	💥 #1 🔵 CLIENT >> read error: connection reset by peer
			//	→ the client was killed or crashed, it didn't close cleanly.
			//
			// net.ErrClosed means "we closed this socket ourselves" (from the other
			// direction): it's the normal end, not worth a message.
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("💥 #%d %s read error: %v", id, label, err)
			}
			src.Close()
			dst.Close()
			return offset
		}
	}
}

// printBlock builds the whole block in memory, then writes it in one go.
//
// Header: connection, direction, time, time since the connection opened, size of
// this read, and offset of its first byte in this direction's stream.
//
// Text mode example (HTTP, a text protocol):
//
//	┌── #3 🔵 CLIENT >> ── 14:32:01.482113 ── +3.102ms ── 44 bytes @ 0
//	│ GET /api/users HTTP/1.1
//	│ Host: localhost
//	│
//	└────────────────────────────────────────
//
// Hex mode works for ANY protocol: it shows the raw bytes, whatever they mean.
// Example with Redis, where the protocol uses "\r\n" and lengths ("$4" = the next
// 4 bytes are the value) that text mode would hide:
//
//	┌── #3 🔵 CLIENT >> ── 14:32:01.482113 ── +3.102ms ── 14 bytes @ 0
//	│ 00000000  2a 31 0d 0a 24 34 0d 0a  50 49 4e 47 0d 0a        |*1..$4..PING..|
//	└────────────────────────────────────────
func printBlock(id int, label string, color string, data []byte, useHex bool, start time.Time, offset int) {
	now := time.Now()
	timestamp := now.Format("15:04:05.000000")
	elapsed := now.Sub(start).Round(time.Microsecond)

	body := ""
	if useHex {
		body = hex.Dump(data)
	} else {
		body = toPrintable(data)
	}

	// Every line (text or hex) gets its "│ " prefix.
	// Lines are collected in a slice and joined once: a 16 KB hex dump is about
	// 1000 lines, and repeated "out += ..." would copy the whole string each time.
	lines := []string{}
	lines = append(lines, fmt.Sprintf("\n%s┌── #%d %s ── %s ── +%v ── %d bytes @ %d%s",
		color, id, label, timestamp, elapsed, len(data), offset, Reset))
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		lines = append(lines, color+"│"+Reset+" "+line)
	}
	lines = append(lines, color+"└"+strings.Repeat("─", 40)+Reset+"\n")

	outputLock.Lock()
	fmt.Print(strings.Join(lines, "\n"))
	outputLock.Unlock()
}

// toPrintable turns raw bytes into text that is safe to show in a terminal.
// It works byte by byte (not on Unicode characters), so every byte is accounted for.
//
// Rules:
//   - printable ASCII (32 to 126) → shown as is
//   - "\n"                        → real line break
//   - "\r" followed by "\n"       → skipped ("\r\n" gives a single line break)
//   - anything else               → "." (ONE dot per byte, always)
//
// Examples:
//
//	"GET / HTTP/1.1\r\nHost: localhost\r\n"  →  GET / HTTP/1.1
//	                                             Host: localhost
//	"h\xc3\xa9llo" ("héllo", é = 2 bytes)    →  h..llo
//	"\x1b[31mRED" (ANSI escape code)         →  .[31mRED
//
// "Anything else" includes ESC (27): a server can't send control codes that would
// break or hijack the terminal.
// If a "\r\n" is split between two reads, the "\r" shows as "." at the end of the
// first block: that's the real fragmentation, on purpose.
// To see the exact bytes (CRLF vs LF, binary headers...), use -hex.
func toPrintable(data []byte) string {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		b := data[i]
		if b == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			continue
		}
		if b == '\n' || (b >= 32 && b <= 126) {
			out = append(out, b)
			continue
		}
		out = append(out, '.')
	}
	return string(out)
}
