// 测试应用提供真实就绪延迟、正常终止、Job 和卷读回，不伪造 Pod status。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func duration(name string) time.Duration {
	v, _ := strconv.Atoi(os.Getenv(name))
	return time.Duration(v) * time.Second
}
func main() {
	if os.Getenv("APP_MODE") == "job" {
		log.Print("job started")
		time.Sleep(duration("JOB_SECONDS"))
		log.Print("job completed")
		if code, err := strconv.Atoi(os.Getenv("JOB_EXIT_CODE")); err == nil && code != 0 {
			os.Exit(code)
		}
		return
	}
	started := time.Now()
	hostname, _ := os.Hostname()
	token := ""
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatal(err)
		}
		path := filepath.Join(dir, "token")
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			raw := make([]byte, 16)
			_, _ = rand.Read(raw)
			b = []byte(hex.EncodeToString(raw))
			if err := os.WriteFile(path, b, 0600); err != nil {
				log.Fatal(err)
			}
		} else if err != nil {
			log.Fatal(err)
		}
		token = string(b)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if time.Since(started) < duration("READY_DELAY_SECONDS") {
			http.Error(w, "warming", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"hostname": hostname, "token": token, "started": started.UTC().Format(time.RFC3339Nano)})
	})
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	<-ch
	time.Sleep(duration("SHUTDOWN_DELAY_SECONDS"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
