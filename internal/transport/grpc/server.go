package grpctransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	hellov1 "github.com/lihongjie0209/config-service/gen/hello/v1"
	"github.com/lihongjie0209/config-service/internal/apperror"
	"github.com/lihongjie0209/config-service/internal/auth"
	"github.com/lihongjie0209/config-service/internal/buildinfo"
	"github.com/lihongjie0209/config-service/internal/config"
	configdomain "github.com/lihongjie0209/config-service/internal/dynamicconfig"
	"github.com/lihongjie0209/config-service/internal/environment"
	apphealth "github.com/lihongjie0209/config-service/internal/health"
	"github.com/lihongjie0209/config-service/internal/idempotency"
	"github.com/lihongjie0209/config-service/internal/observability"
	"github.com/lihongjie0209/config-service/internal/principal"
	"github.com/lihongjie0209/config-service/internal/requestid"

	commonv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/common/v1"
	configv1 "github.com/lihongjie0209/platform-protos/gen/go/platform/config/v1"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Server struct {
	server  *grpc.Server
	address string
	logger  *slog.Logger
}

func NewServer(lc fx.Lifecycle, cfg config.Config, authService *auth.Service, healthService *apphealth.Service, configService *configdomain.Service, metrics *observability.Metrics, logger *slog.Logger) (*Server, error) {
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.GRPC.MaxReceiveBytes),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(environmentInterceptor(cfg.Runtime.ActiveProfile), requestIDInterceptor, idempotencyInterceptor, recoveryInterceptor(logger), authInterceptor(authService, cfg.Auth), metricsInterceptor(metrics, logger)),
		grpc.ChainStreamInterceptor(environmentStreamInterceptor(cfg.Runtime.ActiveProfile), requestIDStreamInterceptor, idempotencyStreamInterceptor, recoveryStreamInterceptor(logger), authStreamInterceptor(authService, cfg.Auth), metricsStreamInterceptor(metrics, logger)),
	}
	if cfg.GRPC.TLS.Enabled {
		creds, err := serverCredentials(cfg.GRPC.TLS)
		if err != nil {
			return nil, err
		}
		options = append(options, grpc.Creds(creds))
	}
	grpcServer := grpc.NewServer(options...)
	hellov1.RegisterHelloServiceServer(grpcServer, &helloServer{})
	configv1.RegisterConfigServiceServer(grpcServer, &configServer{service: configService})
	grpc_health_v1.RegisterHealthServer(grpcServer, &healthServer{health: healthService})
	if cfg.GRPC.ReflectionEnabled {
		reflection.Register(grpcServer)
	}
	server := &Server{server: grpcServer, address: cfg.GRPC.Address, logger: logger}
	lc.Append(fx.Hook{OnStart: server.start(cfg.GRPC.Enabled), OnStop: server.stop})
	return server, nil
}

type configServer struct {
	configv1.UnimplementedConfigServiceServer
	service *configdomain.Service
}

