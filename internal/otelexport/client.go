package otelexport

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// logClient sends one OTLP logs export request.
type logClient interface {
	export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) error
	close()
}

// retryable marks a failure worth retrying (collector overloaded or briefly
// unavailable). Its text is a status code, never response content.
type retryable struct{ msg string }

func (r retryable) Error() string { return r.msg }

func newLogClient(e Exporter) (logClient, error) {
	switch e.Protocol {
	case "", "http":
		return newHTTPClient(e), nil
	case "grpc":
		return newGRPCClient(e)
	default:
		return nil, fmt.Errorf("unsupported protocol %q", e.Protocol)
	}
}

type httpClient struct {
	url     string
	headers map[string]string
	hc      *http.Client
}

func newHTTPClient(e Exporter) *httpClient {
	scheme := "https"
	if e.Insecure {
		scheme = "http"
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &httpClient{
		url:     scheme + "://" + e.Endpoint + "/v1/logs",
		headers: e.Headers,
		hc: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
			// A redirected POST would become a GET and lose the batch.
			return http.ErrUseLastResponse
		}},
	}
}

func (c *httpClient) export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) error {
	body, err := proto.Marshal(req)
	if err != nil {
		return errors.New("encode export request")
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("build export request")
	}
	hr.Header.Set("Content-Type", "application/x-protobuf")
	for k, v := range c.headers {
		hr.Header.Set(k, v)
	}
	resp, err := c.hc.Do(hr)
	if err != nil {
		// Transport errors name the URL (host:port) only; treat as transient.
		return retryable{msg: "collector unreachable"}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		var result collogspb.ExportLogsServiceResponse
		if readErr != nil || len(body) > 64<<10 || proto.Unmarshal(body, &result) != nil {
			return errors.New("invalid collector acknowledgement")
		}
		return accepted(&result)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode == http.StatusBadGateway,
		resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode == http.StatusGatewayTimeout:
		return retryable{msg: fmt.Sprintf("collector status %d", resp.StatusCode)}
	default:
		return fmt.Errorf("collector status %d", resp.StatusCode)
	}
}

func (c *httpClient) close() { c.hc.CloseIdleConnections() }

type grpcClient struct {
	conn   *grpc.ClientConn
	client collogspb.LogsServiceClient
	md     metadata.MD
}

func newGRPCClient(e Exporter) (*grpcClient, error) {
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if e.Insecure {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(e.Endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, errors.New("invalid gRPC endpoint")
	}
	return &grpcClient{conn: conn, client: collogspb.NewLogsServiceClient(conn), md: metadata.New(e.Headers)}, nil
}

func (c *grpcClient) export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) error {
	result, err := c.client.Export(metadata.NewOutgoingContext(ctx, c.md), req)
	if err == nil {
		return accepted(result)
	}
	// Only the code is reported: a server's status message is not ours to log.
	code := status.Code(err)
	switch code {
	case codes.Unavailable, codes.ResourceExhausted, codes.DeadlineExceeded, codes.Aborted:
		return retryable{msg: "collector code " + code.String()}
	default:
		return errors.New("collector code " + code.String())
	}
}

func (c *grpcClient) close() { _ = c.conn.Close() }

func accepted(result *collogspb.ExportLogsServiceResponse) error {
	if result == nil || result.GetPartialSuccess().GetRejectedLogRecords() != 0 {
		return errors.New("collector rejected part of the batch")
	}
	return nil
}
