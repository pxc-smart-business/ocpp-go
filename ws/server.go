package ws

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

// ---------------------- SERVER ----------------------

type CheckClientHandler func(id string, r *http.Request) bool

// Server defines a websocket server, which passively listens for incoming connections on ws or wss protocol.
// The offered API are of asynchronous nature, and each incoming connection/message is handled using callbacks.
//
// To create a new ws server, use:
//
//	server := NewServer()
//
// If you need a server with TLS support, pass the following option:
//
//	server := NewServer(WithServerTLSConfig("cert.pem", "privateKey.pem", nil))
//
// To support client basic authentication, use:
//
//	server.SetBasicAuthHandler(func (user, pass) bool {
//		ok := authenticate(user, pass) // ... check for user and pass correctness
//		return ok
//	})
//
// To specify supported sub-protocols, use:
//
//	server.AddSupportedSubprotocol("ocpp1.6")
//
// If you need to set a specific timeout configuration, refer to the SetTimeoutConfig method.
//
// Using Start and Stop you can respectively start and stop listening for incoming client websocket connections.
//
// To be notified of new and terminated connections,
// refer to SetNewClientHandler and SetDisconnectedClientHandler functions.
//
// To receive incoming messages, you will need to set your own handler using SetMessageHandler.
// To write data on the open socket, simply call the Write function.
type Server interface {
	// Starts and runs the websocket server on a specific port and URL.
	// After start, incoming connections and messages are handled automatically, so no explicit read operation is required.
	//
	// The functions blocks forever, hence it is suggested to invoke it in a goroutine, if the caller thread needs to perform other work, e.g.:
	//	go server.Start(8887, "/ws/{id}")
	//	doStuffOnMainThread()
	//	...
	//
	// To stop a running server, call the Stop function.
	Start(port int, listenPath string)
	// Shuts down a running websocket server.
	// All open channels will be forcefully closed, and the previously called Start function will return.
	Stop()
	// Shutdown gracefully shuts the server down, bounding the underlying
	// http.Server.Shutdown by ctx. Stop() delegates here with
	// context.Background(). On early ctx expiry Errors() is closed immediately
	// and any later teardown errors are dropped.
	Shutdown(ctx context.Context) error
	// Closes a specific websocket connection.
	StopConnection(id string, closeError websocket.CloseError) error
	// Errors returns a buffered channel for asynchronous error messages.
	// The channel is created when the server is constructed and is closed by the
	// server when stopped. Delivery is best-effort and lossy: the channel has a
	// small buffer and errors are dropped (not blocked on) when it is full, so a
	// slow or absent consumer never blocks the server. Consume it promptly if you
	// rely on error notifications.
	Errors() <-chan error
	// Sets a callback function for all incoming messages.
	// The callbacks accept a Channel and the received data.
	// It is up to the callback receiver, to check the identifier of the channel, to determine the source of the message.
	SetMessageHandler(handler MessageHandler)
	// SetNewClientHandler sets a callback function for all new incoming client connections.
	// It is recommended to store a reference to the Channel in the received entity, so that the Channel may be recognized later on.
	//
	// The callback is invoked after a connection was established and upgraded successfully.
	// If custom checks need to be run beforehand, refer to SetCheckClientHandler.
	SetNewClientHandler(handler ConnectedHandler)
	// Sets a callback function for all client disconnection events.
	// Once a client is disconnected, it is not possible to read/write on the respective Channel any longer.
	SetDisconnectedClientHandler(handler func(ws Channel))
	// Set custom timeout configuration parameters. If not passed, a default ServerTimeoutConfig struct will be used.
	//
	// This function must be called before starting the server, otherwise it may lead to unexpected behavior.
	SetTimeoutConfig(config ServerTimeoutConfig)
	// Write sends a message on a specific Channel, identifier by the webSocketId parameter.
	// If the passed ID is invalid, an error is returned.
	//
	// The data is queued and will be sent asynchronously in the background.
	Write(webSocketId string, data []byte) error
	// AddSupportedSubprotocol adds support for a specified subprotocol.
	// This is recommended in order to communicate the capabilities to the client during the handshake.
	// If left empty, any subprotocol will be accepted.
	//
	// Duplicates will be removed automatically.
	AddSupportedSubprotocol(subProto string)
	// SetChargePointIdResolver sets the callback function to use for resolving the charge point ID of a charger connecting to
	// the websocket server. By default, this will just be the path in the URL used by the client.
	SetChargePointIdResolver(resolver func(r *http.Request) (string, error))
	// SetBasicAuthHandler enables HTTP Basic Authentication and requires clients to pass credentials.
	// The handler function is called whenever a new client attempts to connect, to check for credentials correctness.
	// The handler must return true if the credentials were correct, false otherwise.
	SetBasicAuthHandler(handler func(username string, password string) bool)
	// SetCheckOriginHandler sets a handler for incoming websocket connections, allowing to perform
	// custom cross-origin checks.
	//
	// By default, if the Origin header is present in the request, and the Origin host is not equal
	// to the Host request header, the websocket handshake fails.
	SetCheckOriginHandler(handler func(r *http.Request) bool)
	// SetCheckClientHandler sets a handler for validate incoming websocket connections, allowing to perform
	// custom client connection checks.
	// The handler is executed before any connection upgrade and allows optionally returning a custom
	// configuration for the web socket that will be created.
	//
	// Changes to the http request at runtime may lead to undefined behavior.
	SetCheckClientHandler(handler CheckClientHandler)
	// Addr gives the address on which the server is listening, useful if, for
	// example, the port is system-defined (set to 0).
	Addr() *net.TCPAddr
	// GetChannel retrieves an active Channel connection by its unique identifier.
	// If a connection with the given ID exists, it returns the corresponding webSocket instance.
	// If no connection is found with the specified ID, it returns nil and a false flag.
	GetChannel(websocketId string) (Channel, bool)
}

