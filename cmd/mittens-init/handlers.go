package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// oauthCallbackResponse is the JSON body returned by POST /open for OAuth URLs.
type oauthCallbackResponse struct {
	CallbackID string `json:"callbackID"`
}

// runOpenURL implements the xdg-open shim — forwards URLs to the host broker.
// For OAuth flows, it polls for the intercepted callback and replays it.
func runOpenURL() int {
	if len(os.Args) < 2 {
		return 1
	}
	url := os.Args[1]
	if url == "" {
		return 1
	}

	cfg := loadConfig()
	bc := newBrokerClient(cfg)
	if bc == nil {
		return 0
	}

	// Send URL to host broker for opening.
	respBody, _, err := bc.postWithBody("/open", "", url)
	if err != nil {
		return 0
	}

	// If the host broker returned a callbackID, this is an OAuth URL.
	// Block on /await-callback/{id} instead of polling /login-callback.
	if strings.Contains(respBody, "callbackID") {
		var cb oauthCallbackResponse
		if jsonErr := json.Unmarshal([]byte(respBody), &cb); jsonErr == nil && cb.CallbackID != "" {
			// Block for up to 125 seconds (broker timeout is 2 minutes; 125s
			// gives enough headroom for transport latency).
			body, code, awaitErr := bc.getWithTimeout("/await-callback/"+cb.CallbackID, 125*time.Second)
			if awaitErr == nil && code == http.StatusOK && body != "" {
				// Replay to the AI CLI's callback server inside the container.
				client := &http.Client{Timeout: 5 * time.Second}
				resp, replayErr := client.Get(body)
				if replayErr == nil {
					resp.Body.Close()
				}
			}
		}
	}

	return 0
}

// runNotify implements the notify.sh shim — sends notifications to the host broker.
func runNotify() int {
	cfg := loadConfig()
	bc := newBrokerClient(cfg)
	if bc == nil {
		return 0
	}

	event := "unknown"
	message := ""
	if len(os.Args) > 1 {
		event = os.Args[1]
	}
	if len(os.Args) > 2 {
		message = os.Args[2]
	}

	payload, _ := json.Marshal(map[string]string{
		"container": cfg.ContainerName,
		"event":     event,
		"message":   message,
	})

	_, _ = bc.post("/notify", "application/json", string(payload))
	return 0
}

// runClipboard implements the xclip shim — reads clipboard images from the host broker.
func runClipboard() int {
	cfg := loadConfig()
	bc := newBrokerClient(cfg)
	if bc == nil {
		return 1
	}

	// Parse xclip-compatible flags.
	target := ""
	output := false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-selection":
			i++ // skip value
		case "-t", "-target":
			if i+1 < len(args) {
				target = args[i+1]
				i++
			}
		case "-o":
			output = true
		}
	}

	if !output {
		return 0
	}

	switch target {
	case "TARGETS":
		body, code, err := bc.get("/clipboard")
		if err == nil && code == http.StatusOK && len(body) > 0 {
			fmt.Print("image/png")
		}
	case "text/plain":
		fmt.Print("")
	default:
		if strings.HasPrefix(target, "image/") {
			body, code, err := bc.get("/clipboard")
			if err != nil || code != http.StatusOK || len(body) == 0 {
				return 1
			}
			os.Stdout.WriteString(body)
		} else {
			return 1
		}
	}

	return 0
}

// runX11ClipboardSync mirrors synced PNG files into the X11 clipboard selection.
func runX11ClipboardSync() int {
	clipboardImage := "/tmp/mittens-clipboard/clipboard.png"
	if len(os.Args) > 1 {
		clipboardImage = os.Args[1]
	}

	xclipBin := envOr("XCLIP_BIN", "/usr/local/bin/xclip-real")
	var lastHash string
	var ownerCmd *exec.Cmd

	for {
		time.Sleep(time.Second)

		data, err := os.ReadFile(clipboardImage)
		if err != nil {
			continue
		}

		hash := fmt.Sprintf("%x", md5.Sum(data))
		if hash == lastHash {
			continue
		}

		// Kill the previous clipboard owner.
		if ownerCmd != nil && ownerCmd.Process != nil {
			_ = ownerCmd.Process.Kill()
			_ = ownerCmd.Wait()
		}

		f, err := os.Open(clipboardImage)
		if err != nil {
			continue
		}

		ownerCmd = exec.Command(xclipBin, "-selection", "clipboard", "-t", "image/png", "-i")
		ownerCmd.Stdin = f
		if err := ownerCmd.Start(); err != nil {
			f.Close()
			continue
		}
		f.Close()

		lastHash = hash
	}
}