func (s *configServer) PutDraft(ctx context.Context, request *configv1.PutDraftRequest) (*configv1.PutDraftResponse, error) {
	if request.GetEntry() == nil {
		return nil, status.Error(codes.InvalidArgument, "entry is required")
	}
	value, err := s.service.PutDraft(ctx, fromProtoConfig(request.GetEntry()), request.GetExpectedVersion())
	if err != nil {
		return nil, grpcError(err)
	}
	return &configv1.PutDraftResponse{Entry: toProtoConfig(value)}, nil
}
func (s *configServer) Publish(ctx context.Context, request *configv1.PublishRequest) (*configv1.PublishResponse, error) {
	value, err := s.service.Publish(ctx, request.GetId(), request.GetExpectedVersion())
	if err != nil {
		return nil, grpcError(err)
	}
	return &configv1.PublishResponse{Entry: toProtoConfig(value)}, nil
}
func (s *configServer) SubmitForApproval(ctx context.Context, request *configv1.SubmitForApprovalRequest) (*configv1.SubmitForApprovalResponse, error) {
	value, err := s.service.SubmitForApproval(ctx, request.GetId(), request.GetExpectedVersion())
	if err != nil {
		return nil, grpcError(err)
	}
	return &configv1.SubmitForApprovalResponse{Entry: toProtoConfig(value)}, nil
}
func (s *configServer) Approve(ctx context.Context, request *configv1.ApproveRequest) (*configv1.ApproveResponse, error) {
	value, err := s.service.Approve(ctx, request.GetId(), request.GetExpectedVersion(), request.GetComment())
	if err != nil {
		return nil, grpcError(err)
	}
	return &configv1.ApproveResponse{Entry: toProtoConfig(value)}, nil
}
func (s *configServer) Reject(ctx context.Context, request *configv1.RejectRequest) (*configv1.RejectResponse, error) {
	value, err := s.service.Reject(ctx, request.GetId(), request.GetExpectedVersion(), request.GetReason())
	if err != nil {
		return nil, grpcError(err)
	}
	return &configv1.RejectResponse{Entry: toProtoConfig(value)}, nil
}
func (s *configServer) Rollback(ctx context.Context, request *configv1.RollbackRequest) (*configv1.RollbackResponse, error) {
	value, err := s.service.Rollback(ctx, request.GetId(), request.GetTargetRevision(), request.GetExpectedVersion())
	if err != nil {
		return nil, grpcError(err)
	}
	return &configv1.RollbackResponse{Entry: toProtoConfig(value)}, nil
}
func (s *configServer) Resolve(ctx context.Context, request *configv1.ResolveRequest) (*configv1.ResolveResponse, error) {
	values, etag, err := s.service.Resolve(ctx, request.GetEnvironment(), request.GetTenantId(), request.GetService(), request.GetSubjectId(), request.GetKeys())
	if err != nil {
		return nil, grpcError(err)
	}
	response := &configv1.ResolveResponse{Etag: etag}
	for _, value := range values {
		response.Entries = append(response.Entries, toProtoConfig(value))
	}
	return response, nil
}
func (s *configServer) List(ctx context.Context, request *configv1.ListRequest) (*configv1.ListResponse, error) {
	page, pageSize := 0, 0
	if request.GetPage() != nil {
		page, pageSize = int(request.GetPage().GetPage()), int(request.GetPage().GetPageSize())
	}
	result, err := s.service.List(ctx, request.GetEnvironment(), request.GetTenantId(), request.GetService(), page, pageSize)
	if err != nil {
		return nil, grpcError(err)
	}
	response := &configv1.ListResponse{Page: &commonv1.PageResult{Total: uint64(result.Total), Page: uint32(result.Page), PageSize: uint32(result.PageSize)}}
	for _, value := range result.Entries {
		response.Entries = append(response.Entries, toProtoConfig(value))
	}
	return response, nil
}
func fromProtoConfig(value *configv1.ConfigEntry) configdomain.Entry {
	entry := configdomain.Entry{ID: value.GetId(), Environment: value.GetEnvironment(), TenantID: value.GetTenantId(), Service: value.GetService(), Key: value.GetKey(), Value: value.GetValue(), SecretRef: value.GetSecretRef(), Status: value.GetStatus(), Revision: value.GetRevision(), RolloutPercentage: value.GetRolloutPercentage(), PublishedRevision: value.GetPublishedRevision(), ReviewComment: value.GetReviewComment(), ReviewedBy: value.GetReviewedBy(), Version: value.GetVersion()}
	if value.GetReviewedAt() != nil && value.GetReviewedAt().IsValid() {
		reviewedAt := value.GetReviewedAt().AsTime()
		entry.ReviewedAt = &reviewedAt
	}
	return entry
}
func toProtoConfig(value configdomain.Entry) *configv1.ConfigEntry {
	entry := &configv1.ConfigEntry{Id: value.ID, Environment: value.Environment, TenantId: value.TenantID, Service: value.Service, Key: value.Key, Value: value.Value, SecretRef: value.SecretRef, Status: value.Status, Revision: value.Revision, RolloutPercentage: value.RolloutPercentage, PublishedRevision: value.PublishedRevision, ReviewComment: value.ReviewComment, ReviewedBy: value.ReviewedBy, Version: value.Version, CreatedAt: timestamppb.New(value.CreatedAt), UpdatedAt: timestamppb.New(value.UpdatedAt), CreatedBy: value.CreatedBy, UpdatedBy: value.UpdatedBy}
	if value.ReviewedAt != nil {
		entry.ReviewedAt = timestamppb.New(*value.ReviewedAt)
	}
	return entry
}

