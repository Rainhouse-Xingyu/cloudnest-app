//go:build integration && network

package integration

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

// Real default 30-second write deadlines are tested separately so they cannot
// consume the ordinary six-minute correctness-suite budget.
func TestDownloadNetworkStalledPeersReleaseLeases(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	rateSetting(t, f, "upload_speed_limit_kbps", "10240")
	rateSetting(t, f, "download_speed_limit_kbps", "10240")
	payload := bytes.Repeat([]byte("network-stream-original-"), 750000) // > HTTP/2 stream receive window
	file := f.upload(t, f.alice, "network.bin", payload)
	for _, protocol := range []string{"http1", "http2"} {
		t.Run(protocol, func(t *testing.T) {
			done := make(chan struct{}, 4)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() { done <- struct{}{} }()
				f.router.ServeHTTP(w, r)
			}))
			server.EnableHTTP2 = protocol == "http2"
			if server.EnableHTTP2 {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			defer server.CloseClientConnections()
			address := fmt.Sprintf("%s/api/files/%d/download", server.URL, file.ID)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+f.alice)
			connections := make(chan net.Conn, 2)
			trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { connections <- info.Conn }}
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
			var response *http.Response
			var tcp *net.TCPConn
			if protocol == "http1" {
				conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				tcp = conn.(*net.TCPConn)
				defer tcp.Close()
				if err := tcp.SetReadBuffer(1024); err != nil {
					t.Fatal(err)
				}
				tcp.SetDeadline(time.Now().Add(time.Minute))
				if err := req.Write(tcp); err != nil {
					t.Fatal(err)
				}
				response, err = http.ReadResponse(bufio.NewReader(tcp), req)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				response, err = server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if response.ProtoMajor != 2 {
					t.Fatal("HTTP/2 was not negotiated", response.Proto)
				}
			}
			if response.StatusCode != 200 || response.ContentLength != int64(len(payload)) {
				t.Fatal(response.Status, response.ContentLength)
			}
			// Do not read or close the body until the server has timed itself out.
			// Neither TCP disconnect nor client cancellation can cause the release.
			if leaseCount(t, f, *file.StorageID) != 1 {
				t.Fatal("stalled download did not retain its lease")
			}
			start := time.Now()
			select {
			case <-done:
				if time.Since(start) < 25*time.Second {
					t.Fatal("download ended before exercising the real 30-second timeout")
				}
			case <-time.After(45 * time.Second):
				t.Fatal("stalled download outlived its write deadline")
			}
			waitNoReadLeases(t, f, *file.StorageID)
			if tcp != nil {
				if err := tcp.SetReadBuffer(1 << 20); err != nil {
					t.Fatal(err)
				}
				tcp.SetReadDeadline(time.Now().Add(15 * time.Second))
			}
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if timeout, ok := readErr.(net.Error); ok && timeout.Timeout() {
				t.Fatal("client read deadline, rather than server termination, ended the response", readErr)
			}
			if readErr == nil || len(data) >= len(payload) || !bytes.Equal(data, payload[:len(data)]) {
				t.Fatal("timed-out response was not a truncated original prefix", len(data), readErr)
			}
			if protocol == "http2" {
				first := <-connections
				next, _ := http.NewRequestWithContext(ctx, "GET", address, nil)
				next.Header.Set("Authorization", "Bearer "+f.alice)
				next.Header.Set("Range", "bytes=0-3")
				next = next.WithContext(httptrace.WithClientTrace(next.Context(), trace))
				resumed, err := server.Client().Do(next)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resumed.Body)
				resumed.Body.Close()
				if err != nil || resumed.StatusCode != 206 || !bytes.Equal(body, payload[:4]) || <-connections != first {
					t.Fatal("one stalled stream poisoned the shared HTTP/2 connection", resumed.Status, err)
				}
				<-done
				waitNoReadLeases(t, f, *file.StorageID)
			}
		})
	}
}

func TestDownloadNetworkHealthyPacingCanOutliveWriteTimeout(t *testing.T) {
	f := newFixture(t)
	resetRateSettings(t, f)
	payload := bytes.Repeat([]byte("paced"), 6600) // 32.2 seconds at 1 KiB/s
	file := f.upload(t, f.alice, "paced-network.bin", payload)
	server := httptest.NewUnstartedServer(f.router)
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Minute
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/files/%d/download?rate_kbps=1", server.URL, file.ID), nil)
	req.Header.Set("Authorization", "Bearer "+f.alice)
	start := time.Now()
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || response.ProtoMajor != 2 || !bytes.Equal(body, payload) || time.Since(start) < 30*time.Second {
		t.Fatal("healthy paced HTTP/2 transfer was cut short", response.Status, len(body), err)
	}
	waitNoReadLeases(t, f, *file.StorageID)
}
