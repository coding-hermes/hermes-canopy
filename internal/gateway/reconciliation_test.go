package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLongNodeRunOutlivesControlBudget(t *testing.T) {
	const budget = 60 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runs":
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"run_id":"long"}`)
		case "/v1/runs/long/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"event\":\"message.delta\",\"delta\":\"start\"}\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(2 * budget)
			fmt.Fprint(w, "data: {\"event\":\"run.completed\",\"output\":\"finished\"}\n\n")
		}
	}))
	defer srv.Close()
	client, _ := NewClient(srv.URL, "", WithHTTPClient(&http.Client{Timeout: budget}))
	if client.streamHTTP.Timeout != 0 {
		t.Fatal("stream client has a whole-request timeout")
	}
	svc := NewService(client)
	defer svc.Close()
	sink := &runOutputSinkStub{}
	svc.SetRunOutputSink(sink)
	if _, err := svc.StartRunWithContext(context.Background(), StartRunInput{Message: "x", SourceNodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, svc, "long", "completed")
	svc.Close()
	rec, _ := svc.Run("long")
	if rec.Output != "finished" {
		t.Fatalf("output: %+v", rec)
	}
	calls := sink.snapshot()
	if len(calls) != 1 || calls[0].Output != "finished" {
		t.Fatalf("sink: %+v", calls)
	}
}

func TestDroppedStreamReconciliation(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			var gets atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/runs":
					w.WriteHeader(http.StatusAccepted)
					fmt.Fprint(w, `{"run_id":"drop"}`)
				case "/v1/runs/drop/events":
					fmt.Fprint(w, "data: {\"event\":\"message.delta\",\"delta\":\"partial\"}\n\n")
				case "/v1/runs/drop":
					gets.Add(1)
					if !success {
						<-r.Context().Done()
						return
					}
					fmt.Fprint(w, `{"status":"completed","output":"recovered","usage":{"total_tokens":7}}`)
				}
			}))
			defer srv.Close()
			client, _ := NewClient(srv.URL, "", WithHTTPClient(&http.Client{Timeout: 80 * time.Millisecond}))
			svc := NewService(client)
			defer svc.Close()
			sink := &runOutputSinkStub{}
			svc.SetRunOutputSink(sink)
			if _, err := svc.StartRunWithContext(context.Background(), StartRunInput{Message: "x", SourceNodeID: "node"}); err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if success {
				want = "completed"
			}
			waitForStatus(t, svc, "drop", want)
			svc.Close()
			rec, _ := svc.Run("drop")
			if gets.Load() != 1 {
				t.Fatalf("GET attempts: %d", gets.Load())
			}
			if success {
				if rec.Output != "recovered" || rec.Usage["total_tokens"] != float64(7) {
					t.Fatalf("record: %+v", rec)
				}
				calls := sink.snapshot()
				if len(calls) != 1 || calls[0].Output != "recovered" {
					t.Fatalf("sink: %+v", calls)
				}
			} else if !strings.Contains(rec.Error, "reconciliation failed") {
				t.Fatalf("error: %+v", rec)
			}
		})
	}
}

func TestControlCallsKeepTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer srv.Close()
	client, _ := NewClient(srv.URL, "", WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))
	for _, call := range []func() error{
		func() error { _, err := client.StopRun(context.Background(), "x"); return err },
		func() error { return client.RespondApproval(context.Background(), "x", "a", "once") },
	} {
		start := time.Now()
		err := call()
		if err == nil || time.Since(start) > time.Second {
			t.Fatalf("control timeout: %v after %v", err, time.Since(start))
		}
	}
}