// Default implementation of a Websocket server.
//
// Use the NewServer function to create a new server.
type server struct {
	connections           map[string]*webSocket
	gate                  map[string]int
	httpServer            *http.Server
	stopped               bool
	messageHandler        func(ws Channel, data []byte) error
	chargePointIdResolver func(*http.Request) (string, error)
	checkClientHandler    CheckClientHandler
	newClientHandler      func(ws Channel)
	disconnectedHandler   func(ws Channel)
	basicAuthHandler      func(username string, password string) bool
	tlsCertificatePath    string
	tlsCertificateKey     string
	timeoutConfig         ServerTimeoutConfig
	duplicatePolicy       DuplicateConnectionPolicy
	evictionTimeout       time.Duration
	upgrader              websocket.Upgrader
	errC                  chan error
	errClosed             bool // true once errC has been closed, to prevent double-close and sends on a closed channel
	connMutex             sync.RWMutex
	addr                  *net.TCPAddr
	httpHandler           *mux.Router
}

// DuplicateConnectionPolicy controls how the server handles a new connection
// attempt using an ID that already has an active websocket.
type DuplicateConnectionPolicy int

const (
	// KeepCurrent preserves the existing behavior: reject the new duplicate
	// connection and keep the current websocket active.
	KeepCurrent DuplicateConnectionPolicy = iota
	// KeepNew evicts the current websocket and accepts the new duplicate only
	// after the old websocket's disconnect teardown has completed.
	KeepNew
)

// ServerOpt is a function that can be used to set options on a server during creation.
type ServerOpt func(s *server)

// WithServerTLSConfig sets the TLS configuration for the server.
// If the passed tlsConfig is nil, the client will not use TLS.
func WithServerTLSConfig(certificatePath string, certificateKey string, tlsConfig *tls.Config) ServerOpt {
	return func(s *server) {
		s.tlsCertificatePath = certificatePath
		s.tlsCertificateKey = certificateKey
		if tlsConfig != nil {
			s.httpServer.TLSConfig = tlsConfig
		}
	}
}

// WithDuplicateConnectionPolicy sets the duplicate-connection policy for the
// server at construction time. The default is KeepCurrent.
//
// Security caveat: KeepNew lets any client that can authenticate and present
// or guess an active charger ID evict that charger's current connection. Use
// KeepNew only behind an authentication/authorization gate that proves the
// connecting peer is allowed to claim the requested ID.
func WithDuplicateConnectionPolicy(policy DuplicateConnectionPolicy) ServerOpt {
	return func(s *server) {
		s.duplicatePolicy = policy
	}
}

// WithDuplicateConnectionEvictionTimeout sets the bounded wait for KeepNew
// eviction teardown. It is a construction-time option; when unset, the server
// uses a production default of WriteWait plus a dispatcher and handler budget.
func WithDuplicateConnectionEvictionTimeout(d time.Duration) ServerOpt {
	return func(s *server) {
		s.evictionTimeout = d
	}
}