func (s *Server) start(enabled bool) func(context.Context) error {
	return func(context.Context) error {
		if !enabled {
			s.logger.Warn("grpc server is disabled")
			return nil
		}
		listener, err := net.Listen("tcp", s.address)
		if err != nil {
			return fmt.Errorf("listen grpc: %w", err)
		}
		go func() {
			if err := s.server.Serve(listener); err != nil {
				s.logger.Error("grpc server stopped unexpectedly", "error", err)
			}
		}()
		s.logger.Info("grpc server started", "address", s.address)
		return nil
	}
}
func (s *Server) stop(ctx context.Context) error {
	stopped := make(chan struct{})
	go func() { s.server.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
		return nil
	case <-ctx.Done():
		s.server.Stop()
		return ctx.Err()
	}
}

type helloServer struct {
	hellov1.UnimplementedHelloServiceServer
}

func (*helloServer) Ping(ctx context.Context, request *hellov1.PingRequest) (*hellov1.PingResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if strings.TrimSpace(request.GetMessage()) == "" {
		return nil, status.Error(codes.InvalidArgument, "message is required")
	}
	return &hellov1.PingResponse{Message: request.GetMessage(), Version: buildinfo.Version}, nil
}

type healthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	health *apphealth.Service
}

func grpcError(err error) error {
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		return status.Error(codes.Internal, "internal server error")
	}
	code := codes.Internal
	switch appErr.Code {
	case apperror.CodeInvalidArgument:
		code = codes.InvalidArgument
	case apperror.CodeNotFound:
		code = codes.NotFound
	case apperror.CodeConflict:
		code = codes.Aborted
	case apperror.CodeDependencyUnavailable:
		code = codes.Unavailable
	}
	return status.Error(code, appErr.Message)
}

func (s *healthServer) Check(ctx context.Context, _ *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	_, ready := s.health.Ready(ctx)
	serving := grpc_health_v1.HealthCheckResponse_NOT_SERVING
	if ready {
		serving = grpc_health_v1.HealthCheckResponse_SERVING
	}
	return &grpc_health_v1.HealthCheckResponse{Status: serving}, nil
}
func (s *healthServer) List(context.Context, *grpc_health_v1.HealthListRequest) (*grpc_health_v1.HealthListResponse, error) {
	return &grpc_health_v1.HealthListResponse{Statuses: map[string]*grpc_health_v1.HealthCheckResponse{"": {Status: grpc_health_v1.HealthCheckResponse_SERVING}}}, nil
}

func requestIDInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	id := ""
	if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) > 0 && requestid.Valid(values[0]) {
		id = values[0]
	}
	if id == "" {
		id = requestid.Generate()
	}
	header := metadata.Pairs("x-request-id", id)
	_ = grpc.SetHeader(ctx, header)
	return handler(requestid.WithContext(ctx, id), req)
}
func idempotencyInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	values := metadata.ValueFromIncomingContext(ctx, "idempotency-key")
	if len(values) == 0 {
		return handler(ctx, req)
	}
	if !idempotency.Valid(values[0]) {
		return nil, status.Error(codes.InvalidArgument, "invalid idempotency-key")
	}
	return handler(idempotency.WithContext(ctx, values[0]), req)
}
func environmentInterceptor(profile string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(environment.WithContext(ctx, profile), req)
	}
}
func authInterceptor(service *auth.Service, cfg config.Auth) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		authCtx, err := authenticateGRPC(ctx, info.FullMethod, service, cfg)
		if err != nil {
			return nil, err
		}
		return handler(authCtx, req)
	}
}

