package wire

import (
	"bytes"
	"context"
	"net"
	"testing"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type bigMessages struct {
	workerv1.UnimplementedWorkerServer
}

// A history chunk can exceed gRPC's 4MB default.
func (bigMessages) GetContacts(context.Context, *workerv1.GetContactsRequest) (*workerv1.GetContactsResponse, error) {
	return &workerv1.GetContactsResponse{Contacts: []*workerv1.Contact{{Name: string(bytes.Repeat([]byte{'a'}, 10<<20))}}}, nil
}

func (bigMessages) CheckNumbers(_ context.Context, req *workerv1.CheckNumbersRequest) (*workerv1.CheckNumbersResponse, error) {
	return &workerv1.CheckNumbersResponse{Results: []*workerv1.NumberStatus{{Phone: req.Phones[0][:3]}}}, nil
}

func TestLargeMessagesPass(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer(ServerOptions()...)
	workerv1.RegisterWorkerServer(g, bigMessages{})
	go g.Serve(lis)
	defer g.Stop()
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()), DialOption())
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	cli := workerv1.NewWorkerClient(cc)
	resp, err := cli.GetContacts(context.Background(), &workerv1.GetContactsRequest{})
	if err != nil || len(resp.Contacts[0].Name) != 10<<20 {
		t.Fatalf("large response: %v", err)
	}
	big := string(bytes.Repeat([]byte{'5'}, 10<<20))
	if _, err := cli.CheckNumbers(context.Background(), &workerv1.CheckNumbersRequest{Phones: []string{big}}); err != nil {
		t.Fatalf("large request: %v", err)
	}
}