// WithServerReadLimit sets the per-message inbound read limit (bytes) on the
// server's timeout configuration at construction time, leaving every other
// timeout at its default. Unlike SetTimeoutConfig with a struct literal —
// which silently zeroes WriteWait/PingWait — this cannot disable the write
// and ping deadlines.
//
// See ServerTimeoutConfig.ReadLimit for full semantics: the limit is bound at
// upgrade time, exceeding it drops the connection, and any value <= 0 means
// unlimited (the default).
func WithServerReadLimit(limit int64) ServerOpt {
	return func(s *server) {
		s.timeoutConfig.ReadLimit = limit
	}
}

// NewServer Creates a new websocket server.
//
// Additional options may be added using the AddOption function.
//
// By default, the websockets are not secure, and the server will not perform any client certificate verification.
//
// To add TLS support to the server, a valid server certificate path and key must be passed.
// To also add support for client certificate verification, a valid TLSConfig needs to be configured.
// For example:
//
//		tlsConfig := &tls.Config{
//			ClientAuth: tls.RequireAndVerifyClientCert,
//			ClientCAs: clientCAs,
//		}
//	 server := ws.NewServer(ws.WithServerTLSConfig("cert.pem", "privateKey.pem", tlsConfig))
//
// When TLS is correctly configured, the server will automatically use it for all created websocket channels.
func NewServer(opts ...ServerOpt) Server {
	router := mux.NewRouter()
	s := &server{
		connections:   make(map[string]*webSocket),
		gate:          make(map[string]int),
		httpServer:    &http.Server{},
		timeoutConfig: NewServerTimeoutConfig(),
		upgrader:      websocket.Upgrader{Subprotocols: []string{}},
		errC:          make(chan error, 1),
		httpHandler:   router,
		chargePointIdResolver: func(r *http.Request) (string, error) {
			url := r.URL
			return path.Base(url.Path), nil
		},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *server) SetMessageHandler(handler MessageHandler) {
	s.messageHandler = handler
}

func (s *server) SetCheckClientHandler(handler CheckClientHandler) {
	s.checkClientHandler = handler
}

func (s *server) SetNewClientHandler(handler ConnectedHandler) {
	s.newClientHandler = handler
}

func (s *server) SetDisconnectedClientHandler(handler func(ws Channel)) {
	s.disconnectedHandler = handler
}

func (s *server) SetTimeoutConfig(config ServerTimeoutConfig) {
	s.timeoutConfig = config
}

func (s *server) AddSupportedSubprotocol(subProto string) {
	for _, sub := range s.upgrader.Subprotocols {
		if sub == subProto {
			// Don't add duplicates
			return
		}
	}
	s.upgrader.Subprotocols = append(s.upgrader.Subprotocols, subProto)
}

func (s *server) SetChargePointIdResolver(resolver func(r *http.Request) (string, error)) {
	s.chargePointIdResolver = resolver
}

func (s *server) SetBasicAuthHandler(handler func(username string, password string) bool) {
	s.basicAuthHandler = handler
}

func (s *server) SetCheckOriginHandler(handler func(r *http.Request) bool) {
	s.upgrader.CheckOrigin = handler
}

func (s *server) error(err error) {
	log.Error(err)
	// Guard the send with the mutex so it cannot race with Stop() closing the
	// channel. The send is non-blocking (buffered channel with a default case),
	// so the mutex is never held across a blocking channel operation.
	s.connMutex.RLock()
	defer s.connMutex.RUnlock()
	if s.errClosed {
		return
	}
	select {
	case s.errC <- err:
	default:
	}
}

func (s *server) Errors() <-chan error {
	s.connMutex.RLock()
	defer s.connMutex.RUnlock()
	return s.errC
}

func (s *server) Addr() *net.TCPAddr {
	s.connMutex.RLock()
	defer s.connMutex.RUnlock()
	return s.addr
}

func (s *server) AddHttpHandler(listenPath string, handler func(w http.ResponseWriter, r *http.Request)) {
	// The mux router is not safe for concurrent registration. Guard it with the
	// shared mutex so concurrent AddHttpHandler/Start calls don't race.
	s.connMutex.Lock()
	defer s.connMutex.Unlock()
	s.httpHandler.HandleFunc(listenPath, handler)
}

func (s *server) Start(port int, listenPath string) {
	addr := fmt.Sprintf(":%v", port)

	// Configure lifecycle fields under the lock so they don't race with Stop(),
	// Addr() and concurrent AddHttpHandler calls. The lock is released before
	// the blocking Serve call.
	s.connMutex.Lock()
	s.connections = make(map[string]*webSocket)
	s.gate = make(map[string]int)
	s.stopped = false
	if s.httpServer == nil {
		s.httpServer = &http.Server{}
	}
	s.httpServer.Addr = addr
	// Register the websocket handler directly on the router (we already hold the
	// lock, so we must not call AddHttpHandler, which re-acquires it).
	s.httpHandler.HandleFunc(listenPath, func(w http.ResponseWriter, r *http.Request) {
		s.wsHandler(w, r)
	})
	s.httpServer.Handler = s.httpHandler
	// Snapshot the server so we can call Serve without holding the lock.
	httpServer := s.httpServer
	s.connMutex.Unlock()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.error(fmt.Errorf("failed to listen: %w", err))
		return
	}

	s.connMutex.Lock()
	s.addr = ln.Addr().(*net.TCPAddr)
	s.connMutex.Unlock()

	defer ln.Close()

	log.Infof("listening on tcp network %v", addr)
	httpServer.RegisterOnShutdown(s.stopConnections)
	if s.tlsCertificatePath != "" && s.tlsCertificateKey != "" {
		err = httpServer.ServeTLS(ln, s.tlsCertificatePath, s.tlsCertificateKey)
	} else {
		err = httpServer.Serve(ln)
	}

	if !errors.Is(err, http.ErrServerClosed) {
		s.error(fmt.Errorf("failed to listen: %w", err))
	}
}

