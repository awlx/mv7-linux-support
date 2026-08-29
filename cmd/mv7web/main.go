// Command mv7web serves a web console for the Shure MV7+ microphone.
//
// It opens the MV7+ vendor HID console (/dev/hidrawN), serves a sleek web UI
// on 127.0.0.1, and bridges the UI to the device over WebSocket.
package main

import (
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/awlx/mv7-linux-support/internal/hidraw"
	"github.com/awlx/mv7-linux-support/internal/mv7"
	"github.com/awlx/mv7-linux-support/internal/server"
	"github.com/awlx/mv7-linux-support/internal/webui"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "listen address")
	openUI := flag.Bool("open", false, "open the web console in the default browser")
	flag.Parse()
	url := "http://" + *addr
	if *openUI {
		stopOtherInstances()
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		if *openUI && errors.Is(err, syscall.EADDRINUSE) {
			openBrowser(url)
			return
		}
		log.Fatalf("cannot listen on %s: %v", *addr, err)
	}
	defer listener.Close()

	dev, err := hidraw.Find()
	if err != nil {
		log.Fatalf("MV7+ not found: %v", err)
	}
	defer dev.Close()

	client := mv7.NewDevice(dev)
	srv := server.New(client)

	// Serve the embedded web UI.
	sub, err := webui.FS()
	if err != nil {
		log.Fatalf("cannot embed web UI: %v", err)
	}
	http.Handle("/", http.FileServer(http.FS(sub)))
	http.HandleFunc("/ws", srv.HandleWS)

	// The device worker serializes all device access and keeps the UI live.
	stop := make(chan struct{})
	go srv.Run(2*time.Second, stop)

	log.Printf("MV7+ web console listening on %s", url)
	if *openUI {
		openBrowser(url)
	}
	if err := http.Serve(listener, nil); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func openBrowser(url string) {
	if err := exec.Command("xdg-open", url).Start(); err != nil {
		log.Printf("cannot open browser: %v", err)
	}
}

func stopOtherInstances() {
	output, err := exec.Command(
		"pgrep", "-u", strconv.Itoa(os.Geteuid()), "-x", "mv7web",
	).Output()
	if err != nil {
		return
	}
	var pids []int
	for _, field := range strings.Fields(string(output)) {
		pid, err := strconv.Atoi(field)
		if err == nil && pid != os.Getpid() {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			pids = append(pids, pid)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(pids) > 0 && time.Now().Before(deadline) {
		remaining := pids[:0]
		for _, pid := range pids {
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				remaining = append(remaining, pid)
			}
		}
		pids = remaining
		if len(pids) > 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
}
