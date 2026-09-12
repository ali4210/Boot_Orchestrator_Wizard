package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type DiscoveredPeer struct {
	IP        string   `json:"ip"`
	Port      int      `json:"port"`
	Payloads  []string `json:"payloads"`
	SizeBytes int64    `json:"size"`
	URL       string   `json:"url"`
}

type pingResponse struct {
	Status   string   `json:"status"`
	Payloads []string `json:"payloads"`
	Size     int64    `json:"size"`
}

func GetLocalOutboundIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func StartPeerSeeder(targetPath string, port int, stopChan <-chan struct{}) error {
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("invalid seeder target path: %w", err)
	}

	mux := http.NewServeMux()
	var payloadList []string
	var totalSize int64

	if info.IsDir() {
		// Index all bootable files inside the directory
		_ = filepath.Walk(targetPath, func(path string, f os.FileInfo, walkErr error) error {
			if walkErr == nil && !f.IsDir() {
				ext := strings.ToLower(filepath.Ext(path))
				if ext == ".iso" || ext == ".img" || ext == ".zip" || ext == ".xz" {
					rel, _ := filepath.Rel(targetPath, path)
					payloadList = append(payloadList, rel)
					totalSize += f.Size()
				}
			}
			return nil
		})

		// Expose file server
		mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(targetPath))))
	} else {
		fileName := filepath.Base(targetPath)
		payloadList = append(payloadList, fileName)
		totalSize = info.Size()

		mux.HandleFunc("/files/"+fileName, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fileName))
			http.ServeFile(w, r, targetPath)
		})
	}

	// Ping probe metadata endpoint
	mux.HandleFunc("/orchestrator/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pingResponse{
			Status:   "ok",
			Payloads: payloadList,
			Size:     totalSize,
		})
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		<-stopChan
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	return server.ListenAndServe()
}

func ScanSubnetForSeeders(ctx context.Context, timeout time.Duration) []DiscoveredPeer {
	localIP, err := GetLocalOutboundIP()
	if err != nil || localIP == "127.0.0.1" {
		return nil
	}

	parts := strings.Split(localIP, ".")
	if len(parts) != 4 {
		return nil
	}
	subnetPrefix := fmt.Sprintf("%s.%s.%s.", parts[0], parts[1], parts[2])

	var peers []DiscoveredPeer
	var mu sync.Mutex
	var wg sync.WaitGroup

	client := &http.Client{Timeout: 500 * time.Millisecond}

	for i := 1; i <= 254; i++ {
		targetIP := fmt.Sprintf("%s%d", subnetPrefix, i)
		wg.Add(1)

		go func(ip string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			default:
			}

			pingURL := fmt.Sprintf("http://%s:8080/orchestrator/ping", ip)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL, nil)
			if err != nil {
				return
			}

			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				var pr pingResponse
				_ = json.NewDecoder(resp.Body).Decode(&pr)
				resp.Body.Close()

				firstPayload := ""
				if len(pr.Payloads) > 0 {
					firstPayload = pr.Payloads[0]
				}

				mu.Lock()
				peers = append(peers, DiscoveredPeer{
					IP:        ip,
					Port:      8080,
					Payloads:  pr.Payloads,
					SizeBytes: pr.Size,
					URL:       fmt.Sprintf("http://%s:8080/files/%s", ip, firstPayload),
				})
				mu.Unlock()
			}
		}(targetIP)
	}

	wg.Wait()
	return peers
}