// Stop shuts the server down and waits (unbounded) for connections to drain.
func (s *server) Stop() { _ = s.Shutdown(context.Background()) }

// Shutdown gracefully shuts the server down, bounding the underlying
// http.Server.Shutdown by ctx. It returns ctx.Err() if ctx is done before that
// completes, or a listener-close error, or nil. NOTE: upgraded ws connections
// are hijacked and closed by the RegisterOnShutdown hook in an un-awaited
// goroutine, so ctx does NOT enforce a hard per-connection deadline. Stop() delegates here with context.Background().
func (s *server) Shutdown(ctx context.Context) error {
	log.Info("stopping websocket server")
	s.connMutex.RLock()
	httpServer := s.httpServer
	s.connMutex.RUnlock()
	var err error
	if httpServer != nil {
		err = httpServer.Shutdown(ctx) // nil, ctx.Err(), or a listener-close error
		if err != nil {
			// Preserve Stop()'s async report; surface before the errC close.
			s.error(fmt.Errorf("shutdown failed: %w", err))
		}
	}
	s.connMutex.Lock()
	if !s.errClosed {
		s.errClosed = true
		close(s.errC)
	}
	s.connMutex.Unlock()
	return err
}

func (s *server) StopConnection(id string, closeError websocket.CloseError) error {
	s.connMutex.RLock()
	w, ok := s.connections[id]
	s.connMutex.RUnlock()

	if !ok {
		return fmt.Errorf("couldn't stop websocket connection. No connection with id %s is open", id)
	}
	log.Debugf("sending stop signal for websocket %s", w.ID())
	return w.Close(closeError)
}

func (s *server) GetChannel(websocketId string) (Channel, bool) {
	s.connMutex.RLock()
	defer s.connMutex.RUnlock()
	c, ok := s.connections[websocketId]
	return c, ok
}

func (s *server) stopConnections() {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()
	s.stopped = true
	for _, conn := range s.connections {
		_ = conn.Close(websocket.CloseError{Code: websocket.CloseNormalClosure, Text: ""})
	}
}

func (s *server) Write(webSocketId string, data []byte) error {
	s.connMutex.RLock()
	w, ok := s.connections[webSocketId]
	s.connMutex.RUnlock()
	if !ok {
		return fmt.Errorf("couldn't write to websocket. No socket with id %v is open", webSocketId)
	}
	log.Debugf("queuing data for websocket %s", webSocketId)
	return w.Write(data)
}

