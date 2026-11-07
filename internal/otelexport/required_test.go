package otelexport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inferplane/inferplane/internal/audit"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type partialGRPC struct {
	collogspb.UnimplementedLogsServiceServer
}

func (partialGRPC) Export(context.Context, *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	return &collogspb.ExportLogsServiceResponse{PartialSuccess: &collogspb.ExportLogsPartialSuccess{RejectedLogRecords: 1}}, nil
}
func TestRequiredGRPCPartialSuccessIsNotDelivery(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	collogspb.RegisterLogsServiceServer(server, partialGRPC{})
	go server.Serve(ln)
	defer server.Stop()
	set, err := New("test", []Exporter{{Name: "archive", Endpoint: ln.Addr().String(), Protocol: "grpc", Insecure: true, Signals: []string{SignalAudit}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Shutdown(context.Background())
	sink, err := set.RequiredAuditSink("archive")
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Write([]byte(`{"event":"test"}`)); err == nil {
		t.Fatal("partial gRPC acceptance discarded required evidence")
	}
}

func TestRequiredAuditOnlyDeletesFullyAcceptedRecords(t *testing.T) {
	for _, mode := range []string{"success", "partial", "invalid", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "partial":
					raw, _ := proto.Marshal(&collogspb.ExportLogsServiceResponse{PartialSuccess: &collogspb.ExportLogsPartialSuccess{RejectedLogRecords: 1}})
					w.Write(raw)
				case "invalid":
					w.Write([]byte("not protobuf"))
				case "unavailable":
					w.WriteHeader(503)
				default:
					w.WriteHeader(200)
				}
			}))
			defer srv.Close()
			set, err := New("test", []Exporter{{Name: "required", Endpoint: strings.TrimPrefix(srv.URL, "http://"), Insecure: true, Signals: []string{SignalAudit}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer set.Shutdown(context.Background())
			sink, err := set.RequiredAuditSink("required")
			if err != nil {
				t.Fatal(err)
			}
			defer sink.Close()
			path := filepath.Join(t.TempDir(), "audit.wal")
			w, err := audit.NewWriter("test", path, []audit.Sink{sink})
			if err != nil {
				t.Fatal(err)
			}
			err = w.AppendDurable(context.Background(), audit.Record{Event: "key_create_requested"})
			// AppendDurable acknowledges local durability under buffer_then_block.
			// Only the sink's full collector acknowledgement permits reclamation.
			if err != nil {
				t.Fatalf("local durable write: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || (len(raw) == 0) != (mode == "success") {
				t.Fatalf("incorrect retention: %d bytes, %v", len(raw), err)
			}
		})
	}
}