func authenticateGRPC(ctx context.Context, method string, service *auth.Service, cfg config.Auth) (context.Context, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if cfg.PSK.Enabled && auth.MatchesAny(method, cfg.PSK.GRPCMethods) {
		if len(values) == 0 || !auth.VerifyPSK(values[0], cfg.PSK.Key) {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid PSK")
		}
		return principal.WithContext(ctx, principal.Principal{Subject: "psk", Method: principal.AuthenticationPSK}), nil
	}
	if auth.MatchesAny(method, cfg.SkipGRPCMethods) {
		return ctx, nil
	}
	if len(values) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing bearer token")
	}
	scheme, raw, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return nil, status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	caller, err := service.Verify(ctx, raw)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired token")
	}
	return principal.WithContext(ctx, caller), nil
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }

func environmentStreamInterceptor(profile string) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: environment.WithContext(stream.Context(), profile)})
	}
}

func requestIDStreamInterceptor(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := stream.Context()
	id := ""
	if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) > 0 && requestid.Valid(values[0]) {
		id = values[0]
	}
	if id == "" {
		id = requestid.Generate()
	}
	if err := stream.SetHeader(metadata.Pairs("x-request-id", id)); err != nil {
		return status.Error(codes.Internal, "set request metadata")
	}
	return handler(srv, &contextServerStream{ServerStream: stream, ctx: requestid.WithContext(ctx, id)})
}

func idempotencyStreamInterceptor(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	values := metadata.ValueFromIncomingContext(stream.Context(), "idempotency-key")
	if len(values) == 0 {
		return handler(srv, stream)
	}
	if !idempotency.Valid(values[0]) {
		return status.Error(codes.InvalidArgument, "invalid idempotency-key")
	}
	return handler(srv, &contextServerStream{ServerStream: stream, ctx: idempotency.WithContext(stream.Context(), values[0])})
}

func authStreamInterceptor(service *auth.Service, cfg config.Auth) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticateGRPC(stream.Context(), info.FullMethod, service, cfg)
		if err != nil {
			return err
		}
		return handler(srv, &contextServerStream{ServerStream: stream, ctx: ctx})
	}
}

func recoveryStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(stream.Context(), "grpc stream panic recovered", "method", info.FullMethod, "panic", recovered)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, stream)
	}
}

func metricsStreamInterceptor(metrics *observability.Metrics, logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		started := time.Now()
		err := handler(srv, stream)
		code := status.Code(err)
		if metrics.Enabled() {
			metrics.GRPCRequests.WithLabelValues(info.FullMethod, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(info.FullMethod).Observe(time.Since(started).Seconds())
		}
		requestID, _ := requestid.FromContext(stream.Context())
		logger.InfoContext(stream.Context(), "grpc stream", "request_id", requestID, "method", info.FullMethod, "code", code.String(), "duration", time.Since(started))
		return err
	}
}

func recoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(ctx, "grpc panic recovered", "method", info.FullMethod, "panic", recovered)
				err = status.Error(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}
func metricsInterceptor(metrics *observability.Metrics, logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		response, err := handler(ctx, req)
		code := status.Code(err)
		if metrics.Enabled() {
			metrics.GRPCRequests.WithLabelValues(info.FullMethod, code.String()).Inc()
			metrics.GRPCDuration.WithLabelValues(info.FullMethod).Observe(time.Since(started).Seconds())
		}
		span := trace.SpanFromContext(ctx).SpanContext()
		requestID, _ := requestid.FromContext(ctx)
		logger.InfoContext(ctx, "grpc request", "request_id", requestID, "trace_id", span.TraceID().String(), "span_id", span.SpanID().String(), "method", info.FullMethod, "code", code.String(), "duration", time.Since(started))
		return response, err
	}
}

func serverCredentials(cfg config.GRPCTLS) (credentials.TransportCredentials, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load grpc certificate: %w", err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	if cfg.ClientCAFile != "" {
		pem, err := os.ReadFile(cfg.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read grpc client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("parse grpc client CA")
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(tlsConfig), nil
}

var Module = fx.Module("grpc", fx.Provide(NewServer), fx.Invoke(func(*Server) {}))