func (s *server) wsHandler(w http.ResponseWriter, r *http.Request) {
	responseHeader := http.Header{}
	id, err := s.chargePointIdResolver(r)
	if err != nil {
		s.error(fmt.Errorf("failed to resolve charge point id"))
		http.Error(w, "NotFound", http.StatusNotFound)
		return
	}
	log.Debugf("handling new connection for %s from %s", id, r.RemoteAddr)
	// Negotiate sub-protocol
	clientSubProtocols := websocket.Subprotocols(r)
	negotiatedSubProtocol := ""
out:
	for _, requestedProto := range clientSubProtocols {
		if len(s.upgrader.Subprotocols) == 0 {
			// All subProtocols are accepted, pick first
			negotiatedSubProtocol = requestedProto
			break
		}
		// Check if requested suprotocol is supported by server
		for _, supportedProto := range s.upgrader.Subprotocols {
			if requestedProto == supportedProto {
				negotiatedSubProtocol = requestedProto
				break out
			}
		}
	}
	if negotiatedSubProtocol != "" {
		responseHeader.Add("Sec-WebSocket-Protocol", negotiatedSubProtocol)
	}
	// Handle client authentication
	if s.basicAuthHandler != nil {
		username, password, ok := r.BasicAuth()
		if ok {
			ok = s.basicAuthHandler(username, password)
		}
		if !ok {
			s.error(fmt.Errorf("basic auth failed: credentials invalid"))
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}
	// Custom client checks
	if s.checkClientHandler != nil {
		ok := s.checkClientHandler(id, r)
		if !ok {
			s.error(fmt.Errorf("client validation: invalid client"))
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Upgrade websocket
	conn, err := s.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		s.error(fmt.Errorf("upgrade failed: %w", err))
		return
	}

	log.Debugf("upgraded websocket connection for %s from %s", id, conn.RemoteAddr().String())
	// If unsupported sub-protocol, terminate the connection immediately
	if negotiatedSubProtocol == "" {
		s.error(fmt.Errorf("unsupported subprotocols %v for new client %v (%v)", clientSubProtocols, id, r.RemoteAddr))
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseProtocolError, "invalid or unsupported subprotocol"),
			time.Now().Add(s.timeoutConfig.WriteWait))
		_ = conn.Close()
		return
	}
	// Create web socket for client, state is automatically set to connected
	wsCfg := NewDefaultWebSocketConfig(
		s.timeoutConfig.WriteWait,
		s.timeoutConfig.PingWait,
		s.timeoutConfig.PingPeriod,
		s.timeoutConfig.PongWait)
	wsCfg.ReadLimit = s.timeoutConfig.ReadLimit
	ws := newWebSocket(
		id,
		conn,
		r.TLS,
		wsCfg,
		s.handleMessage,
		s.handleDisconnect,
		func(_ Channel, err error) {
			s.error(err)
		},
	)
	if !s.registerNewConnection(ws) {
		return
	}
	// Start reader and write routine
	ws.run()
	if s.newClientHandler != nil {
		var channel Channel = ws
		s.newClientHandler(channel)
	}
}

func (s *server) duplicateEvictionTimeout() time.Duration {
	if s.evictionTimeout > 0 {
		return s.evictionTimeout
	}
	return s.timeoutConfig.WriteWait + 4*time.Second
}

func (s *server) rejectConnection(conn *websocket.Conn, code int, reason string) {
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(s.timeoutConfig.WriteWait))
	_ = conn.Close()
}

func (s *server) decrementGate(id string) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()
	s.gate[id]--
	if s.gate[id] <= 0 {
		delete(s.gate, id)
	}
}

