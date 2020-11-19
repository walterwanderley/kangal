package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"contrib.go.opencensus.io/exporter/prometheus"
	grpcMiddleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpcZap "github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpcRecovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	grpcCtxTags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	kube "github.com/hellofresh/kangal/pkg/kubernetes"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	grpcProxyV2 "github.com/hellofresh/kangal/pkg/proxy/rpc/pb/grpc/proxy/v2"
)

// Runner encapsulates all Kangal EXPERIMENTAL Proxy API server dependencies
type APIRunner struct {
	Config     GRPCConfig
	Exporter   *prometheus.Exporter
	KubeClient *kube.Client
	Logger     *zap.Logger
	Debug      bool
}

// RunAPIServer runs Kangal EXPERIMENTAL proxy API
func RunAPIServer(ctx context.Context, cfg Config, rr APIRunner) error {
	opts := []grpc.ServerOption{
		grpcMiddleware.WithUnaryServerChain(
			grpcCtxTags.UnaryServerInterceptor(grpcCtxTags.WithFieldExtractor(grpcCtxTags.CodeGenRequestFieldExtractor)),
			grpcZap.UnaryServerInterceptor(rr.Logger),
			grpcRecovery.UnaryServerInterceptor(),
		),
	}

	serverAPI := grpc.NewServer(opts...)

	loadTestServiceServer := NewLoadTestServiceServer()

	grpcProxyV2.RegisterLoadTestServiceServer(serverAPI, loadTestServiceServer)

	if rr.Debug {
		rr.Logger.Warn("Running gRPC in debug mode with server reflection registered")
		reflection.Register(serverAPI)
	}

	grpcAddress := fmt.Sprintf(":%d", cfg.GRPC.PortAPI)
	restAddress := fmt.Sprintf(":%d", cfg.GRPC.PortREST)

	var g errgroup.Group

	g.Go(func() error {
		tcpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPC.PortAPI))
		if err != nil {
			return fmt.Errorf("could not create API TCP listener: %w", err)
		}

		rr.Logger.Info("Running gRPC API server...", zap.String("addr", grpcAddress))
		if err := serverAPI.Serve(tcpListener); err != nil {
			return fmt.Errorf("could not serve gRPC API: %w", err)
		}

		return nil
	})

	g.Go(func() error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		mux := runtime.NewServeMux()

		opts := []grpc.DialOption{grpc.WithInsecure()}
		err := grpcProxyV2.RegisterLoadTestServiceHandlerFromEndpoint(ctx, mux, grpcAddress, opts)
		if err != nil {
			return fmt.Errorf("could not register service Ping: %w", err)
		}

		rr.Logger.Info("Running gRPC REST server...", zap.String("addr", restAddress))
		return http.ListenAndServe(restAddress, mux)
	})

	err := g.Wait()
	return err
}
