//go:build e2e

package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
	"github.com/douglasdf99/litezap/internal/spawner"
	"github.com/douglasdf99/litezap/internal/wire"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestE2EDuplicateMessageID sends the same text twice, with the same WhatsApp message id,
// straight to a running worker (manual verification, real number). Expected on the phone:
// one message. Run with:
//
//	LITEZAP_E2E_DATA_DIR=$PWD/data LITEZAP_E2E_INSTANCE=$ID LITEZAP_E2E_TO=5511...@s.whatsapp.net \
//	  go test -tags=e2e -run TestE2EDuplicateMessageID -v ./internal/worker/
func TestE2EDuplicateMessageID(t *testing.T) {
	dataDir, id, to := os.Getenv("LITEZAP_E2E_DATA_DIR"), os.Getenv("LITEZAP_E2E_INSTANCE"), os.Getenv("LITEZAP_E2E_TO")
	if dataDir == "" || id == "" || to == "" {
		t.Skip("set LITEZAP_E2E_DATA_DIR, LITEZAP_E2E_INSTANCE and LITEZAP_E2E_TO")
	}
	sock := spawner.SocketPath(dataDir, id)
	cc, err := grpc.NewClient("passthrough:///worker",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()), wire.DialOption())
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	cli := workerv1.NewWorkerClient(cc)
	b := make([]byte, 9)
	rand.Read(b)
	msgID := "3EB0" + strings.ToUpper(hex.EncodeToString(b))
	for i := 1; i <= 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		resp, err := cli.Send(ctx, &workerv1.SendRequest{To: to, MessageId: msgID, Type: "text", Text: "teste de id duplicado " + msgID[4:10]})
		cancel()
		t.Logf("send #%d: %v, %v", i, resp, err)
	}
	t.Logf("check the phone: exactly one message %q", "teste de id duplicado "+msgID[4:10])
}
