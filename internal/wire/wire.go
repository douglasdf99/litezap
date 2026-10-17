// Package wire holds the gRPC settings shared by the supervisor and the workers.
package wire

import "google.golang.org/grpc"

// MaxMsgBytes bounds one gRPC message. Media goes through files, not gRPC; the largest
// messages are history chunks (hundreds of messages with media references).
const MaxMsgBytes = 32 << 20

// ServerOptions are used by every worker gRPC server (real and fake).
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.MaxRecvMsgSize(MaxMsgBytes), grpc.MaxSendMsgSize(MaxMsgBytes)}
}

// DialOption is used by every client of a worker.
func DialOption() grpc.DialOption {
	return grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMsgBytes), grpc.MaxCallSendMsgSize(MaxMsgBytes))
}