func (s *server) registerNewConnection(ws *webSocket) bool {
	id := ws.ID()
	s.connMutex.Lock()
	if s.stopped {
		s.connMutex.Unlock()
		s.error(fmt.Errorf("server is stopping, closing new client %s", id))
		s.rejectConnection(ws.connection, websocket.ClosePolicyViolation, "server shutting down")
		return false
	}
	if s.gate[id] > 0 {
		s.connMutex.Unlock()
		s.error(fmt.Errorf("client %s connection transition in progress, closing duplicate client", id))
		s.rejectConnection(ws.connection, websocket.ClosePolicyViolation, "connection transition in progress")
		return false
	}
	old, exists := s.connections[id]
	if !exists {
		s.connections[id] = ws
		s.connMutex.Unlock()
		return true
	}
	if s.duplicatePolicy != KeepNew {
		s.connMutex.Unlock()
		s.error(fmt.Errorf("client %s already exists, closing duplicate client", id))
		s.rejectConnection(ws.connection, websocket.ClosePolicyViolation, "a connection with this ID already exists")
		return false
	}
	s.gate[id]++
	defer s.decrementGate(id)
	s.connMutex.Unlock()

	if err := old.Close(websocket.CloseError{Code: websocket.ClosePolicyViolation, Text: "reconnected"}); err != nil {
		log.Debugf("closing old duplicate connection for %s returned: %v", id, err)
	}

	timer := time.NewTimer(s.duplicateEvictionTimeout())
	defer timer.Stop()
	select {
	case <-old.teardownDone:
	case <-timer.C:
		s.error(fmt.Errorf("timed out waiting for old duplicate connection %s to tear down", id))
		s.rejectConnection(ws.connection, websocket.ClosePolicyViolation, "timed out waiting for old connection teardown")
		return false
	}

	s.connMutex.Lock()
	if s.stopped {
		s.connMutex.Unlock()
		s.error(fmt.Errorf("server is stopping, closing replacement client %s", id))
		s.rejectConnection(ws.connection, websocket.ClosePolicyViolation, "server shutting down")
		return false
	}
	if _, exists := s.connections[id]; exists {
		s.connMutex.Unlock()
		s.error(fmt.Errorf("client %s still exists after duplicate eviction, closing replacement", id))
		s.rejectConnection(ws.connection, websocket.ClosePolicyViolation, "a connection with this ID already exists")
		return false
	}
	s.connections[id] = ws
	s.connMutex.Unlock()
	return true
}

// --------- Internal callbacks webSocket -> server ---------
func (s *server) handleMessage(w Channel, data []byte) error {
	s.connMutex.RLock()
	current, ok := s.connections[w.ID()]
	isCurrent := ok && current == w
	s.connMutex.RUnlock()
	if !isCurrent {
		log.Debugf("dropping inbound message from stale websocket %s", w.ID())
		return nil
	}
	if s.messageHandler != nil {
		return s.messageHandler(w, data)
	}
	return fmt.Errorf("no message handler set")
}

func (s *server) handleDisconnect(w Channel, _ error) {
	if ws, ok := w.(*webSocket); ok && ws.teardownDone != nil {
		defer ws.teardownOnce.Do(func() {
			close(ws.teardownDone)
		})
	}
	// server never attempts to auto-reconnect to client. Resources are simply freed up.
	//
	// Identity-guarded removal + ordered/guarded disconnect (S4, #292/#387):
	// a stale/superseded socket (e.g. a slow disconnect racing a same-ID
	// reconnect) must not clobber a newer entry in s.connections, nor emit a
	// disconnected event after a newer connected event for the same ID.
	//
	// The transition gate below also prevents a reconnect from registering in
	// the window after this socket is removed but before the disconnect callback
	// chain has returned.
	id := w.ID()
	s.connMutex.Lock()
	s.gate[id]++
	defer s.decrementGate(id)
	current, ok := s.connections[id]
	isCurrent := ok && current == w // pointer identity: *webSocket vs Channel(holding *webSocket) is a valid, well-defined comparison
	if isCurrent {
		delete(s.connections, id)
	}
	s.connMutex.Unlock()
	if !isCurrent {
		// We were already superseded/removed (a newer same-ID connection, or a
		// server-initiated removal) — stay silent. Emitting a disconnect now would
		// land *after* the newer connect and make a live client look gone (#292).
		log.Debugf("suppressed stale disconnect for %s (superseded before removal)", id)
		return
	}
	// Re-check right before firing: a newer connection for this ID may have
	// registered between the Unlock above and here (e.g. a reconnector parked at
	// connMutex.Lock). If so, its newClientHandler has run (or is about to) and
	// THIS disconnect must not be observed after it.
	s.connMutex.RLock()
	_, superseded := s.connections[id]
	s.connMutex.RUnlock()
	if superseded {
		log.Debugf("suppressed stale disconnect for %s (superseded during removal)", id)
		return
	}
	// Log "closed" only when we actually emit the disconnect, so the log and the
	// event stream agree (a suppressed disconnect above does not log a close).
	log.Infof("closed connection to %s", id)
	if s.disconnectedHandler != nil {
		s.disconnectedHandler(w)
	}
}
